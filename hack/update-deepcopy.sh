#!/usr/bin/env bash
set -o errexit -o nounset -o pipefail

# shellcheck source=hack/lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
"${SCRIPT_ROOT}/hack/install-tools.sh"

"${LOCALBIN}/controller-gen" \
	object:headerFile="${SCRIPT_ROOT}/hack/boilerplate.go.txt" \
	paths="${SCRIPT_ROOT}/api/..."
