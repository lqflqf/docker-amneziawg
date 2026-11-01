#!/usr/bin/env bats
# detect_public_ipv4 in init-amneziawg-confs, with a stub curl.

load helpers

setup() {
    load_confs
    # The stub writes the answer for each service to the -o file, after an
    # optional delay. It records each call so the tests can count them.
    stub curl '
out="" url=""
while (( $# )); do
    case "$1" in
        -o) out=$2; shift ;;
        https://*) url=$1 ;;
    esac
    shift
done
echo "${url}" >> "'"${BATS_TEST_TMPDIR}"'/calls"
sleep "${STUB_DELAY:-0}"
case "${url}" in
    *icanhazip*) answer=${STUB_A-} ;;
    *ipify*)     answer=${STUB_B-} ;;
    *ifconfig*)  answer=${STUB_C-} ;;
esac
[[ -n "${answer}" ]] || exit 22
printf "%s\n" "${answer}" > "${out}"'
    export STUB_A STUB_B STUB_C STUB_DELAY
}

@test "the first service in order wins when all answer" {
    STUB_A=198.51.100.1 STUB_B=198.51.100.2 STUB_C=198.51.100.3
    run detect_public_ipv4
    [ "${status}" -eq 0 ]
    [ "${output}" = 198.51.100.1 ]
}

@test "a malformed or missing answer falls through to the next service" {
    STUB_A='<html>error</html>' STUB_B='' STUB_C=203.0.113.9
    run detect_public_ipv4
    [ "${status}" -eq 0 ]
    [ "${output}" = 203.0.113.9 ]
}

@test "detection fails when no service gives an IPv4 address" {
    STUB_A='' STUB_B='2001:db8::1' STUB_C='not an ip'
    run detect_public_ipv4
    [ "${status}" -ne 0 ]
    [ -z "${output}" ]
}

@test "the services are asked at the same time" {
    STUB_A=198.51.100.1 STUB_B=198.51.100.2 STUB_C=198.51.100.3 STUB_DELAY=2
    start=${SECONDS}
    run detect_public_ipv4
    [ "${status}" -eq 0 ]
    (( SECONDS - start < 5 ))
    [ "$(wc -l < "${BATS_TEST_TMPDIR}/calls")" -eq 3 ]
}

@test "detection leaves no temporary files" {
    STUB_A=198.51.100.1
    run detect_public_ipv4
    [ -z "$(ls -d /tmp/ipdetect.* 2>/dev/null)" ]
}
