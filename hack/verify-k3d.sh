#!/usr/bin/env bash
set -o errexit -o nounset -o pipefail

CONTEXT="${1:?kube context}"
NAMESPACE="${2:?namespace}"
ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
# Objects only this script has a use for: profiles whose tags are labels rather than images, a
# bot with nowhere to dial, an MCPServer on the generated-token path. examples/ is for people.
FIXTURES="${ROOT}/hack/testdata"

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

echo "== applying examples"
k apply -f "${ROOT}/examples/minecraftbot.yaml"
k apply -f "${ROOT}/examples/minecraftbotpool.yaml"

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

echo "== deleting the MCPServer takes its objects"
k delete mcpserver mc-agents --wait=true
await "deployment/mc-agents-mcp-server is gone" 60 \
	bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get deployment mc-agents-mcp-server"

echo "== cleaning up"
k delete minecraftbotpool scouts --wait=true

echo "all checks passed"
