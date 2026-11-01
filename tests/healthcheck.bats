#!/usr/bin/env bats
# /app/healthcheck, with stub awg, ip and nslookup.

load helpers

setup() {
    load_script /app/healthcheck
    ACTIVECONFS=${BATS_TEST_TMPDIR}/activeconfs
    DNSMASQ_STATE=${BATS_TEST_TMPDIR}/dnsmasq-state
    DNSMASQ_ADDRESS=${BATS_TEST_TMPDIR}/address
    stub awg 'printf "%s\n" ${STUB_IFACES}'
    stub ip 'echo "5: wg0    inet ${STUB_ADDR:-10.13.13.1}/24 scope global wg0"'
    stub nslookup 'exit ${STUB_NS:-0}'
    export STUB_IFACES STUB_ADDR STUB_NS
    unset PEERS
}

active() {
    local AWG_CONFS=("$@")
    declare -p AWG_CONFS > "${ACTIVECONFS}"
}

@test "unhealthy without active tunnels" {
    run main
    [ "${status}" -eq 1 ]
    [ "${output}" = "no active tunnels" ]
}

@test "unhealthy when a tunnel is gone" {
    active /config/wg_confs/wg0.conf /config/wg_confs/wg1.conf
    STUB_IFACES=wg0
    run main
    [ "${status}" -eq 1 ]
    [ "${output}" = "tunnel wg1 is down" ]
}

@test "healthy client with every tunnel up" {
    active /config/wg_confs/wg0.conf
    STUB_IFACES=wg0
    run main
    [ "${status}" -eq 0 ]
    [ "${output}" = "tunnels up: wg0" ]
}

@test "server mode needs dnsmasq running, bound and answering" {
    export PEERS=1
    active /config/wg_confs/wg0.conf
    STUB_IFACES=wg0
    echo 10.13.13.1 > "${DNSMASQ_ADDRESS}"

    run main
    [ "${status}" -eq 1 ]
    [ "${output}" = "dnsmasq is not running (state: none)" ]

    echo invalid > "${DNSMASQ_STATE}"
    run main
    [[ "${output}" == "dnsmasq has no valid config"* ]]

    echo running > "${DNSMASQ_STATE}"
    STUB_ADDR=10.99.99.1
    run main
    [ "${output}" = "the tunnel address 10.13.13.1 is not on wg0" ]

    STUB_ADDR=10.13.13.1 STUB_NS=1
    run main
    [ "${output}" = "dnsmasq does not answer on 10.13.13.1" ]

    STUB_NS=0
    run main
    [ "${status}" -eq 0 ]
}
