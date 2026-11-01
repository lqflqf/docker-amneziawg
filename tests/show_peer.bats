#!/usr/bin/env bats
# /app/show-peer.

load helpers

setup() {
    load_script /app/show-peer
    CONFIG_DIR=${BATS_TEST_TMPDIR}/config
    mkdir -p "${CONFIG_DIR}/wg_confs"
    printf '[Interface]\n# peer_laptop\n[Peer]\n' > "${CONFIG_DIR}/wg_confs/wg0.conf"
}

@test "peer_id_for maps numbers and names to the peer directories" {
    [ "$(peer_id_for 3)" = peer3 ]
    [ "$(peer_id_for laptop)" = peer_laptop ]
    [ "$(peer_id_for '../x')" = peer_x ]
}

@test "an unknown peer is reported as not active" {
    run main tablet
    [ "${status}" -eq 0 ]
    [ "${output}" = "PEER tablet is not active" ]
}

@test "no argument prints the usage" {
    run main
    [ "${output}" = "You need to specify which peers to show" ]
}
