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
  kind: mineflayer          # or fabric
  minecraftVersion: "26.1.2"
  server:
    host: mc-mcp-server.mc-agents.svc
    port: 8765
```

```console
$ kubectl get minecraftbots
NAME       KIND         MC       PHASE     LINK      POD        AGE
scout      mineflayer   26.1.2   Running   Linked    scout      4m
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
scouts   8         8         6        mineflayer   26.1.2   11m
```

Bots are named by ordinal (`scouts-0`…`scouts-7`) rather than with a random suffix, because agents
address a bot by name: a scale-down that renamed the survivors would break every reference an agent
was holding. Scaling down removes the highest ordinals. Editing `spec.template` rewrites the bots
in place, and each bot then replaces its own pod.

### Images

| kind | image |
| --- | --- |
| `mineflayer` | `ghcr.io/mc-agents/bot-mineflayer:<tag>` |
| `fabric` | `ghcr.io/mc-agents/bot-fabric:<tag>-mc<minecraftVersion>` |

The fabric tag carries the Minecraft version because a Fabric client is compiled against one
version's mappings. A mineflayer bot negotiates the protocol at runtime, so its image is not
version-specific. `spec.image.repository` and `spec.image.tag` override either; a repository that
already carries a tag or a digest is used verbatim.

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

The operator and the bot images agree on this environment. Adding to it is an API change.

| variable | meaning |
| --- | --- |
| `MCP_HOST`, `MCP_PORT` | where to dial |
| `BOT_NAME` | the name the bot reports in `hello`; `spec.botName`, else the CR name truncated to 16 |
| `BOT_KIND`, `MC_VERSION` | what the bot should claim to be |
| `BOT_HEALTH_PORT` | port to serve `/healthz` and `/readyz` on (8080) |
| `BOT_WORK_DIR` | writable scratch (`/work`); the root filesystem is read-only |
| `MC_ASSETS_DIR` | fabric only, `<mountPath>/<minecraftVersion>` |
| `BOT_RENDER`, `BOT_RENDER_WIDTH`, `BOT_RENDER_HEIGHT`, `BOT_FRAME_RATE_LIMIT` | fabric only |
| `POD_NAME`, `POD_NAMESPACE`, `NODE_NAME` | for the bot's own logs |

`/readyz` must not go green until the bot is linked. That is the only signal the operator has about
the link, and `LINK` in `kubectl get` is exactly it.

`enableServiceLinks: false` on every bot pod: a Service named `mcp` in the same namespace would
otherwise inject `MCP_PORT` and silently override the spec.

## Layout

```
api/v1alpha1/        CRD types; controller-gen generates deepcopy and the chart's CRDs
cmd/                 the binary; flags only
pkg/apiclient/       typed REST client + informer + lister for the two CRDs
pkg/queue/           informer-fed workqueue and the Reconciler interface
pkg/controller/bot/  MinecraftBot -> Pod
pkg/controller/pool/ MinecraftBotPool -> MinecraftBots
pkg/podspec/         the pod a bot turns into
pkg/botimage/        kind + version -> image reference
pkg/throttle/        spawn spacing
charts/              the operator and its CRDs
```

Following Thrust and Furnace: **no controller-runtime.** The informers, work queues, leader
election and event recorder are wired from `client-go` directly, configuration is stdlib `flag`,
and the packages sit under `pkg/`. `pkg/queue` is the whole of what controller-runtime would have
provided here — an informer feeding a rate-limited queue whose worker calls `Reconcile`.

The typed client is hand-written on `rest.RESTClient` with generics rather than generated by
`code-generator`: two resources do not justify a generated clientset, and the generator would have
forced the `api/<group>/<version>` layout on us.

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
`ghcr.io/mc-agents/operator:<version>-<stamp>.g<sha>` and the chart to
`ghcr.io/mc-agents/charts`, both signed with keyless cosign. `GITHUB_TOKEN` is the only credential,
so there is no registry secret to rotate.

## Known limits

- The bot port carries no authentication, by the decision in `docs/architecture.md`: the port never
  leaves the cluster and a NetworkPolicy is the gate. Rotating a token per bot would put secret
  rotation in the operator for a port nothing outside can reach.
- `LINK` is inferred from pod readiness. The operator never talks to the MCP server, so a bot that
  reports ready while lying about its link would be believed.
- `Lost` is a heuristic: a running container that restarted and is not ready again. There is no
  history to distinguish it from a bot that was never linked.
- Bot pods are bare pods, not a Deployment or StatefulSet. A drained node deletes the pod and the
  operator makes a new one; the bot's in-game session does not survive that, and nothing here
  pretends otherwise.
