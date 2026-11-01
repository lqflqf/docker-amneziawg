#!/usr/bin/env bats
# AWG parameter generation and the saved-parameter rules in
# init-amneziawg-confs.

load helpers

setup() {
    unset "${!AWG_@}"
    load_confs
    AWG_PARAMS_FILE=${BATS_TEST_TMPDIR}/awg_params
}

h_lo() { echo "${1%%-*}"; }
h_hi() { echo "${1##*-}"; }

@test "normalize_awg_switch maps synonyms and drops unknown values" {
    [ "$(normalize_awg_switch X on)" = on ]
    [ "$(normalize_awg_switch X OFF)" = off ]
    [ "$(normalize_awg_switch X yes)" = on ]
    [ "$(normalize_awg_switch X 0)" = off ]
    [ -z "$(normalize_awg_switch X '')" ]
    [ -z "$(normalize_awg_switch X maybe 2>/dev/null)" ]
}

@test "the default is 2.0 with non-overlapping H ranges and an I1 signature" {
    generate_awg_params >/dev/null
    [ "${AWG_VERSION}" = 2.0 ]
    (( $(h_hi "${AWG_H1}") < $(h_lo "${AWG_H2}") ))
    (( $(h_hi "${AWG_H2}") < $(h_lo "${AWG_H3}") ))
    (( $(h_hi "${AWG_H3}") < $(h_lo "${AWG_H4}") ))
    (( $(h_lo "${AWG_H1}") >= 5 ))
    [[ "${AWG_I1}" == *"<r 1178>" ]]
    (( AWG_S4 >= 4 && AWG_S4 <= 20 ))
    [ -z "${AWG_HEADER_PROTECTION_KEY}" ]
    [ -z "${AWG_RANDOM_TRAILERS}" ]
}

@test "3.1 turns RandomTrailers on and draws one S value for S1-S4" {
    AWG_VERSION=3.1
    generate_awg_params >/dev/null
    [ "${AWG_RANDOM_TRAILERS}" = on ]
    [ -z "${AWG_RT_EXPLICIT}" ]
    [ "${AWG_S1}" = "${AWG_S2}" ]
    [ "${AWG_S2}" = "${AWG_S3}" ]
    [ "${AWG_S3}" = "${AWG_S4}" ]
    (( AWG_S1 >= 12 && AWG_S1 <= 20 ))
    [ "${AWG_CONTENT_PADDING}" = 0 ]
    [ "$(printf '%s' "${AWG_HEADER_PROTECTION_KEY}" | base64 -d | wc -c)" -eq 32 ]
    [ -z "${AWG_PARAMS_INVALID}" ]
}

@test "3.0 keeps S values at 12 or more" {
    AWG_VERSION=3.0
    generate_awg_params >/dev/null
    (( AWG_S3 >= 12 && AWG_S4 >= 12 && AWG_S4 <= 20 ))
    [ -n "${AWG_REJECT_AFTER_TIME}" ]
}

@test "a pinned S value below 12 under 3.x blocks generation" {
    AWG_VERSION=3.0
    AWG_S1=8
    generate_awg_params > "${BATS_TEST_TMPDIR}/out"
    grep -q 'AWG_S1 (8) < 12' "${BATS_TEST_TMPDIR}/out"
    [ "${AWG_PARAMS_INVALID}" = 1 ]
}

@test "1.5 has integer H values, no S3/S4 and no signatures" {
    AWG_VERSION=1.5
    generate_awg_params >/dev/null
    [[ "${AWG_H1}" =~ ^[0-9]+$ ]]
    [ "${AWG_S3}" = 0 ]
    [ "${AWG_S4}" = 0 ]
    [ -z "${AWG_I1}" ]
}

@test "saved values are reused, and the environment wins over them" {
    printf '%s\n' AWG_VERSION=2.0 AWG_JC=7 AWG_S1=33 AWG_H1=100-200 > "${AWG_PARAMS_FILE}"
    AWG_S1=44
    load_awg_params
    generate_awg_params >/dev/null
    [ "${AWG_JC}" = 7 ]
    [ "${AWG_S1}" = 44 ]
    [ "${AWG_H1}" = 100-200 ]
}

@test "a version change drops the saved version-specific values only" {
    printf '%s\n' AWG_VERSION=2.0 AWG_JC=7 AWG_S1=5 AWG_S3=9 AWG_H1=100-200 > "${AWG_PARAMS_FILE}"
    AWG_VERSION=3.0
    load_awg_params
    generate_awg_params >/dev/null
    [ "${AWG_JC}" = 7 ]
    [ "${AWG_H1}" != 100-200 ]
    (( AWG_S1 >= 12 ))
    [ -z "${AWG_PARAMS_INVALID}" ]
}

@test "the saved version is kept when AWG_VERSION is not set" {
    printf '%s\n' AWG_VERSION=1.5 AWG_H1=1234 > "${AWG_PARAMS_FILE}"
    load_awg_params
    generate_awg_params >/dev/null
    [ "${AWG_VERSION}" = 1.5 ]
    [ "${AWG_H1}" = 1234 ]
}

@test "RandomTrailers drops saved unequal S values but keeps pinned ones" {
    printf '%s\n' AWG_VERSION=2.0 AWG_S1=30 AWG_S2=40 AWG_S3=10 AWG_S4=8 > "${AWG_PARAMS_FILE}"
    AWG_RANDOM_TRAILERS=on
    load_awg_params
    generate_awg_params >/dev/null
    [ "${AWG_S1}" = "${AWG_S2}" ]
    [ "${AWG_S2}" = "${AWG_S3}" ]
    [ "${AWG_S3}" = "${AWG_S4}" ]
    [ "${AWG_RT_EXPLICIT}" = on ]
}

@test "save_awg_params writes only the explicit 3.1 switches" {
    AWG_VERSION=3.1
    generate_awg_params >/dev/null
    save_awg_params
    grep -qx 'AWG_RANDOM_TRAILERS=' "${AWG_PARAMS_FILE}"
    grep -qx 'AWG_VERSION=3.1' "${AWG_PARAMS_FILE}"
}

@test "awg31_options_block writes only the switches that are on" {
    AWG_RANDOM_TRAILERS=on
    AWG_DISABLE_COOKIES=off
    awg31_options_block
    [ "${BLOCK_OUT}" = $'RandomTrailers = on\n' ]
    AWG_RANDOM_TRAILERS=""
    AWG_DISABLE_COOKIES=on
    awg31_options_block
    [ "${BLOCK_OUT}" = $'DisableCookies = on\n' ]
}
