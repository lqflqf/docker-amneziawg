# shellcheck shell=bash
# Shared setup for the unit tests. They run inside the built image
# (.github/scripts/unit-test.sh), so the scripts under test are the installed
# ones, with the image's busybox, GNU coreutils and awg.

bats_require_minimum_version 1.5.0

S6=/etc/s6-overlay/s6-rc.d

# Each script only defines functions when it is sourced (see the guard at its
# end), so the tests can call those functions one by one.
load_confs() {
    # shellcheck source=/dev/null
    source "${S6}/init-amneziawg-confs/run"
}

load_script() {
    # shellcheck source=/dev/null
    source "$1"
}

# Puts a stub command first in PATH. The body is a bash script.
stub() {
    local name=$1 body=$2
    mkdir -p "${BATS_TEST_TMPDIR}/bin"
    printf '#!/bin/bash\n%s\n' "${body}" > "${BATS_TEST_TMPDIR}/bin/${name}"
    chmod +x "${BATS_TEST_TMPDIR}/bin/${name}"
    PATH="${BATS_TEST_TMPDIR}/bin:${PATH}"
}
