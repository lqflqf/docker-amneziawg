#!/usr/bin/env bash
# Rewrite the upstream pins (ARG defaults) at the top of the Dockerfile: each
# version tag together with the commit it resolved to when it was tested.
#
# Usage: update-pins.sh <tools version|""> <tools commit|""> <go version|""> <go commit|"">
# An empty version leaves that pin (and its commit) unchanged; a version needs
# its commit.
set -euo pipefail

tools="${1-}"
tools_commit="${2-}"
go="${3-}"
go_commit="${4-}"
dockerfile="${DOCKERFILE:-Dockerfile}"
version_re='^v[0-9]+\.[0-9]+\.[0-9]+$'
commit_re='^[0-9a-f]{40}$'

check() {
    local arg=$1 value=$2 re=$3
    if [[ ! "$value" =~ $re ]]; then
        echo "update-pins: refusing unexpected ${arg} '${value}'" >&2
        exit 1
    fi
}

set_arg() {
    local arg=$1 value=$2
    if [[ "$(grep -c "^ARG ${arg}=" "$dockerfile")" != 1 ]]; then
        echo "update-pins: expected exactly one 'ARG ${arg}=' line in ${dockerfile}" >&2
        exit 1
    fi
    sed -i "s/^ARG ${arg}=.*/ARG ${arg}=${value}/" "$dockerfile"
    grep -qx "ARG ${arg}=${value}" "$dockerfile"
    echo "${arg} -> ${value}"
}

set_pin() {
    local prefix=$1 version=$2 commit=$3
    [[ -z "$version" ]] && return 0
    set_arg "${prefix}_VERSION" "$version"
    set_arg "${prefix}_COMMIT" "$commit"
}

# Validate everything before touching the Dockerfile.
for pair in "AMNEZIAWG_TOOLS:$tools:$tools_commit" "AMNEZIAWG_GO:$go:$go_commit"; do
    IFS=: read -r prefix version commit <<<"$pair"
    [[ -z "$version" ]] && continue
    check "${prefix}_VERSION" "$version" "$version_re"
    check "${prefix}_COMMIT" "$commit" "$commit_re"
done

set_pin AMNEZIAWG_TOOLS "$tools" "$tools_commit"
set_pin AMNEZIAWG_GO "$go" "$go_commit"
