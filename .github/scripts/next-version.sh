#!/usr/bin/env bash
# Resolve the release version <tools>-r<N> for this workflow run.
#
# Usage: next-version.sh <amneziawg-tools version> <workflow run id>
#
# The build counter N lives in git: every release is an annotated tag
# v<tools>-r<N> whose message carries "run-id: <id>" of the run that made it.
#   - a tag already carrying this run id is reused, so re-running a
#     workflow run keeps its number instead of claiming a new one;
#   - otherwise N is one past the highest existing N for this tools version,
#     starting at 1 for a tools version that has never been released.
# Tags for other tools versions, and tags outside this scheme, are ignored.
#
# Prints GITHUB_OUTPUT lines: version=<tools>-r<N> and reused=true|false.
set -euo pipefail

tools="${1:?usage: next-version.sh <tools-version> <run-id>}"
run_id="${2:?usage: next-version.sh <tools-version> <run-id>}"
tools="${tools#v}"

if [[ ! "$tools" =~ ^[0-9A-Za-z._]+$ ]]; then
    echo "next-version: unexpected amneziawg-tools version '${tools}'" >&2
    exit 1
fi

tag_re="^v${tools//./\\.}-r([1-9][0-9]*)$"
max=0
reuse=""

while IFS= read -r tag; do
    [[ "$tag" =~ $tag_re ]] || continue
    n=$((10#${BASH_REMATCH[1]}))
    (( n > max )) && max=$n
    if git tag -l --format='%(contents)' "$tag" | grep -qx "run-id: ${run_id}"; then
        reuse=$n
    fi
done < <(git tag -l "v${tools}-r*")

if [[ -n "$reuse" ]]; then
    echo "version=${tools}-r${reuse}"
    echo "reused=true"
else
    echo "version=${tools}-r$((max + 1))"
    echo "reused=false"
fi
