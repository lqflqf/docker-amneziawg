#!/usr/bin/env bash
# Rewrite the upstream version pins (ARG defaults) at the top of the Dockerfile.
#
# Usage: update-pins.sh <amneziawg-tools version|""> <amneziawg-go version|"">
# An empty argument leaves that pin unchanged.
set -euo pipefail

tools="${1-}"
go="${2-}"
dockerfile="${DOCKERFILE:-Dockerfile}"
version_re='^v[0-9]+\.[0-9]+\.[0-9]+$'

set_pin() {
    local arg=$1 value=$2
    [[ -z "$value" ]] && return 0
    if [[ ! "$value" =~ $version_re ]]; then
        echo "update-pins: refusing unexpected ${arg} '${value}'" >&2
        exit 1
    fi
    if [[ "$(grep -c "^ARG ${arg}=" "$dockerfile")" != 1 ]]; then
        echo "update-pins: expected exactly one 'ARG ${arg}=' line in ${dockerfile}" >&2
        exit 1
    fi
    sed -i "s/^ARG ${arg}=.*/ARG ${arg}=${value}/" "$dockerfile"
    grep -qx "ARG ${arg}=${value}" "$dockerfile"
    echo "${arg} -> ${value}"
}

set_pin AMNEZIAWG_TOOLS_VERSION "$tools"
set_pin AMNEZIAWG_GO_VERSION "$go"
