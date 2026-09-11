# shellcheck shell=bash

SCRIPT_ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
LOCALBIN="${SCRIPT_ROOT}/bin"
mkdir -p "${LOCALBIN}"

if command -v go >/dev/null 2>&1; then
	GO="$(command -v go)"
elif [[ -x "${HOME}/sdk/go1.26.1/bin/go" ]]; then
	GO="${HOME}/sdk/go1.26.1/bin/go"
else
	echo "go not in PATH; export GO=/path/to/go or install Go" >&2
	exit 1
fi
GO_BIN_DIR="$(dirname "${GO}")"

export PATH="${GO_BIN_DIR}:${LOCALBIN}:${PATH}"
