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

count_is() { [[ "$(k get minecraftbots -l "mc-agents.dev/pool=$1" --no-headers 2>/dev/null | wc -l | tr -d ' ')" == "$2" ]]; }

echo "== applying examples"
k apply -f "${ROOT}/examples/minecraftbot.yaml"
k apply -f "${ROOT}/examples/minecraftbotpool.yaml"

echo "== a single bot becomes a pod"
await "minecraftbot/scout names its pod" 60 equals minecraftbot scout .status.podName scout
await "pod/scout exists" 60 k get pod scout

echo "== a missing bot image lands in status, not just in the pod"
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

echo "== deleting the CR collects the pod"
k delete minecraftbot scout --wait=true
await "pod/scout is gone" 60 bash -c "! kubectl --context ${CONTEXT} -n ${NAMESPACE} get pod scout"

echo "== cleaning up"
k delete minecraftbotpool scouts --wait=true

echo "all checks passed"
