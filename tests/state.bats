#!/usr/bin/env bats
# The saved state (/config/.donoteditthisfile) and peer bookkeeping in
# init-amneziawg-confs.

load helpers

setup() {
    load_confs
    STATE_FILE=${BATS_TEST_TMPDIR}/state
}

@test "write_state and load_state round-trip values with quotes and newlines" {
    SERVERURL='vpn.example.com'
    ALLOWEDIPS='0.0.0.0/0, ::/0'
    AWG_I1=$'<b 0x01>\n<r 8>'
    write_state "${STATE_FILE}" SERVERURL ALLOWEDIPS AWG_I1
    load_state
    [ "${ORIG[SERVERURL]}" = vpn.example.com ]
    [ "${ORIG[ALLOWEDIPS]}" = '0.0.0.0/0, ::/0' ]
    [ "${ORIG[AWG_I1]}" = '<b 0x01> <r 8>' ]
}

@test "load_state never runs the file as code" {
    printf 'ORIG_SERVERURL="$(touch %s/pwned)"\n' "${BATS_TEST_TMPDIR}" > "${STATE_FILE}"
    load_state
    [ ! -e "${BATS_TEST_TMPDIR}/pwned" ]
    [ "${ORIG[SERVERURL]}" = "\$(touch ${BATS_TEST_TMPDIR}/pwned)" ]
}

@test "state_changed finds a change and ignores missing optional keys" {
    for name in "${STATE_VARS[@]}"; do
        printf -v "${name}" '%s' "value-${name}"
    done
    save_vars
    load_state
    run ! state_changed

    grep -v '^ORIG_TEMPLATES_HASH=' "${STATE_FILE}" > "${STATE_FILE}.new"
    mv "${STATE_FILE}.new" "${STATE_FILE}"
    ORIG=()
    load_state
    run ! state_changed

    SERVERPORT=51821
    state_changed
    [ "${CHANGED_VAR}" = SERVERPORT ]
}

@test "wants_keepalive matches all, a PEERS entry or a peer ID" {
    PERSISTENTKEEPALIVE_PEERS_ARRAY=(laptop peer_phone)
    wants_keepalive peer_laptop laptop
    wants_keepalive peer_phone phone
    run ! wants_keepalive peer_tablet tablet
    PERSISTENTKEEPALIVE_PEERS_ARRAY=(all)
    wants_keepalive peer_tablet tablet
    PERSISTENTKEEPALIVE_PEERS_ARRAY=()
    run ! wants_keepalive peer_tablet tablet
}

@test "assign_peer_addresses keeps existing addresses and fills the lowest free one" {
    declare -gA PEER_IP=()
    INTERFACE=10.13.13
    ORIG_INTERFACE=10.13.13
    mkdir -p /config/peer_a /config/peer_b
    printf '[Interface]\nAddress = 10.13.13.3\n' > /config/peer_a/peer_a.conf
    printf '[Interface]\nAddress = 10.13.13.3\n' > /config/peer_b/peer_b.conf
    PEER_IDS=(peer_a peer_b peer_c)
    assign_peer_addresses >/dev/null
    [ "${PEER_IP[peer_a]}" = 10.13.13.3 ]
    [ "${PEER_IP[peer_b]}" = 10.13.13.2 ]
    [ "${PEER_IP[peer_c]}" = 10.13.13.4 ]
    rm -rf /config/peer_a /config/peer_b
}

@test "assign_peer_addresses moves existing peers to a new subnet" {
    declare -gA PEER_IP=()
    INTERFACE=10.20.30
    ORIG_INTERFACE=10.13.13
    mkdir -p /config/peer_a
    printf '[Interface]\nAddress = 10.13.13.7\n' > /config/peer_a/peer_a.conf
    PEER_IDS=(peer_a)
    assign_peer_addresses >/dev/null
    [ "${PEER_IP[peer_a]}" = 10.20.30.7 ]
    rm -rf /config/peer_a
}

@test "check_template refuses world-writable templates" {
    printf 'x\n' > "${BATS_TEST_TMPDIR}/t.conf"
    chmod 644 "${BATS_TEST_TMPDIR}/t.conf"
    check_template "${BATS_TEST_TMPDIR}/t.conf"
    chmod 646 "${BATS_TEST_TMPDIR}/t.conf"
    run check_template "${BATS_TEST_TMPDIR}/t.conf"
    [ "${status}" -eq 1 ]
    [[ "${output}" == *"world-writable"* ]]
}
