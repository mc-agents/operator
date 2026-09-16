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

# The upstream script rather than go install: golangci-lint is built with a pinned toolchain and
# its own instructions say a source build may not match the release.
GOLANGCI_LINT_VERSION="v2.11.4"
GOLANGCI_LINT="${LOCALBIN}/golangci-lint"

if [[ ! -x "${GOLANGCI_LINT}" ]] || ! "${GOLANGCI_LINT}" --version 2>/dev/null | grep -q "${GOLANGCI_LINT_VERSION#v}"; then
	echo "installing golangci-lint ${GOLANGCI_LINT_VERSION}"
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh \
		| sh -s -- -b "${LOCALBIN}" "${GOLANGCI_LINT_VERSION}"
fi
