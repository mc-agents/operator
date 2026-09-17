#!/usr/bin/env bash
# The release before this one: the newest VERSION in the history that is not the current value,
# walking back from the given revision (default HEAD). What the upgrade verification installs
# first, and what the release notes start after when there is no tag yet to start from.
#
# The history rather than the registry, so the answer is the same on a laptop and on a runner and
# a release that was never published fails the upgrade loudly instead of being skipped.
set -o errexit -o nounset -o pipefail

ROOT="$(realpath "$(dirname "${BASH_SOURCE[0]}")/..")"
from="${1:-HEAD}"

current="$(git -C "${ROOT}" show "${from}:VERSION" | tr -d '[:space:]')"
while read -r commit; do
	version="$(git -C "${ROOT}" show "${commit}:VERSION" | tr -d '[:space:]')"
	if [[ "${version}" != "${current}" ]]; then
		echo "${version}"
		exit 0
	fi
done < <(git -C "${ROOT}" log --format=%H "${from}" -- VERSION)

echo "no VERSION before ${current} in the history of ${from}" >&2
exit 1
