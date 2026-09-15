# mc-agents operator

Keeps Minecraft bot pods alive for the [mc-agents MCP server](https://github.com/mc-agents/mcp-server),
and runs that server for whichever namespace asks for one. One `MinecraftBot` is one pod; a
`MinecraftBotPool` is a replica count you can scale; an `MCPServer` is one MCP server with its own
token; `MinecraftBotProfile` and `ClusterMinecraftBotProfile` hold the defaults bots are built from.

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

## Where things live

The operator and its CRDs live in `mc-agents-system`. Nothing else does. A namespace that wants bots
asks for an MCP server there, and everything that server does stays there:

```
mc-agents-system          the operator
game (a tenant)           MCPServer mc-agents
                            -> Deployment, Service, ServiceAccount, Role, RoleBinding, token Secret
                          MinecraftBotProfile default        (optional)
                          MinecraftBot scout-1               created by join-server
                            -> Pod
cluster                   ClusterMinecraftBotProfile default (optional)
```

This is the cert-manager and Strimzi shape. Two tenants get two servers with two tokens and never
see each other's bots, and a namespace admin can do all of it through the built-in `admin` and
`edit` roles, which the chart aggregates the mc-agents resources into.

### MCPServer

```yaml
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MCPServer
metadata:
  name: mc-agents
  namespace: game
spec:
  auth:
    existingSecret: mc-agents-token   # empty: the operator makes <name>-mcp-server-auth once
  bots:
    profileRef:                       # written into every bot this server creates
      name: quest
  maxBots: 16
```

```console
$ kubectl get mcpservers -n game
NAME        READY   ENDPOINT                                            AGE
mc-agents   True    http://mc-agents-mcp-server.game.svc:3000/mcp       2m
```

Everything the operator makes is named `<name>-mcp-server` and owned by the MCPServer, so deleting
it takes them all. The generated token is created only when it is missing and never rewritten: an
agent already holding it keeps working across every reconcile. `status.tokenSecretRef` names the
Secret either way. join-server creates bots in the MCPServer's namespace and nowhere else, which is
also all its Role allows.

An empty `spec.image.tag` runs the operator's `--mcp-server-tag`, the MCP server release this
operator release was verified against.

### Profiles

A bot takes every field its own spec leaves empty from a profile. Nearest wins:

1. the bot's own spec
2. the profile named by `spec.profileRef`, or else the namespace's `MinecraftBotProfile` named `default`
3. the `ClusterMinecraftBotProfile` named `default`
4. the operator's flags (`--bot-registry`, `--fabric-tag`, `--azalea-tag`, `--asset-fetcher-image`)

```yaml
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBotProfile
metadata:
  name: default
  namespace: game
spec:
  fabric:
    tag: 0.64.0
  azalea:
    tag: 0.16.0
    resources:
      limits: {memory: 512Mi}
```

A profile tag still gets `-mc<minecraftVersion>` appended, as the flags do; `spec.image.tag` on a
bot is used as written. Labels and annotations merge key by key, every other field is taken whole.
`status.profiles` lists the profiles a bot was built from. A bot that names a profile which does not
exist is `Failed` rather than quietly built from the defaults.

Changing a profile rebuilds the pods of the bots that use it, as changing a Deployment's template
does.

## The API

```yaml
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBot
metadata:
  name: scout
spec:
  kind: fabric              # the default; azalea for a bot without a client
  minecraftVersion: "26.1.2"
  server:
    host: mc-agents-mcp-server.game.svc
    port: 8765
```

join-server writes these; writing one by hand is for a bot outside any MCPServer.

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
| `azalea` | `junhyung.cloud/library/bot-azalea:<tag>-mc<minecraftVersion>` |

The tag carries the Minecraft version because each kind is built against one version: a Fabric
client against its mappings, azalea against its protocol. The registry and `<tag>` come from the
nearest profile, then the flags. `spec.image.repository` and `spec.image.tag` override both; a
repository that already carries a tag or a digest is used verbatim.

`fabric` is a real client. It renders, so it can take a screenshot and draw a resource pack, and it
needs about 2GiB. `azalea` speaks the protocol with no client at a few MiB, which is what a
scenario with many bots at once wants; the catalogue says which tools each kind answers.


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
internal/controller/mcpserver/ MCPServer -> Deployment, Service, RBAC, token
internal/mcpserverspec/   the objects an MCPServer turns into
internal/profile/         which profiles a bot takes, and folding them into its spec
internal/podspec/         the pod a bot turns into
internal/botimage/        kind + version + profile -> image reference
internal/throttle/        spawn spacing
internal/hash/            spec -> short digest
charts/                   the operator and its CRDs
```

Following Thrust and Furnace: **controller-runtime**, `internal/` for everything that is not the
API types, and stdlib `flag` for configuration. The manager owns the caches, the work queues, the
leader election lease and the event recorder; each reconciler is a `client.Client` plus a
`record.EventRecorder` and a `SetupWithManager(mgr, workers)`.

The caches for pods, Deployments, Services, ServiceAccounts, Roles and RoleBindings are filtered to
objects carrying `app.kubernetes.io/managed-by: mc-agents-operator`, so the operator never holds one
it did not create. Secrets are not cached at all: the MCPServer reconciler reads its own token Secret
by name, uncached, and never lists them. MCPServers are applied server-side under the field owner
`mc-agents-operator`, so a reconcile that changes nothing writes nothing. That filter is also why the bot reconciler keeps
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
reported at zero, and deleting a bot CR collects its pod. It then checks tenancy: a bot takes its
image tag from the namespace profile over the cluster profile, an MCPServer in a tenant namespace
gets its Deployment, Service, Role and one token that survives a reconcile, turns Ready, and
deleting it takes its objects with it.

**The cluster is named `mc-agents` and nothing else.** `hyperfarm-local` is shared between sessions
and has already lost work to a concurrent deploy; the Makefile refuses to run against it.

## Releasing

`VERSION` is the single source of truth; `Chart.yaml`'s `version` and `appVersion` must match it,
and CI checks both that they agree and that `VERSION` went up. A push to `main` publishes
`junhyung.cloud/library/operator:<version>-<stamp>.g<sha>` and the chart to
`junhyung.cloud/library/charts`, both signed with keyless cosign. A Harbor robot account pushes
them, from the repository secrets `REGISTRY_USERNAME` and `REGISTRY_PASSWORD`. Pulling needs
neither: the `library` project allows anonymous pull.

## Upgrading from 0.10

0.11 moved the API group from `mc-agents.dev` to `mc-agents.junhyung.cloud`. A group is part of a
CRD's name, so these are new CRDs and not a new version of the old ones: install the new ones, move
what you had, then delete the old CRDs, which deletes the old bots and their pods with them.

```console
helm show crds oci://junhyung.cloud/library/charts/mc-agents-operator --version <v> \
  | kubectl apply --server-side -f -
kubectl delete crd minecraftbots.mc-agents.dev minecraftbotpools.mc-agents.dev
```

The MCP server has to move with it: 0.55 creates bots in the new group, and a server from before
creates them in a group nothing watches.

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
- Rotating an `existingSecret` does not restart the MCP server. The operator does not read that
  Secret, so it cannot notice the change; `kubectl rollout restart` the Deployment after rotating.
- Bot pods are bare pods, not a Deployment or StatefulSet. A drained node deletes the pod and the
  operator makes a new one; the bot's in-game session does not survive that, and nothing here
  pretends otherwise.
