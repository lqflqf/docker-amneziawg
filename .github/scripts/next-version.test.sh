#!/usr/bin/env bash
# Tests for next-version.sh against a scratch git repository.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
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
    actual=$("$script" "$@" | paste -sd' ')
    if [[ "$actual" == "$expected" ]]; then
        echo "ok   - ${name}"
    else
        echo "FAIL - ${name}: expected '${expected}', got '${actual}'"
        fails=$((fails + 1))
    fi
}
release() { git tag -a "$1" -m "$1" -m "run-id: $2"; }

check "no tags starts at r1" "version=3.1.20260812-r1 reused=false" v3.1.20260812 100
check "v prefix is optional" "version=3.1.20260812-r1 reused=false" 3.1.20260812 100

release v3.1.20260812-r1 100
git commit -q --allow-empty -m two
release v3.1.20260812-r2 101
git commit -q --allow-empty -m three
check "next after r2 is r3" "version=3.1.20260812-r3 reused=false" v3.1.20260812 102
check "rerun reuses its own tag" "version=3.1.20260812-r2 reused=true" v3.1.20260812 101
check "run id must match exactly" "version=3.1.20260812-r3 reused=false" v3.1.20260812 10

check "other tools version starts at r1" "version=3.1.20260901-r1 reused=false" v3.1.20260901 102

git tag v1.0.0
git tag -a v3.1.20260812-r9-hotfix -m x -m "run-id: 1"
git tag -a v3.1.20260812-rc1 -m x -m "run-id: 1"
git tag -a v3.1x20260812-r50 -m x -m "run-id: 1"
check "foreign tags are ignored" "version=3.1.20260812-r3 reused=false" v3.1.20260812 102

release v3.1.20260812-r10 103
check "counter compares numerically" "version=3.1.20260812-r11 reused=false" v3.1.20260812 104

if "$script" 'v3.1;rm' 1 >/dev/null 2>&1; then
    echo "FAIL - rejects unexpected tools version"
    fails=$((fails + 1))
else
    echo "ok   - rejects unexpected tools version"
fi

if (( fails > 0 )); then
    echo "${fails} test(s) failed"
    exit 1
fi
echo "all tests passed"
