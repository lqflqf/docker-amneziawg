#!/usr/bin/env bash
# Run the Go integration tests (internal/integration, build tag "integration")
# inside a built image: the test binary is compiled in the golang container,
# then run as root in a throwaway container of the image with NET_ADMIN and
# /dev/net/tun, against the real awg, awg-quick, amneziawg-go and dnsmasq.
#
# Usage: integration-test.sh <image>
# ENGINE=nerdctl runs the containers with nerdctl instead of docker.
set -euo pipefail

image=${1:?usage: integration-test.sh <image>}
ENGINE=${ENGINE:-docker}
root=$(cd "$(dirname "$0")/../.." && pwd)

if [[ ! -c /dev/net/tun ]]; then
    if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
        echo "integration-test.sh: /dev/net/tun is missing on the runner" >&2
        exit 1
    fi
    echo "integration-test.sh: skipped (no /dev/net/tun on this host)"
    exit 0
fi

out="${root}/.integration"
mkdir -p "${out}"
trap 'rm -rf "${out}"' EXIT
ENGINE="${ENGINE}" "${root}/.github/scripts/go.sh" test -c -tags integration \
    -o /src/.integration/awgctl.test ./internal/integration

"${ENGINE}" run --rm --cap-add NET_ADMIN --device /dev/net/tun \
    -v "${out}:/t:ro" --entrypoint /t/awgctl.test \
    "${image}" -test.v -test.count=1
