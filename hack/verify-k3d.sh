#!/usr/bin/env bash
set -o errexit -o nounset -o pipefail

CONTEXT="${1:?kube context}"
NAMESPACE="${2:?namespace}"
ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"

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

echo "== applying examples"
k apply -f "${ROOT}/examples/minecraftbot.yaml"
k apply -f "${ROOT}/examples/minecraftbotpool.yaml"

echo "== a single bot becomes a pod"
await "minecraftbot/scout names its pod" 60 equals minecraftbot scout .status.podName scout
await "pod/scout exists" 60 k get pod scout

echo "== a missing bot image lands in status, not just in the pod"
# A tag no release will ever publish. The default tag is a real image once one has been released.
k patch minecraftbot scout --type merge -p '{"spec":{"image":{"tag":"verify-missing"}}}'
await "minecraftbot/scout reports the pull failure" 180 \
	contains minecraftbot scout .status.lastError ImagePull
await "minecraftbot/scout is Failed" 60 equals minecraftbot scout .status.phase Failed

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
k apply -f "${ROOT}/examples/minecraftbotprofile.yaml"
k apply -f "${ROOT}/examples/minecraftbot-azalea.yaml"
await "minecraftbot/tagged runs the namespace profile's tag" 60 \
	contains minecraftbot tagged .status.image bot-azalea:namespace-default-mc26.1.2
await "minecraftbot/tagged names both profiles" 30 \
	equals minecraftbot tagged '.status.profiles[*]' "MinecraftBotProfile/default ClusterMinecraftBotProfile/default"
k delete minecraftbotprofile default --wait=true
await "without it the cluster profile's tag replaces the pod" 90 \
	contains pod tagged '.spec.containers[0].image' bot-azalea:cluster-default-mc26.1.2
k delete minecraftbot tagged --wait=true
kubectl --context "${CONTEXT}" delete clusterminecraftbotprofile default --wait=true

echo "== a bot asked for again by name waits for its old pod instead of calling it a clash"
await "pod/tagged is gone" 60 bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get pod tagged"
# No profile is left, so the bot runs the operator's default azalea image, which needs no MCP
# server to stay up.
k apply -f "${ROOT}/examples/minecraftbot-azalea.yaml"
await_running tagged 180
# leave-server then join-server: the CR goes and comes back while the old pod is still
# terminating. A finalizer holds the pod there, since azalea exits on SIGTERM within a second and
# the window would otherwise close before anything looked at it.
release_hold() { k patch pod tagged --type json -p '[{"op":"remove","path":"/metadata/finalizers"}]'; }
# Under errexit a failed assertion would otherwise leave pod/tagged Terminating in the tenant
# namespace, and the cluster is reused: every later run would then meet it as a predecessor.
trap 'release_hold >/dev/null 2>&1 || true' EXIT
k patch pod tagged --type merge -p '{"metadata":{"finalizers":["mc-agents.junhyung.cloud/verify-hold"]}}'
k delete minecraftbot tagged --wait=false
k apply -f "${ROOT}/examples/minecraftbot-azalea.yaml"
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
k apply -f "${ROOT}/examples/mcpserver.yaml"
if [[ -n "${MCP_SERVER_TAG:-}" ]]; then
	k patch mcpserver mc-agents --type merge -p "{\"spec\":{\"image\":{\"tag\":\"${MCP_SERVER_TAG}\"}}}"
fi
await "deployment/mc-agents-mcp-server exists" 60 k get deployment mc-agents-mcp-server
await "service/mc-agents-mcp-server exists" 30 k get service mc-agents-mcp-server
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

echo "== deleting the MCPServer takes its objects"
k delete mcpserver mc-agents --wait=true
await "deployment/mc-agents-mcp-server is gone" 60 \
	bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get deployment mc-agents-mcp-server"

echo "== cleaning up"
k delete minecraftbotpool scouts --wait=true

echo "all checks passed"
