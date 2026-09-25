#!/usr/bin/env bash
# Tests for release-tags.sh against a scratch git repository.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/release-tags.sh"
repo="$(mktemp -d)"
trap 'rm -rf "$repo"' EXIT
cd "$repo"

git init -q
git config user.name test
git config user.email test@example.com
git config tag.gpgSign false
git commit -q --allow-empty -m one

fails=0
check() {
    local name=$1 expected=$2 actual
    shift 2
    actual=$("$script" "$@")
    if [[ "$actual" == "$expected" ]]; then
        echo "ok   - ${name}"
    else
        echo "FAIL - ${name}: expected '${expected}', got '${actual}'"
        fails=$((fails + 1))
    fi
}
# release <tag> <run-id> <unix time>: an annotated release tag created at that time
release() { GIT_COMMITTER_DATE="@$3 +0000" git tag -a "$1" -m "$1" -m "run-id: $2" -m "image: x@sha256:0"; }

check "no tags: no latest" "" latest
check "no tags: not superseded" "false" superseded 100
check "no tags: no previous" "" previous 100

release v3.0.20260805-r1 100 1000
git commit -q --allow-empty -m two
release v3.1.20260812-r1 200 2000
check "latest is the highest run id" "v3.1.20260812-r1" latest
check "own run is not superseded" "false" superseded 200
check "older run is superseded" "true" superseded 150

# a re-run of run 150 tags after run 200 did: run 200 still owns latest
release v3.0.20260805-r2 150 3000
check "late tag from an older run is not latest" "v3.1.20260812-r1" latest
check "previous of the late tag is by run id" "v3.0.20260805-r1" previous 150
check "previous excludes own tag" "v3.0.20260805-r2" previous 200

# a force-push drops the latest release's commit from the branch
git reset -q --hard HEAD~1
check "latest need not be reachable from HEAD" "v3.1.20260812-r1" latest

git tag v9.9.9
git tag -a v9.9.9-r1-hotfix -m x -m "run-id: 999"
git tag -a v9.9.9-rc1 -m x -m "run-id: 999"
check "foreign tags are ignored" "v3.1.20260812-r1" latest
check "foreign tags do not supersede" "false" superseded 200

GIT_COMMITTER_DATE="@4000 +0000" git tag -a v3.1.20260812-r2 -m x
check "tags without run id rank lowest" "v3.1.20260812-r1" latest

check "run ids compare numerically" "false" superseded 1000

if "$script" superseded 'x;1' >/dev/null 2>&1; then
    echo "FAIL - rejects bad run id"
    fails=$((fails + 1))
else
    echo "ok   - rejects bad run id"
fi

if (( fails > 0 )); then
    echo "${fails} test(s) failed"
    exit 1
fi
echo "all tests passed"
