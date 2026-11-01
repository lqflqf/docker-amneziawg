#!/usr/bin/env bash
# Run a Go command in the golang image that the Dockerfile builds with, so the
# host needs no Go toolchain.
#
# Usage: .github/scripts/go.sh <go args...>
#   .github/scripts/go.sh test ./...
#   .github/scripts/go.sh vet ./...
#   .github/scripts/go.sh mod tidy
# ENGINE=nerdctl runs the container with nerdctl instead of docker.
# GO_CGO=1 turns cgo on (for go test -race); the image then needs gcc, which
# this script adds to the throwaway container.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
ENGINE=${ENGINE:-docker}
# The single pin is the go-builder stage of the Dockerfile.
image=$(sed -n 's/^FROM \(golang:[^ ]*\) AS .*/\1/p' "${root}/Dockerfile" | head -n1)
[[ -n "${image}" ]] || { echo "go.sh: no golang image in the Dockerfile" >&2; exit 1; }

cgo=${GO_CGO:-0}
tty=()
[[ -t 0 && -t 1 ]] && tty=(-t)

# Named volumes keep the module and build caches between runs.
# shellcheck disable=SC2016 # expanded inside the container
"${ENGINE}" run --rm -i "${tty[@]}" \
    -v "${root}:/src" -w /src \
    -v awgctl-gomod:/go/pkg/mod \
    -v awgctl-gocache:/root/.cache/go-build \
    -e CGO_ENABLED="${cgo}" \
    -e GOFLAGS="${GOFLAGS:-}" \
    "${image}" sh -euc '
        if [ "${CGO_ENABLED}" = 1 ]; then apk add --no-cache --quiet gcc musl-dev >/dev/null; fi
        exec go "$@"
    ' sh "$@"
