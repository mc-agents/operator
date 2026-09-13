# mc-agents operator

Keeps Minecraft bot pods alive for the [mc-agents MCP server](https://github.com/mc-agents/mcp-server).
One `MinecraftBot` is one pod; a `MinecraftBotPool` is a replica count you can scale.

The design this implements is [`mcp-server/docs/architecture.md`](https://github.com/mc-agents/mcp-server/blob/main/docs/architecture.md),
section *3. operator*. The wire contract the bots speak is
[`docs/bot-protocol.md`](https://github.com/mc-agents/mcp-server/blob/main/docs/bot-protocol.md).

## What it is not

**The operator is not on the data path.** Bots dial the MCP server; the server listens. Nothing
here proxies a tool call, tracks a pod IP, or knows which game server a bot is playing on — a bot
learns that from a `connect` frame the MCP server sends it after the handshake. The operator's
whole job is that a pod with the right image and the right environment exists, and that it comes
back when it dies.

That is also why the CRD has no game server address in it. Put one in the spec and you would have
two places deciding where a bot plays.

## The API

```yaml
apiVersion: mc-agents.dev/v1alpha1
kind: MinecraftBot
metadata:
  name: scout
spec:
  kind: fabric              # the default, and the only kind
  minecraftVersion: "26.1.2"
  server:
    host: mc-mcp-server.mc-agents.svc
    port: 8765
```

```console
$ kubectl get minecraftbots
NAME       KIND         MC       PHASE     LINK      POD        AGE
scout      fabric       26.1.2   Running   Linked    scout      4m
looker     fabric       26.1.2   Starting  Waiting   looker     20s
```

`PHASE` is about the pod. `LINK` is about the bot: the pod's readiness probe goes green only once
the bot's `hello` has been accepted, so `Linked` means the MCP server has it in its registry.
`Waiting` is a running process that has not been accepted yet — usually the MCP server is down,
which is worth telling apart from a pod that never started. `Lost` is a container that restarted
and has not linked again.

`--priority=1` columns carry `status.lastError` and the resolved image. When a bot image does not
exist, `PHASE` is `Failed` and the error says `ImagePullBackOff`, rather than leaving you to go
read the pod.

### Pools

A pool exists because "scale the bots" is the request, and one CR per bot makes that a shell loop.

```console
$ kubectl scale minecraftbotpool/scouts --replicas=8
$ kubectl get minecraftbotpools
NAME     DESIRED   CURRENT   LINKED   KIND         MC       AGE
scouts   8         8         6        fabric       26.1.2   11m
```

Bots are named by ordinal (`scouts-0`…`scouts-7`) rather than with a random suffix, because agents
address a bot by name: a scale-down that renamed the survivors would break every reference an agent
was holding. Scaling down removes the highest ordinals. Editing `spec.template` rewrites the bots
in place, and each bot then replaces its own pod.

### Images

| kind | image |
| --- | --- |
| `fabric` | `junhyung.cloud/library/bot-fabric:<tag>-mc<minecraftVersion>` |

The tag carries the Minecraft version because a Fabric client is compiled against one version's
mappings. `spec.image.repository` and `spec.image.tag` override it; a repository that already
carries a tag or a digest is used verbatim.

One kind exists. There were two, and `spec.kind` is kept -- with `fabric` as its default and only
value -- so that a second does not have to be reinvented; the same is true of the catalogue and
the bot protocol.


### Client assets

A fabric bot cannot ship the Minecraft client jar inside its image — that is not ours to
redistribute. An initContainer pulls it from the Mojang version manifest into a cache volume:

```yaml
spec:
  assets:
    claimName: mc-assets-26-1-2   # unset falls back to an emptyDir
    prefilled: true               # a Job already filled it; skip the initContainer
    readOnly: true
```

With an emptyDir, every pod re-downloads 200–600MB and start-up is measured in minutes. Fill one
PVC per Minecraft version with a Job and mount it read-only, and pod start is a mount.

### Spawn spacing

Paper refuses a second login from the same address within four seconds. Bot pods each get their own
IP, so this rarely bites in Kubernetes — but a pool going from 0 to 8 creates eight pods in one
sweep, and spacing them costs nothing. `--spawn-interval` (default `4s`) gates pod creation; a bot
held back sits in `Pending` and is requeued for exactly the remaining wait.

### What the bot pod is handed

The names are [`docs/bot-protocol.md`](https://github.com/mc-agents/mcp-server/blob/main/docs/bot-protocol.md)'s,
under *How a bot is told where to dial*. Adding to them is a change to that document first.

| variable | meaning |
| --- | --- |
| `MCP_SERVER_HOST`, `MCP_SERVER_PORT` | where to dial |
| `BOT_NAME` | the name the bot reports in `hello`; `spec.botName`, else the CR name truncated to 16 |
| `BOT_KIND`, `MC_VERSION` | what the bot should claim to be |
| `HEALTH_PORT` | port to serve `/healthz` and `/readyz` on (8080) |
| `BOT_WORK_DIR` | writable scratch (`/work`); the root filesystem is read-only |
| `MC_ASSETS_DIR` | fabric only, `<mountPath>/<minecraftVersion>` |
| `BOT_RENDER`, `BOT_RENDER_WIDTH`, `BOT_RENDER_HEIGHT`, `BOT_FRAME_RATE_LIMIT` | fabric only |
| `POD_NAME`, `POD_NAMESPACE`, `NODE_NAME` | for the bot's own logs |

`/readyz` must not go green until the bot is linked. That is the only signal the operator has about
the link, and `LINK` in `kubectl get` is exactly it.

`enableServiceLinks: false` on every bot pod: a Service named `mcp-server` in the same namespace
would otherwise inject `MCP_SERVER_PORT` and silently override the spec.

## Layout

```
api/v1alpha1/             CRD types; controller-gen generates deepcopy and the chart's CRDs
cmd/                      the binary: flags, the manager, and nothing else
internal/controller/bot/  MinecraftBot -> Pod
internal/controller/pool/ MinecraftBotPool -> MinecraftBots
internal/podspec/         the pod a bot turns into
internal/botimage/        kind + version -> image reference
internal/throttle/        spawn spacing
internal/hash/            spec -> short digest
charts/                   the operator and its CRDs
```

Following Thrust and Furnace: **controller-runtime**, `internal/` for everything that is not the
API types, and stdlib `flag` for configuration. The manager owns the caches, the work queues, the
leader election lease and the event recorder; each reconciler is a `client.Client` plus a
`record.EventRecorder` and a `SetupWithManager(mgr, workers)`.

The pod cache is filtered to pods carrying `app.kubernetes.io/managed-by: mc-agents-operator`, so
the operator never holds a pod it did not create. That filter is also why the bot reconciler keeps
an uncached reader: an `AlreadyExists` on create has to be confirmed against the API server, or a
pod created moments ago would read as someone else's and land in `status.lastError`.

## Developing

```console
make build            # compile
make test             # unit tests, race detector
make generate         # deepcopy + CRDs, after editing api/v1alpha1
make check            # what CI runs
```

`make generate` must be run after any change to `api/v1alpha1`; CI fails when the generated files
and the types disagree.

## Verifying against a real cluster

The reconcile loop cannot be proven by unit tests alone, so there is a dedicated k3d cluster.

```console
make verify           # k3d-up + k3d-deploy + k3d-verify
make k3d-down         # throw it away
```

`make verify` creates the cluster `mc-agents`, builds and imports the operator image, installs the
chart, then applies the examples and asserts: a bot becomes a pod, a missing bot image surfaces in
`status.lastError` as `ImagePullBackOff`, the pool creates three ordinal bots, `kubectl scale` moves
that to five, back to one and then to zero, the survivor is `scouts-0`, `status.replicas` is still
reported at zero, and deleting a bot CR collects its pod.

**The cluster is named `mc-agents` and nothing else.** `hyperfarm-local` is shared between sessions
and has already lost work to a concurrent deploy; the Makefile refuses to run against it.

## Releasing

`VERSION` is the single source of truth; `Chart.yaml`'s `version` and `appVersion` must match it,
and CI checks both that they agree and that `VERSION` went up. A push to `main` publishes
`junhyung.cloud/library/operator:<version>-<stamp>.g<sha>` and the chart to
`junhyung.cloud/library/charts`, both signed with keyless cosign. A Harbor robot account pushes
them, from the repository secrets `REGISTRY_USERNAME` and `REGISTRY_PASSWORD`. Pulling needs
neither: the `library` project allows anonymous pull.

## Known limits

- The bot port carries no authentication, by the decision in `docs/architecture.md`: the port never
  leaves the cluster and a NetworkPolicy is the gate. Rotating a token per bot would put secret
  rotation in the operator for a port nothing outside can reach.
- `LINK` is inferred from pod readiness. The operator never talks to the MCP server, so a bot that
  reports ready while lying about its link would be believed.
- `Lost` is a heuristic: a running container that restarted and is not ready again. There is no
  history to distinguish it from a bot that was never linked.
- The asset fetcher is `junhyung.cloud/library/mc-assets`, published from `bot-fabric` so that
  the layout it writes and the layout a fabric bot reads come from one copy of one script. Its
  contract is `--version <mc> --dest <dir>`, or the same two as `MC_VERSION` and `MC_ASSETS_DIR`.
- Bot pods are bare pods, not a Deployment or StatefulSet. A drained node deletes the pod and the
  operator makes a new one; the bot's in-game session does not survive that, and nothing here
  pretends otherwise.
