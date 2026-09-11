#!/usr/bin/env bash
# Consistency: VERSION, Chart.version and Chart.appVersion must agree, because the published
# image tag is built from VERSION and a chart that disagreed would claim a version it was never
# released as. Monotonicity: with a base revision given, VERSION must have gone up.
set -o errexit -o nounset -o pipefail

ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
CHART="${ROOT}/charts/mc-agents-operator/Chart.yaml"

version="$(tr -d '[:space:]' <"${ROOT}/VERSION")"
chart_version="$(sed -n 's/^version: *//p' "${CHART}" | tr -d '"')"
chart_app_version="$(sed -n 's/^appVersion: *//p' "${CHART}" | tr -d '"')"

fail() {
	echo "$*" >&2
	exit 1
}

[[ -n "${version}" ]] || fail "VERSION is empty"
[[ "${chart_version}" == "${version}" ]] || fail "Chart.yaml version ${chart_version} != VERSION ${version}"
[[ "${chart_app_version}" == "${version}" ]] || fail "Chart.yaml appVersion ${chart_app_version} != VERSION ${version}"

base="${1:-}"
if [[ -z "${base}" ]]; then
	echo "version ${version} is consistent"
	exit 0
fi

if ! previous="$(git -C "${ROOT}" show "${base}:VERSION" 2>/dev/null | tr -d '[:space:]')"; then
	echo "version ${version} is consistent; ${base} has no VERSION to compare against"
	exit 0
fi

[[ "${previous}" != "${version}" ]] || fail "VERSION is still ${version}; raise it"
highest="$(printf '%s\n%s\n' "${previous}" "${version}" | sort -V | tail -1)"
[[ "${highest}" == "${version}" ]] || fail "VERSION went down: ${previous} -> ${version}"

echo "version ${previous} -> ${version}"
