#!/usr/bin/env bash
set -o errexit -o nounset -o pipefail

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

CONTROLLER_GEN_VERSION="v0.21.0"
CONTROLLER_GEN="${LOCALBIN}/controller-gen"

if [[ ! -x "${CONTROLLER_GEN}" ]] || ! "${CONTROLLER_GEN}" --version 2>/dev/null | grep -q "${CONTROLLER_GEN_VERSION}"; then
	echo "installing controller-gen ${CONTROLLER_GEN_VERSION}"
	GOBIN="${LOCALBIN}" "${GO}" install "sigs.k8s.io/controller-tools/cmd/controller-gen@${CONTROLLER_GEN_VERSION}"
fi
