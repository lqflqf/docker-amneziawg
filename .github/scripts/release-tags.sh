#!/usr/bin/env bash
# Query the release tags v<tools>-r<N> (see next-version.sh).
#
# Usage:
#   release-tags.sh latest
#       Print the newest release tag, or nothing if there is none.
#   release-tags.sh superseded <run-id>
#       Print "true" if a release was made by a later workflow run than
#       <run-id>, otherwise "false".
#   release-tags.sh previous <run-id>
#       Print the newest release tag made by an earlier run than <run-id>,
#       or nothing if there is none.
#
# "Newest" means made by the highest workflow run id (recorded as "run-id: <id>"
# in the tag annotation), not "reachable from HEAD": after a force-push the
# last published image can be on a commit the branch no longer contains, and
# a re-run of an old run can create its tag after a newer run's. Run ids only
# grow, and publishing runs are serialized, so the highest id is the release
# `latest` points at. Tags without a run id rank below all others, ordered by
# creation date.
set -euo pipefail

tag_re='^v[0-9]+(\.[0-9]+)*-r[1-9][0-9]*$'

# Prints "<run-id> <tag>" for every release tag, oldest first.
list() {
    local tag run
    while IFS= read -r tag; do
        [[ "$tag" =~ $tag_re ]] || continue
        # awk reads all input (no early exit to SIGPIPE git under pipefail)
        run=$(git tag -l --format='%(contents)' "$tag" | awk '/^run-id: [0-9]+$/ && r == "" { r = $2 } END { print r }')
        echo "${run:-0} ${tag}"
    done < <(git for-each-ref --sort=creatordate --format='%(refname:strip=2)' 'refs/tags/v*-r*')
}

case "${1:-}" in
    latest)
        # stable sort keeps creation order among equal run ids, so tail wins ties
        list | sort -s -n -k1,1 | tail -n1 | cut -d' ' -f2
        ;;
    superseded | previous)
        run_id="${2:?usage: release-tags.sh $1 <run-id>}"
        [[ "$run_id" =~ ^[0-9]+$ ]] || { echo "release-tags: bad run id '${run_id}'" >&2; exit 1; }
        if [[ "$1" == previous ]]; then
            list | awk -v me="$run_id" '$1 < me + 0' | sort -s -n -k1,1 | tail -n1 | cut -d' ' -f2
        elif list | awk -v me="$run_id" '$1 > me + 0 { found = 1 } END { exit !found }'; then
            echo true
        else
            echo false
        fi
        ;;
    *)
        echo "usage: release-tags.sh latest | superseded <run-id> | previous <run-id>" >&2
        exit 1
        ;;
esac
