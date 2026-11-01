#!/usr/bin/env bash
# Run the bats unit tests (tests/*.bats) inside a built image, so the scripts
# under test are the installed ones, with the image's busybox, GNU coreutils
# and awg. bats is added to a throwaway container only; the image does not
# change.
#
# Usage: unit-test.sh <image>
# ENGINE=nerdctl runs the container with nerdctl instead of docker.
set -euo pipefail

image=${1:?usage: unit-test.sh <image>}
ENGINE=${ENGINE:-docker}
# Alpine keeps only the newest version of a package; bump this pin when apk
# cannot find it.
BATS_PKG=bats=1.13.0-r1
tests_dir=$(cd "$(dirname "$0")/../../tests" && pwd)

# shellcheck disable=SC2016 # expanded inside the container
"$ENGINE" run --rm --entrypoint /bin/bash \
    -v "${tests_dir}:/tests:ro" \
    -e BATS_PKG="${BATS_PKG}" \
    "$image" -euc '
        apk add --no-cache --quiet "${BATS_PKG}" >/dev/null
        bats --print-output-on-failure /tests
    '
