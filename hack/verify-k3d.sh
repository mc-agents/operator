#!/usr/bin/env bash
# verify-k3d.sh [--from <version>] <kube context> <tenant namespace>
#
# Without --from: the operator is installed, and every fixture is driven through what the operator
# promises about it. With --from: the published release of that version is installed first, the
# fixtures are applied under it, the source chart replaces it, and the objects have to survive and
# reconcile; the cluster must hold no operator yet.
set -o errexit -o nounset -o pipefail

FROM=""
while [[ $# -gt 0 ]]; do
	case "$1" in
	--from)
		FROM="${2:?--from needs a version}"
		shift 2
		;;
	--from=*)
		FROM="${1#--from=}"
		shift
		;;
	-*)
		echo "unknown option $1" >&2
		exit 2
		;;
	*) break ;;
	esac
done
CONTEXT="${1:?kube context}"
NAMESPACE="${2:?namespace}"
ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
# Objects only this script has a use for: profiles whose tags are labels rather than images, a
# bot with nowhere to dial, an MCPServer on the generated-token path. examples/ is for people.
FIXTURES="${ROOT}/hack/testdata"
# Where the operator itself runs; the tenant namespace above is where its objects go.
OPERATOR_NAMESPACE="${OPERATOR_NAMESPACE:-mc-agents-system}"
CRDS=(clusterminecraftbotprofiles mcpservers minecraftbotpools minecraftbotprofiles minecraftbots)

# Where a published chart is. Releases before 0.14.0 went to the registry's library project; from
# 0.14.0 every mc-agents artifact is under a project of its own, and the old ones were left where
# they are rather than copied, so an upgrade from one of them starts there.
chart_repo() {
	if [[ "$(printf '%s\n%s\n' "$1" 0.14.0 | sort -V | head -1)" != 0.14.0 ]]; then
		echo oci://junhyung.cloud/library/charts/mc-agents-operator
	else
		echo oci://junhyung.cloud/mc-agents/charts/mc-agents-operator
	fi
}

k() { kubectl --context "${CONTEXT}" -n "${NAMESPACE}" "$@"; }

await() {
	local what="$1" timeout="$2"
	shift 2
	local deadline=$((SECONDS + timeout))
	until "$@" >/dev/null 2>&1; do
		if ((SECONDS >= deadline)); then
			echo "timed out after ${timeout}s waiting for: ${what}" >&2
			return 1
		fi
		sleep 2
	done
	echo "ok: ${what}"
}

field() { k get "$1" "$2" -o jsonpath="{$3}" 2>/dev/null; }

equals() { [[ "$(field "$1" "$2" "$3")" == "$4" ]]; }

contains() { [[ "$(field "$1" "$2" "$3")" == *"$4"* ]]; }

# await Running, and fail at once should Failed show up on the way there.
await_running() {
	local name="$1" timeout="$2"
	local deadline=$((SECONDS + timeout))
	until equals minecraftbot "${name}" .status.phase Running; do
		if equals minecraftbot "${name}" .status.phase Failed; then
			echo "minecraftbot/${name} is Failed: $(field minecraftbot "${name}" .status.lastError)" >&2
			return 1
		fi
		if ((SECONDS >= deadline)); then
			echo "timed out after ${timeout}s waiting for minecraftbot/${name} to be Running" >&2
			return 1
		fi
		sleep 1
	done
	echo "ok: minecraftbot/${name} is Running and was never Failed"
}

count_is() { [[ "$(k get minecraftbots -l "mc-agents.junhyung.cloud/pool=$1" --no-headers 2>/dev/null | wc -l | tr -d ' ')" == "$2" ]]; }

# The API server has to refuse the manifest, and for the reason the CRD's rule gives; a server
# dry run keeps an accepted one from landing in the namespace.
refused() {
	local what="$1" message="$2" manifest="$3" out
	if out="$(printf '%s\n' "${manifest}" | k apply --dry-run=server -f - 2>&1)"; then
		echo "${what} was accepted: ${out}" >&2
		return 1
	fi
	if [[ "${out}" != *"${message}"* ]]; then
		echo "${what} was refused for another reason: ${out}" >&2
		return 1
	fi
	echo "ok: ${what} is refused: ${message}"
}

# The MCP endpoint over Streamable HTTP, through a port-forward from this machine rather than a
# curl pod: the runner has curl and python3, and a pod would be one more image to pull. The local
# port is whatever is free: a developer's machine already forwards a server of its own on the
# obvious number.
MCP_LOCAL_PORT="${MCP_LOCAL_PORT:-$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')}"
MCP_BASE="http://127.0.0.1:${MCP_LOCAL_PORT}/mcp"
mcp_token=""
mcp_session=""

mcp_post() {
	curl -sS -X POST "${MCP_BASE}" \
		-H "Authorization: Bearer ${mcp_token}" \
		-H 'Content-Type: application/json' \
		-H 'Accept: application/json, text/event-stream' \
		${mcp_session:+-H "mcp-session-id: ${mcp_session}"} \
		-d "$1"
}

mcp_open() {
	mcp_token="$(k get secret mc-agents-mcp-server-auth -o jsonpath='{.data.token}' | base64 -d)"
	mcp_session="$(curl -sS -i -X POST "${MCP_BASE}" \
		-H "Authorization: Bearer ${mcp_token}" \
		-H 'Content-Type: application/json' \
		-H 'Accept: application/json, text/event-stream' \
		-d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"verify-k3d","version":"0"}}}' |
		tr -d '\r' | grep -i '^mcp-session-id:' | head -1 | sed 's/^[^:]*: *//' || true)"
	if [[ -z "${mcp_session}" ]]; then
		echo "initialize returned no session id" >&2
		return 1
	fi
	mcp_post '{"jsonrpc":"2.0","method":"notifications/initialized"}' >/dev/null
	echo "ok: an MCP session is open with the token from the Secret"
}

# mcp_call <tool> <json arguments>: prints the text the tool answered with. Exit 1 when the tool
# said it failed, 2 when the call itself did; a caller that expects a failure tests the text.
mcp_call() {
	mcp_post "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$1\",\"arguments\":$2}}" |
		python3 -c '
import json, sys
raw = sys.stdin.read()
# The answer comes as one JSON body or as SSE events; a progress notification may precede the result.
events = [line[5:] for line in raw.splitlines() if line.startswith("data:")] or [raw]
answer = next((json.loads(e) for e in events if json.loads(e).get("id") == 2), None)
if answer is None or "result" not in answer:
    print("rpc: " + json.dumps(answer if answer is not None else raw)[:500])
    sys.exit(2)
result = answer["result"]
print("".join(c.get("text", "") for c in result.get("content", [])))
sys.exit(1 if result.get("isError") else 0)
'
}

requested_by_server() { k get minecraftbots -l mc-agents.junhyung.cloud/requested-by=mcp-server --no-headers 2>/dev/null; }

crd_is_helms() { [[ "$(kubectl --context "${CONTEXT}" get crd "$1.mc-agents.junhyung.cloud" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')" == Helm ]]; }

# Install the published release FROM, put the fixtures under it, replace it with the source chart
# and assert nothing was lost on the way. The full loop below runs on a cluster that already has
# the source chart; this one has to start from the release.
verify_upgrade() {
	local helm=(helm --kube-context "${CONTEXT}")

	echo "== installing the published release ${FROM}"
	if "${helm[@]}" status mc-agents-operator -n "${OPERATOR_NAMESPACE}" >/dev/null 2>&1; then
		echo "release mc-agents-operator already exists in ${OPERATOR_NAMESPACE}; the upgrade has to start from ${FROM} (make k3d-down first)" >&2
		return 1
	fi
	"${helm[@]}" install mc-agents-operator "$(chart_repo "${FROM}")" --version "${FROM}" \
		--namespace "${OPERATOR_NAMESPACE}" --create-namespace --wait

	echo "== the release reconciles the fixtures"
	# Not the profiles: their tags are labels, not images, and a bot built from one cannot run.
	k apply -f "${FIXTURES}/minecraftbot.yaml"
	k apply -f "${FIXTURES}/minecraftbotpool.yaml"
	k apply -f "${FIXTURES}/mcpserver.yaml"
	await "the pool has three bots" 60 count_is scouts 3
	await_running scout 180
	await "deployment/mc-agents-mcp-server exists" 60 k get deployment mc-agents-mcp-server
	await "the token secret exists" 30 k get secret mc-agents-mcp-server-auth
	local bot_uid pool_uid server_uid token_uid pod_uid
	bot_uid="$(field minecraftbot scout .metadata.uid)"
	pool_uid="$(field minecraftbotpool scouts .metadata.uid)"
	server_uid="$(field mcpserver mc-agents .metadata.uid)"
	token_uid="$(field secret mc-agents-mcp-server-auth .metadata.uid)"
	pod_uid="$(field pod scout .metadata.uid)"

	# Before 0.13 the CRDs came from the chart's crds/ directory, which Helm installs once and never
	# owns. 0.13 renders them from templates, and Helm refuses an object that does not say it belongs
	# to the release; this is the step the README documents under "Upgrading from 0.12".
	local crd
	if [[ "$(printf '%s\n%s\n' "${FROM}" 0.13.0 | sort -V | head -1)" != 0.13.0 ]]; then
		echo "== adopting the CRDs the release ${FROM} left outside the release"
		for crd in "${CRDS[@]}"; do
			kubectl --context "${CONTEXT}" label crd "${crd}.mc-agents.junhyung.cloud" app.kubernetes.io/managed-by=Helm --overwrite
			kubectl --context "${CONTEXT}" annotate crd "${crd}.mc-agents.junhyung.cloud" \
				meta.helm.sh/release-name=mc-agents-operator \
				meta.helm.sh/release-namespace="${OPERATOR_NAMESPACE}" --overwrite
		done
	fi

	echo "== upgrading to the source chart"
	bash "${ROOT}/hack/k3d-install.sh" "${CONTEXT}" "${OPERATOR_NAMESPACE}"

	echo "== the objects survived"
	for crd in "${CRDS[@]}"; do
		await "crd/${crd} belongs to the release" 30 crd_is_helms "${crd}"
	done
	equals minecraftbot scout .metadata.uid "${bot_uid}" || { echo "minecraftbot/scout was replaced" >&2; return 1; }
	equals minecraftbotpool scouts .metadata.uid "${pool_uid}" || { echo "minecraftbotpool/scouts was replaced" >&2; return 1; }
	equals mcpserver mc-agents .metadata.uid "${server_uid}" || { echo "mcpserver/mc-agents was replaced" >&2; return 1; }
	equals secret mc-agents-mcp-server-auth .metadata.uid "${token_uid}" || { echo "the token secret was replaced; agents holding it are cut off" >&2; return 1; }
	echo "ok: the bot, the pool, the MCPServer and its token are the objects they were"

	echo "== the new operator reconciles them"
	await "the pool still has three bots" 60 count_is scouts 3
	await_running scout 180
	# A pod that still matches the spec is left alone; one that does not is replaced, and either
	# way the bot has to be Running again. What must not happen is a pod gone and nothing after it.
	if [[ "$(field pod scout .metadata.uid)" == "${pod_uid}" ]]; then
		echo "ok: pod/scout was left running through the upgrade"
	else
		echo "ok: pod/scout was replaced by the new operator and is Running again"
	fi
	# What only the new operator does to an MCPServer it inherited.
	await "the link secret exists" 60 k get secret mc-agents-mcp-server-link
	await "the server Deployment carries the link token" 60 \
		contains deployment mc-agents-mcp-server '.spec.template.spec.containers[0].env[*].name' BOT_LINK_TOKEN
	await "mcpserver/mc-agents is Ready" 240 \
		equals mcpserver mc-agents '.status.conditions[?(@.type=="Ready")].status' True

	echo "== cleaning up"
	k delete mcpserver mc-agents --wait=true
	k delete minecraftbotpool scouts --wait=true
	k delete minecraftbot scout --wait=true
	echo "all upgrade checks passed"
}

if [[ -n "${FROM}" ]]; then
	verify_upgrade
	exit 0
fi

echo "== the schema refuses what the operator could not make work"
refused "a pool template with a botName" "botName is per bot" "$(cat <<'EOF'
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBotPool
metadata:
  name: same-name
spec:
  template:
    spec:
      minecraftVersion: "26.1.2"
      botName: scout
      server:
        host: nowhere.invalid
EOF
)"
refused "a bot overriding the port the operator sets" "env names the operator sets are reserved" "$(cat <<'EOF'
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBot
metadata:
  name: overriding
spec:
  minecraftVersion: "26.1.2"
  server:
    host: nowhere.invalid
  env:
    - name: MCP_SERVER_PORT
      value: "1"
EOF
)"

echo "== applying the fixtures"
k apply -f "${FIXTURES}/minecraftbot.yaml"
k apply -f "${FIXTURES}/minecraftbotpool.yaml"

echo "== a single bot becomes a pod"
await "minecraftbot/scout names its pod" 60 equals minecraftbot scout .status.podName scout
await "pod/scout exists" 60 k get pod scout

echo "== a missing bot image lands in status, not just in the pod"
# A tag no release will ever publish, on an azalea bot: a fabric pod fetches half a gigabyte of
# assets in an init container before the kubelet ever pulls the bot image, and on a runner with
# an empty cache the pull failure came after the wait had run out.
k apply -f - <<EOF
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBot
metadata:
  name: missing
spec:
  kind: azalea
  minecraftVersion: "26.1.2"
  image:
    tag: verify-missing
  server:
    host: nowhere.invalid
EOF
await "minecraftbot/missing reports the pull failure" 180 \
	contains minecraftbot missing .status.lastError ImagePull
await "minecraftbot/missing is Failed" 60 equals minecraftbot missing .status.phase Failed
k delete minecraftbot missing --wait=true

echo "== the pool owns its bots"
await "the pool has three bots" 60 count_is scouts 3
await "pool status counts them" 60 equals minecraftbotpool scouts .status.replicas 3

echo "== kubectl scale drives the pool"
k scale minecraftbotpool/scouts --replicas=5
await "the pool has five bots" 60 count_is scouts 5
k scale minecraftbotpool/scouts --replicas=1
await "the pool has one bot" 60 count_is scouts 1
await "the survivor is the lowest ordinal" 30 k get minecraftbot scouts-0
k scale minecraftbotpool/scouts --replicas=0
await "the pool empties" 60 count_is scouts 0
# An omitempty on the counter would drop it here, blanking the printer column and taking
# .status.replicas away from the scale subresource.
await "status.replicas is still reported at zero" 30 equals minecraftbotpool scouts .status.replicas 0

echo "== deleting the CR collects the pod"
# Running first: a pod deleted while the kubelet is still pulling its image stays Terminating
# until the pull returns, and on a runner meeting a new bot image for the first time that took
# longer than the wait below, which is meant to time the operator and not the registry.
await_running scout 180
k delete minecraftbot scout --wait=true
await "pod/scout is gone" 60 bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get pod scout"

echo "== a bot takes its tag from the namespace profile over the cluster profile"
k apply -f "${FIXTURES}/profiles.yaml"
k apply -f "${FIXTURES}/minecraftbot-azalea.yaml"
await "minecraftbot/tagged runs the namespace profile's tag" 60 \
	contains minecraftbot tagged .status.image bot-azalea:namespace-default-mc26.1.2
await "minecraftbot/tagged names both profiles" 30 \
	equals minecraftbot tagged '.status.profiles[*]' "MinecraftBotProfile/default ClusterMinecraftBotProfile/default"
k delete minecraftbotprofile default --wait=true
await "without it the cluster profile's tag replaces the pod" 90 \
	contains pod tagged '.spec.containers[0].image' bot-azalea:cluster-default-mc26.1.2
k delete minecraftbot tagged --wait=true
kubectl --context "${CONTEXT}" delete clusterminecraftbotprofile default --wait=true

echo "== a bot with no profile left runs the operator's defaults"
await "pod/tagged is gone" 60 bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get pod tagged"
# The operator's default azalea image needs no MCP server to stay up, so the pod tests below can
# expect Running.
k apply -f "${FIXTURES}/minecraftbot-azalea.yaml"
await_running tagged 180

echo "== a pod that dies comes back"
# The whole job, and the one thing the unit tests cannot do: delete the pod out from under the
# bot and expect another one, of another UID, to be Running in its place.
pod_uid="$(field pod tagged .metadata.uid)"
k delete pod tagged --wait=false
replaced() {
	local uid
	uid="$(field pod tagged .metadata.uid)"
	[[ -n "${uid}" && "${uid}" != "${pod_uid}" ]]
}
await "a new pod/tagged replaces the deleted one" 60 replaced
await_running tagged 180

echo "== a bot asked for again by name waits for its old pod instead of calling it a clash"
# leave-server then join-server: the CR goes and comes back while the old pod is still
# terminating. A finalizer holds the pod there, since azalea exits on SIGTERM within a second and
# the window would otherwise close before anything looked at it.
release_hold() { k patch pod tagged --type json -p '[{"op":"remove","path":"/metadata/finalizers"}]'; }
# Under errexit a failed assertion would otherwise leave pod/tagged Terminating in the tenant
# namespace, and the cluster is reused: every later run would then meet it as a predecessor.
trap 'release_hold >/dev/null 2>&1 || true' EXIT
k patch pod tagged --type merge -p '{"metadata":{"finalizers":["mc-agents.junhyung.cloud/verify-hold"]}}'
k delete minecraftbot tagged --wait=false
k apply -f "${FIXTURES}/minecraftbot-azalea.yaml"
await "minecraftbot/tagged waits for the old pod" 30 \
	contains minecraftbot tagged .status.lastError "waiting for the previous pod to terminate"
equals minecraftbot tagged .status.phase Pending || {
	echo "minecraftbot/tagged is $(field minecraftbot tagged .status.phase) while the old pod terminates, want Pending" >&2
	exit 1
}
release_hold
trap - EXIT
await_running tagged 180
k delete minecraftbot tagged --wait=true

echo "== an MCPServer runs in its own namespace"
k apply -f "${FIXTURES}/mcpserver.yaml"
if [[ -n "${MCP_SERVER_TAG:-}" ]]; then
	k patch mcpserver mc-agents --type merge -p "{\"spec\":{\"image\":{\"tag\":\"${MCP_SERVER_TAG}\"}}}"
fi
await "deployment/mc-agents-mcp-server exists" 60 k get deployment mc-agents-mcp-server
await "service/mc-agents-mcp-server carries the MCP port alone" 30 \
	equals service mc-agents-mcp-server '.spec.ports[*].name' mcp
# The bot port has no authentication: it stays on a ClusterIP Service behind a NetworkPolicy
# whatever spec.service.type asks for the MCP port.
await "service/mc-agents-mcp-server-bots carries the bot port" 30 \
	equals service mc-agents-mcp-server-bots '.spec.ports[*].name' bot-link
equals service mc-agents-mcp-server-bots .spec.type ClusterIP || {
	echo "service/mc-agents-mcp-server-bots is $(field service mc-agents-mcp-server-bots .spec.type), want ClusterIP" >&2
	exit 1
}
await "networkpolicy/mc-agents-mcp-server exists" 30 k get networkpolicy mc-agents-mcp-server
await "role/mc-agents-mcp-server exists" 30 k get role mc-agents-mcp-server
await "the token secret exists" 30 k get secret mc-agents-mcp-server-auth
token_uid="$(field secret mc-agents-mcp-server-auth .metadata.uid)"
await "status names the endpoint" 30 \
	equals mcpserver mc-agents .status.endpoint "http://mc-agents-mcp-server.${NAMESPACE}.svc:3000/mcp"
await "mcpserver/mc-agents is Ready" 240 \
	equals mcpserver mc-agents '.status.conditions[?(@.type=="Ready")].status' True
k annotate mcpserver mc-agents verify/touched="$(date +%s)" --overwrite
sleep 5
if [[ "$(field secret mc-agents-mcp-server-auth .metadata.uid)" != "${token_uid}" ]]; then
	echo "the token secret was replaced by a reconcile" >&2
	exit 1
fi
echo "ok: the token survives a reconcile"

echo "== rotating the generated token is delete plus restart"
# Secrets are not watched, so the delete alone changes nothing; the restart is what enqueues the
# MCPServer, through the Deployment it owns.
token_replaced() {
	local uid
	uid="$(field secret mc-agents-mcp-server-auth .metadata.uid)"
	[[ -n "${uid}" && "${uid}" != "${token_uid}" ]]
}
k delete secret mc-agents-mcp-server-auth --wait=true
k rollout restart deployment/mc-agents-mcp-server
await "a new token secret exists" 60 token_replaced
k rollout status deployment/mc-agents-mcp-server --timeout=240s
await "mcpserver/mc-agents is Ready with the new token" 60 \
	equals mcpserver mc-agents '.status.conditions[?(@.type=="Ready")].status' True

echo "== a bot declared by hand links with the server's token"
await "the link secret exists" 30 k get secret mc-agents-mcp-server-link
# The bots Service in this namespace, and the link Secret the server was handed: the pod gets the
# token from it, and the policy admits the pod because it is in the server's own namespace.
k apply -f - <<EOF
apiVersion: mc-agents.junhyung.cloud/v1alpha1
kind: MinecraftBot
metadata:
  name: linked
spec:
  kind: azalea
  minecraftVersion: "26.1.2"
  server:
    host: mc-agents-mcp-server-bots.${NAMESPACE}.svc
  linkTokenSecretRef:
    name: mc-agents-mcp-server-link
EOF
await "minecraftbot/linked is Linked" 180 equals minecraftbot linked .status.link Linked

echo "== the server names it, starts a bot on request and takes that bot back"
k port-forward service/mc-agents-mcp-server "${MCP_LOCAL_PORT}:3000" >/dev/null 2>&1 &
forward_pid=$!
trap 'kill "${forward_pid}" 2>/dev/null || true' EXIT
await "the MCP port is forwarded to :${MCP_LOCAL_PORT}" 30 curl -sf -o /dev/null "http://127.0.0.1:${MCP_LOCAL_PORT}/actuator/health/liveness"
mcp_open
listed="$(mcp_call list-bots '{}')" || {
	echo "list-bots failed: ${listed}" >&2
	exit 1
}
if [[ "${listed}" != *"linked (azalea)"* ]]; then
	echo "list-bots does not name the bot that linked: ${listed}" >&2
	exit 1
fi
echo "ok: list-bots names linked"
# join-server asks the operator for a bot, waits for its hello, and only then sends it to the game
# server. A host that does not resolve fails the second half, so the failure has to say the bot
# linked and the server refused it; one that never linked would say so, and would have been given
# back before this could look at it. A join that says the bot is still starting is not a failure:
# the same call waits for the same bot.
joined=""
for _ in 1 2 3 4 5 6; do
	if joined="$(mcp_call join-server '{"bot":"probe","kind":"azalea","host":"nowhere.invalid","port":25565,"timeoutMs":20000}')"; then
		echo "join-server succeeded against a host that does not resolve: ${joined}" >&2
		exit 1
	fi
	[[ "${joined}" == *"still starting"* ]] || break
	sleep 5
done
if [[ "${joined}" != *"did not accept the connection"* || "${joined}" != *"could not join nowhere.invalid"* ]]; then
	echo "join-server failed for another reason than the game server: ${joined}" >&2
	exit 1
fi
echo "ok: join-server linked the bot and the game server refused it"
if [[ "$(requested_by_server | wc -l | tr -d ' ')" != 1 ]]; then
	echo "want one MinecraftBot labelled requested-by=mcp-server, have: $(requested_by_server)" >&2
	exit 1
fi
echo "ok: a MinecraftBot labelled mc-agents.junhyung.cloud/requested-by=mcp-server appeared"
left="$(mcp_call leave-server '{"bot":"probe"}')" || {
	echo "leave-server failed: ${left}" >&2
	exit 1
}
if [[ "${left}" != *"given back"* ]]; then
	echo "leave-server did not give the bot back: ${left}" >&2
	exit 1
fi
given_back() { [[ -z "$(requested_by_server)" ]]; }
await "the bot join-server asked for is deleted" 60 given_back
kill "${forward_pid}" 2>/dev/null || true
trap - EXIT
k delete minecraftbot linked --wait=true

echo "== deleting the MCPServer takes its objects"
k delete mcpserver mc-agents --wait=true
await "deployment/mc-agents-mcp-server is gone" 60 \
	bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get deployment mc-agents-mcp-server"

echo "== cleaning up"
k delete minecraftbotpool scouts --wait=true

echo "all checks passed"
