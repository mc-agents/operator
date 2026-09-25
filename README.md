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

## Installing

The chart is an OCI artifact, so Helm 3.8 or later. It carries the CRDs as release resources and
upgrades them with the operator.

```console
helm install mc-agents-operator oci://junhyung.cloud/mc-agents/charts/mc-agents-operator \
  --version <v> --namespace mc-agents-system --create-namespace
```

Start an MCP server in a namespace of your own:

```console
kubectl create namespace game
kubectl apply -n game -f - <<'EOF'
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MCPServer
metadata:
  name: mc-agents
spec: {}
EOF

kubectl get mcpservers -n game -w
```

Its bearer token is in the Secret named by `status.tokenSecretRef`, and join-server creates bots in
the same namespace. Set image tags for that namespace's bots with a `MinecraftBotProfile` named
`default`, or for the whole cluster with a `ClusterMinecraftBotProfile` named `default`.

```console
kubectl get minecraftbots -n game -w
```

Each release is verified against the k3s that k3d 5.9 creates, currently v1.35; nothing here
needs an API newer than a cluster from the last few years has. The MCP server and bot releases it
was verified with are in [`docs/compatibility.md`](docs/compatibility.md), one row per operator
release, with the order the four ship in.

## Where things live

The operator and its CRDs live in `mc-agents-system`. Nothing else does. A namespace that wants bots
asks for an MCP server there, and everything that server does stays there:

```
mc-agents-system          the operator
game (a tenant)           MCPServer mc-agents
                            -> Deployment, two Services, NetworkPolicy, ServiceAccount, Role, RoleBinding, two token Secrets
                          MinecraftBotProfile default        (optional)
                          MinecraftBot scout-1               created by join-server
                            -> Pod
cluster                   ClusterMinecraftBotProfile default (optional)
```

This is the cert-manager and Strimzi shape. Two tenants get two servers with two tokens and never
see each other's bots, and a namespace admin can do all of it through the built-in `admin` and
`edit` roles, which the chart aggregates the mc-agents resources into.

Those two aggregated ClusterRoles carry the release's fullname, so under the install above they are
`mc-agents-operator-edit` and `mc-agents-operator-view`, not anything named after the API group.
`-edit` carries `rbac.authorization.k8s.io/aggregate-to-admin` and `aggregate-to-edit`, and grants
create, update, patch, delete and deletecollection on `mcpservers`, `minecraftbots`,
`minecraftbotpools`, `minecraftbotpools/scale` and `minecraftbotprofiles`; `-view` carries
`aggregate-to-view` and grants get, list and watch on the same set without the scale subresource.
Nothing binds either one: the built-in roles pick the rules up, so whoever already holds `admin` or
`edit` in a namespace can run bots there and needs no grant from you. `ClusterMinecraftBotProfile`
is in neither, being cluster-scoped — the tags every tenant falls back to stay with whoever holds
the cluster. `rbac.create=false` drops both, along with the operator's own rules.

The operator watches every namespace, and the chart's `watchNamespaces` narrows it to a list;
empty, the default, is all of them. What narrows is the informers and nothing else — the
ClusterRole stays cluster-wide, so widening a release again is a change to the value rather than to
its permissions. The list names the tenants' namespaces; `mc-agents-system` holds the operator and
no MCPServer or bot of its own.

Leaving a tenant namespace out of that list is quiet. Nothing watches it, so an MCPServer or a
MinecraftBot created there is never reconciled: no pod, no status, no events, no error anywhere.
The object sits exactly as it was applied, which reads as a deploy taking its time rather than as a
namespace nobody is listening to. The operator logs the list it started with, `namespaces=all` when
it is empty, and that is the thing to read before going looking anywhere else.

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
  logFormat: ecs                    # optional; ecs, logstash or gelf for a log pipeline
```

```console
$ kubectl get mcpservers -n game
NAME        PHASE     READY   ENDPOINT                                            AGE
mc-agents   Running   True    http://mc-agents-mcp-server.game.svc:3000/mcp       2m
```

Everything the operator makes is named `<name>-mcp-server` and owned by the MCPServer, so deleting
it takes them all. The generated token is created only when it is missing and never rewritten: an
agent already holding it keeps working across every reconcile. `status.tokenSecretRef` names the
Secret either way. join-server creates bots in the MCPServer's namespace and nowhere else, which is
also all its Role allows.

The MCP port is on the Service `<name>-mcp-server`, of whatever `spec.service.type` asks for: the
bearer token is that port's gate. The bot-link port is on `<name>-mcp-server-bots`, always a
ClusterIP, behind a NetworkPolicy that admits only pods of the MCPServer's own namespace that carry
`app.kubernetes.io/name: minecraft-bot`; the MCP port's rule stays open, since the bearer token
gates it. That port never leaves the cluster. The kubelet's probes bypass the policy on
kube-router, Calico and Cilium alike, so the readiness probe needs no rule. `READY` is `False`
with the kubelet's own reason, `ImagePullBackOff` say, when the server pod cannot start, rather
than waiting for the Deployment to call it stalled.

`PHASE` says the same thing in the vocabulary a bot uses: `Running` is the moment `READY` turns
`True`, `Starting` is the Deployment's pod on its way up, `Pending` is no Deployment in view yet,
and `Failed` is a pod the kubelet will not start, a rollout that has given up, or a token or an
apply the operator could not make. There is no `Terminating`: a deleted MCPServer takes everything
it owns with it.

Behind the policy is a second token, so a pod that gets past it still cannot pass itself off as a
bot. The operator makes `<name>-mcp-server-link` once, as it does the auth token, hands it to the
server as `BOT_LINK_TOKEN`, and tells the server its name as `MCP_BOTS_LINK_SECRET`: join-server
writes it into every bot it creates as `spec.linkTokenSecretRef`, the pod gets it as
`BOT_LINK_TOKEN`, and a `hello` without it is refused. A bot declared by hand names the same Secret
itself, as [`examples/minecraftbot-azalea.yaml`](examples/minecraftbot-azalea.yaml) does; one that
names none links only to a server that has no token configured, which is what lets the server, the
bots and the operator ship one at a time. The two Secrets stay two: an agent holds the auth token,
and a bot that could read it would hold the MCP port too.

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

Layer 4 is the operator's flags, which the chart fills from `bots.registry`, `bots.fabricTag`,
`bots.azaleaTag` and `bots.assetFetcherImage`: the releases this operator release was verified
against, under everything. Layer 3 is where a cluster pins its own. `defaultProfile.create` renders
the `ClusterMinecraftBotProfile` named `default` from `defaultProfile.spec`, which is a
`MinecraftBotProfile` spec, so the tags a whole cluster's bots fall back to live in the values file
with the rest of the install rather than in a manifest applied beside it. Turn it on in a later
`helm upgrade` and not on the first install: Helm validates every rendered object before it creates
any, and on a first install the CRD this one needs ships in the same release and does not exist yet.

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
    host: mc-agents-mcp-server-bots.game.svc
    port: 8765
  linkTokenSecretRef:         # the MCPServer's <name>-mcp-server-link; key defaults to token
    name: mc-agents-mcp-server-link
```

join-server writes these; writing one by hand is for a bot outside any MCPServer, or one you want
to outlive a session, since leave-server gives back only the bots join-server asked for.

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

### What each state means

| `PHASE` / `LINK` | `status.lastError` | what happened | what the operator does next | what to check | event |
| --- | --- | --- | --- | --- | --- |
| `Pending` / `Unknown` | empty | waiting its turn under `--spawn-interval` | requeues for the remaining wait, then creates the pod | nothing | `Throttled`, once per wait |
| `Pending` / `Unknown` | `waiting for the previous pod to terminate` | the bot was deleted and asked for again under one name while the old pod exits | deletes the old pod if the collector has not, requeues every 2s | a pod held by a finalizer | none |
| `Starting` / `Unknown` | empty | the pod exists and its containers are not running yet | waits for the kubelet | `kubectl describe pod`; a fabric bot fetching assets into an emptyDir takes minutes | `Spawned` |
| `Starting` / `Unknown` | `Unschedulable: ...` | nothing will schedule the pod: 2Gi for a fabric bot on a small node, a claim that does not exist | waits | node capacity, `spec.assets.claimName` | none |
| `Running` / `Waiting` | empty | the process is up and its `hello` has not been accepted | waits | the MCPServer is `Ready`, `spec.server` names its bots Service | none |
| `Running` / `Linked` | empty | the MCP server has the bot in its registry | nothing | | `Linked` |
| `Running` / `Lost` | empty | the container restarted and has not linked again | waits; the kubelet restarts the container | the bot's previous log | `LinkLost` |
| `Failed` / any | `ImagePullBackOff: ...`, `ErrImagePull`, `InvalidImageName`, `CreateContainerConfigError`, `CreateContainerError` | the kubelet cannot start a container | nothing; the pod stays for `kubectl describe` until the spec or a profile changes | the tag, the registry, pull secrets, `status.image` | `PodFailed` |
| `Failed` / any | `CrashLoopBackOff: ...; <container> last exited with code N` | the container exited three times (`crashLoopRestarts`); one or two are a restart the kubelet is already making | nothing; the kubelet keeps backing off | the exit message, which carries the tail of the log | `PodFailed` |
| `Failed` / `Unknown` | `MinecraftBotProfile "x" not found`, `resolve image: ...` | the spec names something that does not exist | nothing until the spec or the profile changes; no pod is made | `spec.profileRef`, `status.profiles` | `InvalidSpec` |
| `Failed` / `Unknown` | `pod <name> already exists and is not owned by this bot` | a pod of this name that no MinecraftBot of this name owns | nothing; the pod is not touched | who made that pod | `AdoptionRefused` |
| `Terminating` / `Unknown` | | the MinecraftBot is being deleted | the garbage collector takes the pod | | none |

A pod that exits, `Succeeded` or `Failed`, is not a state a bot stays in: the operator deletes it,
records what it said, and makes another one second later, so the bot goes back to `Starting`. A
pod that no longer matches the spec, because the spec or a profile changed, is replaced the same
way.

| `kubectl describe minecraftbot` shows | meaning |
| --- | --- |
| `Recreating` | the pod no longer matches the spec |
| `PodExited` | the pod exited on its own, with the exit message |
| `PodFailed` | the phase turned `Failed` with a pod, with `status.lastError` |

The `Ready` condition is `True` only under `Linked`; otherwise its reason is `WaitingForLink`,
`PodPending`, `Terminating`, or the same cause `Degraded` gives. `Degraded` is `True` while the
phase is `Failed`, and its reason names the cause: the kubelet's reason for a blocked container,
`PodExited`, `InvalidSpec`, `PodNotOwned`. An MCPServer records `TokenCreated` and `ApplyFailed`;
a pool `ScaledUp`, `ScaledDown`, `TemplateUpdated` and `NameClash`, the last when an ordinal's
name is held by a bot that is not the pool's, which `status.lastError` then carries.

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
| `fabric` | `junhyung.cloud/mc-agents/bot-fabric:<tag>-mc<minecraftVersion>` |
| `azalea` | `junhyung.cloud/mc-agents/bot-azalea:<tag>-mc<minecraftVersion>` |

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
| `BOT_LINK_TOKEN` | what to present in `hello`, from the Secret `spec.linkTokenSecretRef` names; absent when it names none |
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
internal/controller/mcpserver/ MCPServer -> Deployment, Services, RBAC, tokens
internal/mcpserverspec/   the objects an MCPServer turns into
internal/podstatus/       what the kubelet says about a pod it cannot start
internal/metrics/         bots by phase and link, MCPServers ready
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

The metrics endpoint carries two gauges beside controller-runtime's, listed from the cache on
every scrape: `mc_agents_bots{namespace,kind,phase,link}` and `mc_agents_mcpserver_ready{namespace}`.
No label names a bot; the port is served without auth. `--zap-log-level=2` shows the operator's
own debug lines, the spawn throttle waits among them; `-v` raises only what client-go says.

## Developing

```console
make build            # compile
make test             # unit tests, race detector
make generate         # deepcopy + CRDs, after editing api/v1alpha1
make check            # what CI runs: version check, vet, tests, chart lint, generated files, golangci-lint
```

`make generate` must be run after any change to `api/v1alpha1`; `make check` regenerates and fails
when the result differs from what is committed. golangci-lint is installed into `bin/` at the
version `hack/install-tools.sh` pins, so a laptop and CI run the same one.

## Verifying against a real cluster

The reconcile loop cannot be proven by unit tests alone, so there is a dedicated k3d cluster.

```console
make verify           # k3d-up + k3d-deploy + k3d-verify
make verify-upgrade   # k3d-up + the previous release + k3d-verify-upgrade
make k3d-down         # throw it away
```

`make verify` creates the cluster `mc-agents`, builds and imports the operator image, installs the
chart, then applies the examples and the fixtures in `hack/testdata` and asserts: a bot becomes a
pod, a missing bot image surfaces in `status.lastError` as `ImagePullBackOff`, the pool creates
three ordinal bots, `kubectl scale` moves that to five, back to one and then to zero, the survivor
is `scouts-0`, `status.replicas` is still reported at zero, deleting a bot CR collects its pod,
and a pod deleted out from under a bot is replaced by one that comes back Running. It then checks
tenancy: a bot takes its image tag from the namespace profile over the cluster profile, an
MCPServer in a tenant namespace gets its Deployment, both Services, NetworkPolicy, Role and one
token that survives a reconcile, turns Ready, gets a new token from a delete and a restart, and
deleting it takes its objects with it. Before any of that, the schema refuses a pool template with
a `botName` and a bot overriding an environment name the operator sets.

Then the loop the whole thing exists for, against the MCP server release `values.yaml` pins: an
azalea bot declared by hand with the server's link Secret reaches `Linked`; through a port-forward
to the MCP Service, `list-bots` names it; `join-server` for a new azalea bot against a host that
does not resolve fails saying the bot linked and the game server refused it, and a MinecraftBot
labelled `mc-agents.junhyung.cloud/requested-by=mcp-server` has appeared; `leave-server` gives it
back and the MinecraftBot is gone.

`hack/testdata` holds what only the script has a use for: profiles whose tags are labels rather
than images, a bot with nowhere to dial. `examples/` is for people, and every image in it exists.

CI runs the same loop on a k3d cluster named `ci` before it publishes anything, so an image or chart
in the registry has reconciled these objects at least once. There the k3d node image comes through
the registry's proxy cache of Docker Hub, since the runners share addresses and Docker Hub
throttles anonymous pulls per address; the Makefile stays on Docker Hub, so a laptop needs no login.

**The cluster is named `mc-agents` and nothing else.** `hyperfarm-local` is shared between sessions
and has already lost work to a concurrent deploy; the Makefile refuses to run against it.

### Upgrading from the previous release

```console
make verify-upgrade                    # from the release before this one
make verify-upgrade FROM=0.12.0-20260916095130.gdccf990b
make k3d-down
```

On a cluster with no operator yet, `hack/verify-k3d.sh --from <version>` installs the published
chart of that version, applies the fixtures under it, replaces it with the source chart the way
`make k3d-deploy` does, and asserts that the bot, the pool, the MCPServer and its token are the
same objects afterwards, that the CRDs belong to the release, and that the new operator reconciles
what it inherited: the pool is still at three, the bot is Running again, the MCPServer gains its
link Secret and turns Ready. For a release before 0.13 it first takes the adoption step documented
under *Upgrading*, which is what that row is for. CI runs two rows before publishing: the release
before this one, and 0.12, whose chart exists only under its stamped tag.

## Releasing

`VERSION` is the single source of truth; `Chart.yaml`'s `version` and `appVersion` must match it,
and CI checks both that they agree and that `VERSION` went up whenever something that ships
changed. A push to `main` that changed something that ships publishes
`junhyung.cloud/mc-agents/operator:<version>-<stamp>.g<sha>`, the same image as `:<version>`, and
the chart to `junhyung.cloud/mc-agents/charts` under `<version>` with `<version>` as its
`appVersion`, so `helm install --version <version>` runs the operator of that version. All of it is
signed with keyless cosign. A Harbor robot account pushes them, from the repository secrets
`REGISTRY_USERNAME` and `REGISTRY_PASSWORD`. Pulling needs neither: the `mc-agents` project allows
anonymous pull. The same push tags the commit `v<version>` and makes a GitHub release of it, with
the commits since the previous tag and the set it was verified with as the notes; a tag that is
already there is left where it is. A push that changed only documentation publishes and tags
nothing: `hack/check-version.sh` says so, and `:<version>` keeps pointing at the build that was
verified under it.

A release also states what it was verified against: `mcpServer.tag`, `bots.fabricTag`,
`bots.azaleaTag` and the `mc-assets` tag in `values.yaml` are the MCP server and bot releases
`make verify` ran with, and move when a release is verified against newer ones. Each release adds
its row to [`docs/compatibility.md`](docs/compatibility.md).

## Upgrading

### from 0.13

0.14 moves every artifact from the registry's `library` project to one of its own, `mc-agents`:
the chart is `oci://junhyung.cloud/mc-agents/charts/mc-agents-operator`, and the operator, MCP
server and bot images are under `junhyung.cloud/mc-agents/`. The earlier releases stay where they
were rather than being copied, so `helm upgrade` names the new location and nothing else changes;
the chart's defaults for the image registries move with it, and a values file that pinned
`image.registry` or `bots.registry` to `junhyung.cloud/library` keeps pulling the old builds until
it is changed.

0.14 also hands every `MCPServer` a link token: a `<name>-mcp-server-link` Secret, the server
refuses a bot that does not present it, and the bots `join-server` starts are pointed at the
Secret. A `MinecraftBot` you declared by hand is not, and stops linking until its spec carries
`linkTokenSecretRef: {name: <name>-mcp-server-link}`; a pool's template takes the same field.

### from 0.12

0.13 renders the CRDs from the chart's templates instead of Helm's `crds/` directory, so a schema
change reaches an existing install with `helm upgrade` rather than a `helm show crds | kubectl
apply` by hand before every release. Helm refuses to take over an object it did not create until
that object says it belongs to the release, so once, before the first upgrade to 0.13, mark the
five CRDs as the release's:

```console
for crd in clusterminecraftbotprofiles mcpservers minecraftbotpools minecraftbotprofiles minecraftbots; do
  kubectl label crd "$crd.mc-agents.junhyung.cloud" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd.mc-agents.junhyung.cloud" \
    meta.helm.sh/release-name=mc-agents-operator \
    meta.helm.sh/release-namespace=mc-agents-system --overwrite
done
```

With Helm 4, which applies server-side, the first upgrade also needs `--force-conflicts`: the
schema's field manager is still `kubectl` from the hand-applied CRDs, and the upgrade is what takes
it over. Helm 3 applies client-side and needs nothing. `make verify-upgrade FROM=<0.12 chart>`
walks exactly this path on a k3d cluster, and CI does before every release.

The release name and namespace are those of your install. Skipping this fails the upgrade with
`invalid ownership metadata` and changes nothing. The CRDs carry `helm.sh/resource-policy: keep`, so an
uninstall leaves them and every MCPServer and bot in place; `crds.keep=false` drops that.

### from 0.10

0.11 moved the API group from `mc-agents.dev` to `mc-agents.junhyung.cloud`. A group is part of a
CRD's name, so these are new CRDs and not a new version of the old ones: upgrade, which installs
the new ones, move what you had, then delete the old CRDs, which deletes the old bots and their
pods with them.

```console
kubectl delete crd minecraftbots.mc-agents.dev minecraftbotpools.mc-agents.dev
```

The MCP server has to move with it: 0.55 creates bots in the new group, and a server from before
creates them in a group nothing watches.

## Known limits

- The bot port has two gates and no more: the NetworkPolicy, which is only as good as the
  cluster's network plugin, and the link token, which is one per MCPServer rather than one per
  bot. A bot that can read the Secret can link as any name; a token per bot would put secret
  rotation in the operator for a port nothing outside the namespace can reach.
- `LINK` is inferred from pod readiness. The operator never talks to the MCP server, so a bot that
  reports ready while lying about its link would be believed.
- `Lost` is a heuristic: a running container that restarted and is not ready again. There is no
  history to distinguish it from a bot that was never linked.
- The asset fetcher is `junhyung.cloud/mc-agents/mc-assets`, published from `bot-fabric` so that
  the layout it writes and the layout a fabric bot reads come from one copy of one script. Its
  contract is `--version <mc> --dest <dir>`, or the same two as `MC_VERSION` and `MC_ASSETS_DIR`.
- Secrets are not watched, so rotating a token is two commands. For the generated one,
  `kubectl -n <ns> delete secret <name>-mcp-server-auth` and then
  `kubectl -n <ns> rollout restart deployment/<name>-mcp-server`: the restart is what enqueues the
  MCPServer, through the Deployment it owns, and the reconcile finds the Secret missing and makes
  a new one. Agents read the new value from the Secret `status.tokenSecretRef` names, which has
  not changed. For an `existingSecret`, rotate the Secret yourself and run only the restart. The
  link token rotates the same way, on `<name>-mcp-server-link`, and takes every bot with it: a
  running pod holds the value it started with, so delete the bots' pods after the restart and
  the operator makes new ones that read the new token.
- Bot pods are bare pods, not a Deployment or StatefulSet. A drained node deletes the pod and the
  operator makes a new one; the bot's in-game session does not survive that, and nothing here
  pretends otherwise.

Licensed under the Apache License 2.0; see LICENSE.
