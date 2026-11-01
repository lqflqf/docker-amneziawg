#!/usr/bin/env bats
# PUID/PGID parsing in init-adduser.

load helpers

setup() {
    load_script "${S6}/init-adduser/run"
    # The real fatal stops the container through s6.
    fatal() {
        echo "FATAL: $*"
        exit 1
    }
}

@test "parse_id accepts 1-4294967294 and drops leading zeros" {
    parse_id PUID 1
    [ "${ID_OUT}" = 1 ]
    parse_id PUID 911
    [ "${ID_OUT}" = 911 ]
    parse_id PGID 0100
    [ "${ID_OUT}" = 100 ]
    parse_id PUID 4294967294
    [ "${ID_OUT}" = 4294967294 ]
}

@test "parse_id refuses root with an explicit message" {
    for value in 0 00; do
        run parse_id PUID "${value}"
        [ "${status}" -eq 1 ]
        [[ "${output}" == *"PUID=${value} (root) is not supported"*"id -u"* ]]
    done
    run parse_id PGID 0
    [ "${status}" -eq 1 ]
    [[ "${output}" == *"PGID=0 (root) is not supported"*"id -g"* ]]
}

@test "parse_id refuses values that are not IDs" {
    for value in -1 4294967295 99999999999 abc 1a "" " 1"; do
        run parse_id PUID "${value}"
        [ "${status}" -eq 1 ] || { echo "accepted: '${value}'"; return 1; }
        [[ "${output}" == *"not a numeric ID between 1 and 4294967294"* ]]
    done
}

@test "banner_line keeps the right border at a fixed column" {
    short=$(banner_line uid:gid 911:911)
    long=$(banner_line version "$(printf 'x%.0s' {1..80})")
    [[ "${short}" == "│  ▸ uid:gid  911:911 "*"  │" ]]
    [[ "${long}" == *"x…  │" ]]
    [ "$(LC_ALL=C.UTF-8 wc -m <<<"${short}")" -eq "$(LC_ALL=C.UTF-8 wc -m <<<"${long}")" ]
}
