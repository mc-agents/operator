#!/usr/bin/env bash
# Install or upgrade the source chart into a k3d cluster, running the image `make image` built and
# `k3d image import` put on the nodes. One place for the helm invocation: `make k3d-deploy` runs it
# on a fresh cluster, and `hack/verify-k3d.sh --from` runs it over a published release.
set -o errexit -o nounset -o pipefail

CONTEXT="${1:?kube context}"
NAMESPACE="${2:?namespace}"
ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
CHART="${ROOT}/charts/mc-agents-operator"
RELEASE=mc-agents-operator

# Helm 4 applies server-side, and a CRD that kubectl applied before 0.13 has kubectl as the field
# manager of its schema; taking it over is the point of the upgrade. Helm 3 applies client-side and
# has no such flag.
force_conflicts=()
if helm upgrade --help 2>/dev/null | grep -q -- --force-conflicts; then
	force_conflicts=(--force-conflicts)
fi

helm --kube-context "${CONTEXT}" upgrade --install "${RELEASE}" "${CHART}" \
	--namespace "${NAMESPACE}" --create-namespace \
	--set image.registry=mc-agents \
	--set image.repository=operator \
	--set image.tag=dev \
	--set image.pullPolicy=Never \
	"${force_conflicts[@]}" \
	--wait

# The dev tag never changes, so the pod spec is identical and helm would leave the old image
# running. Restart explicitly or the next verify run tests the previous build.
kubectl --context "${CONTEXT}" -n "${NAMESPACE}" rollout restart deploy/mc-agents-operator
kubectl --context "${CONTEXT}" -n "${NAMESPACE}" rollout status deploy/mc-agents-operator
