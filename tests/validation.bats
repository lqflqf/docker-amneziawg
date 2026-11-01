#!/usr/bin/env bats
# Input validation in init-amneziawg-confs: IPv4/IPv6, Endpoint hosts, ports
# and DNS_UPSTREAM.

load helpers

setup() {
    load_confs
}

@test "valid_port accepts 1-65535 in decimal" {
    for port in 1 53 51820 65535; do
        valid_port "${port}"
    done
}

@test "valid_port refuses leading zeros, out-of-range and non-numeric values" {
    for port in 0 08 0100000 051820 65536 100000 -1 "" 1a " 1" 1.5; do
        run valid_port "${port}"
        [ "${status}" -ne 0 ] || { echo "accepted: '${port}'"; return 1; }
    done
}

@test "is_ipv4 accepts dotted quads and refuses the rest" {
    for ip in 0.0.0.0 10.13.13.1 255.255.255.255; do
        is_ipv4 "${ip}"
    done
    for ip in 256.1.1.1 1.2.3 1.2.3.4.5 01.2.3.4 a.b.c.d ""; do
        run is_ipv4 "${ip}"
        [ "${status}" -ne 0 ] || { echo "accepted: '${ip}'"; return 1; }
    done
}

@test "is_ipv6 accepts valid addresses" {
    for ip in :: ::1 1:: 2001:db8::1 1:2:3:4:5:6:7:8 1:2:3:4:5:6:7:: \
        fe80::1:2 ::192.0.2.1 ::ffff:192.0.2.1 64:ff9b::192.0.2.1 \
        1:2:3:4:5:6:192.0.2.1 ABCD:ef01::; do
        is_ipv6 "${ip}" || { echo "refused: '${ip}'"; return 1; }
    done
}

@test "is_ipv6 refuses malformed addresses" {
    for ip in ::: 1::2::3 1:2:3:4:5:6:7:8:9 1:2:3:4:5:6:7 1:2:3:4:5:6:7:8:: \
        ::1:2:3:4:5:6:7:8 1:2:3:4:5:6:7:192.0.2.1 1:192.0.2.1:2 192.0.2.1:: \
        ::256.0.0.1 12345:: :1 1: 1::2: fe80::1%eth0 "[::1]" g::1 1.2.3.4 ""; do
        run is_ipv6 "${ip}"
        [ "${status}" -ne 0 ] || { echo "accepted: '${ip}'"; return 1; }
    done
}

@test "valid_endpoint_host accepts names, IPv4 and bracketed IPv6" {
    for host in vpn.example.com localhost example.com. 203.0.113.7 \
        "[2001:db8::1]" "[::ffff:192.0.2.1]" "[::1]"; do
        valid_endpoint_host "${host}" || { echo "refused: '${host}'"; return 1; }
    done
}

@test "valid_endpoint_host refuses broken IPv6 and host names" {
    for host in "[:::]" "[1::2::3]" "[]" "[2001:db8::1" 2001:db8::1 \
        "-bad.example.com" "bad_name.example.com" "a..b" ""; do
        run valid_endpoint_host "${host}"
        [ "${status}" -ne 0 ] || { echo "accepted: '${host}'"; return 1; }
    done
}

@test "parse_dns_upstream keeps IPv4, IPv6 and #port entries" {
    DNS_UPSTREAM=" 1.1.1.1, 2001:db8::1#5353 ,127.0.0.1#5335"
    parse_dns_upstream
    [ "${DNS_UPSTREAM}" = "1.1.1.1,2001:db8::1#5353,127.0.0.1#5335" ]
    [ "${DNS_SERVER_LINES}" = $'server=1.1.1.1\nserver=2001:db8::1#5353\nserver=127.0.0.1#5335' ]
}

@test "parse_dns_upstream refuses bad addresses, brackets, ports and empty lists" {
    for value in ":::" "[::1]" "[::1]#53" "1.1.1.1#0" "1.1.1.1#65536" \
        "1.1.1.1#53#1" "dns.example.com" "1::2::3" ", ,"; do
        DNS_UPSTREAM=${value}
        run parse_dns_upstream
        [ "${status}" -ne 0 ] || { echo "accepted: '${value}'"; return 1; }
    done
}
