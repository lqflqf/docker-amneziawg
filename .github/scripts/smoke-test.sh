#!/usr/bin/env bash
# Smoke-test a locally built image as a black box: image contents, the s6
# graph, start-up and its failure modes, identity, dnsmasq, awgctl through
# docker exec, the import of an old volume, and an end-to-end tunnel. The
# config logic itself is covered by the Go unit and integration tests.
#
# Usage: smoke-test.sh <image>
# ENGINE=nerdctl runs every container with nerdctl (containerd) instead of
# docker. Use only commands and flags that both engines accept.
# BUILD_DATE (optional): the date the image was built with; it must be in
# /build_version.
#
# Every check must fail the script on its own. Do not write `test ... && echo`:
# a failing left side of && is exempt from errexit, so only the last line of a
# group would count. Do not pipe into `grep -q` either: it exits at the first
# match, the writer can die of SIGPIPE, and pipefail fails the pipeline. Use
# grep -q ... <<<"$(cmd)".
# Single-quoted scripts run inside the container, where they expand.
# shellcheck disable=SC2016
set -euo pipefail

image="${1:?usage: smoke-test.sh <image>}"
ENGINE=${ENGINE:-docker}
engine() { command "$ENGINE" "$@"; }
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
container="awgci-$$"
volume="awgci-vol-$$"
network="awgci-net-$$"
tmpdir=$(mktemp -d)
cleanup() {
    local c
    for c in "$container" "$container-up" "$container-srv" "$container-cli" "$container-old"; do
        engine rm -f "$c" >/dev/null 2>&1 || true
    done
    engine volume rm -f "$volume" "$volume-srv" "$volume-old" >/dev/null 2>&1 || true
    engine network rm "$network" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT

# The last release of the shell-based image, for the import-legacy check.
legacy_image=ghcr.io/lqflqf/docker-amneziawg:3.1.20260812-r15@sha256:aa54a84d9c61a1f14b6fd5cf59f057184be1e0864dfcce8c009edd82f8af1799

# Runs a list of checks inside the image. Each argument is one shell
# condition; the first one that fails stops the group with a non-zero exit.
in_image() {
    local title=$1
    shift
    echo "### ${title}"
    echo "### ${title}" >> "$summary"
    engine run --rm --entrypoint /bin/sh "$image" -c '
        for check in "$@"; do
            if sh -c "$check"; then
                echo "  ok   - $check"
            else
                echo "  FAIL - $check"
                exit 1
            fi
        done
    ' sh "$@"
    echo "- ${title}: OK" >> "$summary"
}

section() {
    echo "### $1"
    echo "### $1" >> "$summary"
}
passed() { echo "- $1: OK" >> "$summary"; }
ok() { echo "  ok   - $1"; }
fail() {
    echo "FAIL - $1"
    engine logs "$container" 2>&1 | tail -n 60
    exit 1
}
logs_have() { grep -qF -- "$1" <<<"$(engine logs "${2:-$container}" 2>&1)"; }
dx() { engine exec "$container" "$@"; }

echo "## Smoke tests" >> "$summary"

in_image "Binaries" \
    'test -x /usr/bin/awg' \
    'test -x /usr/bin/awg-quick' \
    'test -x /usr/bin/amneziawg-go' \
    'test -x /usr/local/bin/awgctl' \
    'awgctl version | grep -q "^schema"' \
    '! test -e /usr/bin/wg && ! test -L /usr/bin/wg' \
    '! test -e /usr/bin/wg-quick && ! test -L /usr/bin/wg-quick' \
    '! test -e /etc/wireguard && ! test -L /etc/wireguard'

s6=/etc/s6-overlay/s6-rc.d
in_image "s6-overlay services" \
    "test -f $s6/init-adduser/branding" \
    "! grep -qi linuxserver $s6/init-adduser/branding" \
    "test -f $s6/user/contents.d/init-adduser" \
    "test -f $s6/user/contents.d/init-amneziawg-module" \
    "test -f $s6/user/contents.d/init-amneziawg-config" \
    "test -f $s6/user/contents.d/svc-dnsmasq" \
    "test -f $s6/user/contents.d/svc-amneziawg" \
    "test -x $s6/init-adduser/run" \
    "test -x $s6/init-amneziawg-module/run" \
    "test -x $s6/init-amneziawg-config/run" \
    "test -x $s6/svc-dnsmasq/run" \
    "test -x $s6/svc-amneziawg/run" \
    "test -x $s6/svc-amneziawg/finish" \
    "! grep -q PEERS $s6/svc-dnsmasq/run $s6/svc-amneziawg/run $s6/svc-amneziawg/finish" \
    "test ! -e $s6/init-amneziawg-confs"

in_image "Service types and dependency chain" \
    "test \"\$(cat $s6/init-adduser/type)\" = oneshot" \
    "test \"\$(cat $s6/init-amneziawg-module/type)\" = oneshot" \
    "test \"\$(cat $s6/init-amneziawg-config/type)\" = oneshot" \
    "test \"\$(cat $s6/svc-dnsmasq/type)\" = longrun" \
    "test \"\$(cat $s6/svc-amneziawg/type)\" = oneshot" \
    "test -f $s6/init-amneziawg-module/dependencies.d/init-adduser" \
    "test -f $s6/init-amneziawg-config/dependencies.d/init-amneziawg-module" \
    "test -f $s6/svc-dnsmasq/dependencies.d/init-amneziawg-config" \
    "test -f $s6/svc-amneziawg/dependencies.d/init-amneziawg-config" \
    "test ! -e $s6/svc-amneziawg/dependencies.d/svc-dnsmasq"

# Plain Alpine with Alpine's s6-overlay package. awgctl replaced the shell
# config scripts, the templates, qrencode and openresolv.
in_image "Runtime base" \
    'apk info -e s6-overlay >/dev/null' \
    'test -x /init' \
    'grep -q "^s6: s6-[0-9]" /build_version' \
    '! test -e /docker-mods && ! test -e /usr/bin/lsiown && ! test -e /lsiopy' \
    '! test -e /app && ! test -e /defaults' \
    '! apk info -e openresolv >/dev/null && ! command -v resolvconf' \
    '! command -v qrencode' \
    '! grep -q "src_valid_mark) != 1" /usr/bin/awg-quick' \
    'command -v bash usermod groupmod flock dnsmasq >/dev/null' \
    'id dnsmasq >/dev/null' \
    'test "$(id -u abc):$(id -g abc)" = 911:911' \
    'command -v nft >/dev/null'

section "Image config"
[[ "$(engine image inspect "$image" --format '{{json .Config.Entrypoint}}')" == '["/init"]' ]] \
    || { echo "  FAIL - entrypoint is not /init"; exit 1; }
[[ "$(engine image inspect "$image" --format '{{json .Config.Cmd}}')" == null ]] \
    || { echo "  FAIL - CMD must be empty (/init stops the container when CMD exits)"; exit 1; }
grep -q awgctl <<<"$(engine image inspect "$image" --format '{{json .Config.Healthcheck}}')" \
    || { echo "  FAIL - the health check does not run awgctl health"; exit 1; }
image_env=$(engine image inspect "$image" --format '{{range .Config.Env}}{{println .}}{{end}}')
grep -qx 'S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0' <<<"$image_env" \
    || { echo "  FAIL - S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 is missing"; exit 1; }
if [[ -n "${BUILD_DATE:-}" ]]; then
    build_version=$(engine run --rm --entrypoint cat "$image" /build_version)
    grep -qx "Build-date: ${BUILD_DATE}" <<<"$build_version" \
        || { echo "  FAIL - /build_version does not have Build-date: ${BUILD_DATE}"; echo "$build_version"; exit 1; }
fi
ok "entrypoint /init, no CMD, health check awgctl health"
passed "Image config"

# ----------------------------------------------------------------------------
# First start on an empty volume, without /dev/net/tun: the DB is created, and
# the tunnel comes up only if this host has the amneziawg kernel module.
# ----------------------------------------------------------------------------
# start ARGS...: (re)create the container on the shared volume and wait until
# awgctl init has finished.
start() {
    engine rm -f "$container" >/dev/null 2>&1 || true
    engine run -d --name "$container" --cap-add NET_ADMIN -v "$volume":/config "$@" "$image" >/dev/null
    for _ in $(seq 1 60); do
        logs_have 'Config initialization finished' && return 0
        sleep 1
    done
    fail "init did not finish ($*)"
}
# wait_tunnel: until svc-amneziawg has finished (up, or all attempts failed).
wait_tunnel() {
    for _ in $(seq 1 45); do
        logs_have 'Tunnel wg0 is up' "${1:-$container}" && return 0
        logs_have 'Tunnel wg0 is down' "${1:-$container}" && return 0
        sleep 1
    done
    fail "svc-amneziawg never finished"
}
healthy() {
    for _ in $(seq 1 20); do
        engine exec "${1:-$container}" awgctl health >/dev/null 2>&1 && return 0
        sleep 1
    done
    return 1
}

section "First start"
start -e SERVERURL=ci.example.com -e INTERNAL_SUBNET=10.57.57.0 -e PUID=1234 -e PGID=2345
logs_have 'Created /config/amneziawg.db (AWG 3.1)' || fail "the DB was not created with AWG 3.1"
logs_have 'No peers yet' || fail "a new server must start with no peers"
[[ "$(dx stat -c '%a %u:%g' /config/amneziawg.db)" == "600 1234:2345" ]] || fail "DB mode/owner is not 600 PUID:PGID"
[[ -z "$(dx sh -c 'ls -A /config | grep -v -e "^amneziawg.db$" || true')" ]] || fail "unexpected files in /config: $(dx ls -A /config)"
[[ -z "$(dx sh -c 'ls /config/*-wal 2>/dev/null')" ]] || fail "a -wal file exists"
[[ "$(dx stat -c %a /run/amneziawg/wg0.conf)" == 600 ]] || fail "wg0.conf is not mode 600"
dx grep -qx 'Address = 10.57.57.1/24' /run/amneziawg/wg0.conf || fail "wg0.conf has the wrong address"
logs_have '▸ uid:gid  1234:2345' || fail "the banner does not show PUID/PGID"
box_widths=$(engine logs "$container" 2>&1 | sed -n '/^╭/,/^╰/p' |
    python3 -c 'import sys; print(sorted({len(l) for l in sys.stdin.buffer.read().decode().splitlines()}))')
[[ "$box_widths" == "[50]" ]] || fail "banner box lines are not all 50 columns wide: $box_widths"
order=$(engine logs "$container" 2>&1 | grep -nE '▸ repo |Config initialization finished|Activating tunnel wg0' | cut -d: -f1 | tr '\n' ' ')
read -r l_banner l_init l_tunnel <<<"$order"
(( l_banner < l_init && l_init < l_tunnel )) || fail "start order is not banner, config, tunnel (lines: $order)"
ok "DB 600 PUID:PGID, nothing else in /config, wg0.conf in /run, banner, start order"
wait_tunnel
if grep -qw wg0 <<<"$(dx awg show interfaces)"; then
    healthy || fail "unhealthy although wg0 is up"
    ok "healthy with wg0 up (kernel module on this host)"
else
    dx awgctl health >/dev/null && fail "healthy although wg0 is down"
    ok "unhealthy without a tunnel"
fi
passed "First start"

section "dnsmasq"
for _ in $(seq 1 20); do
    [[ "$(dx cat /run/dnsmasq-state 2>/dev/null)" == running ]] && dx pgrep -x dnsmasq >/dev/null && break
    sleep 1
done
[[ "$(dx cat /run/dnsmasq-state 2>/dev/null)" == running ]] || fail "dnsmasq state is not running"
dx grep -qx 'interface=wg0' /run/dnsmasq/dnsmasq.conf || fail "dnsmasq is not limited to wg0"
dx grep -q '^listen-address' /run/dnsmasq/dnsmasq.conf && fail "listen-address answers on every interface"
[[ "$(dx cat /run/dnsmasq/address)" == 10.57.57.1 ]] || fail "/run/dnsmasq/address is not the tunnel address"
[[ "$(dx grep '^server=' /run/dnsmasq/dnsmasq.conf)" == $'server=1.1.1.1\nserver=1.0.0.1' ]] || fail "default upstreams"
dnsmasq_user=$(dx sh -c 'stat -c %U /proc/$(pgrep -x dnsmasq | head -n1)')
[[ "$dnsmasq_user" == dnsmasq ]] || fail "dnsmasq runs as ${dnsmasq_user}, expected dnsmasq (not the DB owner)"
foreign_listeners() {
    grep -vE "(127\.0\.0\.1|\[::1\]|%wg0|${1//./\\.}):53 " <<<"$2" || true
}
[[ -z "$(foreign_listeners 10.57.57.1 "$(dx ss -Hlntu 'sport = :53')")" ]] || fail "dnsmasq listens outside the tunnel"
ok "dnsmasq runs as dnsmasq, only on wg0 and loopback, default upstreams"
passed "dnsmasq"

section "awgctl"
code() { local rc=0; dx awgctl "$@" >/dev/null 2>&1 || rc=$?; echo "$rc"; }
dx awgctl peer add phone >/dev/null || fail "peer add"
dx awgctl peer add laptop --no-keepalive >/dev/null || fail "peer add laptop"
grep -q 'Endpoint = ci.example.com:51820' <<<"$(dx awgctl peer export phone)" || fail "peer export"
grep -q '█' <<<"$(dx awgctl peer export 1 --qr)" || fail "QR code"
[[ "$(code set awg.s1 20)" == 2 ]] || fail "set awg.* must be refused (exit 2)"
[[ "$(code peer rm laptop)" == 3 ]] || fail "peer rm without --yes must exit 3"
[[ "$(code set server.endpoint_host other.example.com)" == 3 ]] || fail "a reissue change without --yes must exit 3"
[[ "$(code set fw.nat_interface 'eth0;reboot')" == 1 ]] || fail "an injection string was accepted"
dx awgctl set dns.upstream 9.9.9.9 >/dev/null || fail "set dns.upstream"
dx grep -qx 'server=9.9.9.9' /run/dnsmasq/dnsmasq.conf || fail "dns.upstream was not applied"
dx awgctl peer rm laptop --yes >/dev/null || fail "peer rm"
[[ "$(dx awgctl peer list --json | grep -c '"name"')" == 1 ]] || fail "peer list after rm"
ok "peer add/export/rm, exit codes 2 and 3, dns.upstream applied live"
passed "awgctl"

section "Restart and drift"
gen=$(dx cat /run/amneziawg/state.json | grep committed)
start -e SERVERURL=ci.example.com -e SERVERPORT=4443 -e PEERS=3 -e PUID=1234 -e PGID=2345
logs_have 'SERVERPORT=4443 differs from the saved server.endpoint_port' || fail "no drift warning"
logs_have 'PEERS is not supported by this image' || fail "no PEERS warning"
[[ "$(dx cat /run/amneziawg/state.json | grep committed)" == "$gen" ]] || fail "a restart changed the DB"
[[ "$(dx awgctl get server.endpoint_port)" == 51820 ]] || fail "an env change after the first start was applied"
grep -q 'phone' <<<"$(dx awgctl peer list)" || fail "the peer is gone after a restart"
unowned=$(dx find /config ! -type l \( ! -user 1234 -o ! -group 2345 \))
[[ -z "$unowned" ]] || fail "not owned by PUID:PGID: $unowned"
tunnel_was_up=false
logs_have 'Tunnel wg0 is up' && tunnel_was_up=true
engine stop -t 20 "$container" >/dev/null
[[ "$(engine inspect "$container" --format '{{.State.ExitCode}}')" == 0 ]] || fail "stop did not exit 0"
if [[ "$tunnel_was_up" == true ]]; then
    logs_have '**** Tunnel wg0 is down ****' || fail "the finish script did not take wg0 down"
fi
ok "env is read only on the first start, state kept, ownership kept, clean stop"
# A PUID/PGID change re-owns the volume instead of stopping the container.
start -e PUID=1500 -e PGID=1500
unowned=$(dx find /config ! -type l \( ! -user 1500 -o ! -group 1500 \))
[[ -z "$unowned" ]] || fail "not owned by the new PUID:PGID: $unowned"
ok "a new PUID/PGID takes over the volume"
passed "Restart and drift"

# ----------------------------------------------------------------------------
# Fatal start-up errors stop the container with exit code 1, so the restart
# policy tries again instead of leaving an idle container.
# ----------------------------------------------------------------------------
section "Fatal start-up errors"
stopped_with_1() {
    for _ in $(seq 1 60); do
        [[ "$(engine inspect "$container" --format '{{.State.Running}}')" == false ]] && break
        sleep 1
    done
    [[ "$(engine inspect "$container" --format '{{.State.Running}}')" == false ]] || fail "$1: the container kept running"
    [[ "$(engine inspect "$container" --format '{{.State.ExitCode}}')" == 1 ]] || fail "$1: exit code is not 1"
}
fresh() {
    engine rm -f "$container" >/dev/null 2>&1 || true
    engine volume rm -f "$volume" >/dev/null 2>&1 || true
    engine volume create "$volume" >/dev/null
}
for ids in 'PUID=abc PGID=1000' 'PUID=1000 PGID=99999999999' 'PUID=0 PGID=1000'; do
    read -r id_u id_g <<<"$ids"
    fresh
    engine run -d --name "$container" --cap-add NET_ADMIN -v "$volume":/config \
        -e SERVERURL=ci.example.com -e "$id_u" -e "$id_g" "$image" >/dev/null
    stopped_with_1 "$ids"
    logs_have "Config initialization finished" && fail "$ids: init ran"
done
ok "an invalid PUID/PGID stops the container before init"

fresh
engine run --rm --entrypoint mkdir -v "$volume":/config "$image" -p /config/wg_confs
engine run -d --name "$container" --cap-add NET_ADMIN -v "$volume":/config -e SERVERURL=ci.example.com "$image" >/dev/null
stopped_with_1 "old volume"
logs_have 'import-legacy' || fail "no import-legacy hint for an old volume"
[[ -z "$(engine run --rm --entrypoint sh -v "$volume":/config "$image" -c 'ls /config/amneziawg.db 2>/dev/null')" ]] \
    || fail "a DB was created over an old volume"
ok "an old volume without a DB stops the container with an import-legacy hint"

fresh
engine run -d --name "$container" --network none --cap-add NET_ADMIN -v "$volume":/config "$image" >/dev/null
stopped_with_1 "no SERVERURL, no network"
logs_have 'could not be detected' || fail "no detection error"
[[ -z "$(engine run --rm --entrypoint sh -v "$volume":/config "$image" -c 'ls -A /config')" ]] \
    || fail "a failed seed left files in /config"
ok "a failed public IP detection (after retries) stops the container and writes nothing"
passed "Fatal start-up errors"

# ----------------------------------------------------------------------------
# import-legacy: a volume written by the last shell release keeps its keys and
# peer confs.
# ----------------------------------------------------------------------------
section "Import of an old volume"
engine run -d --name "$container-old" --cap-add NET_ADMIN -v "$volume-old":/config \
    -e PEERS=1,laptop -e SERVERURL=old.example.com -e PUID=1000 -e PGID=1000 "$legacy_image" >/dev/null
for _ in $(seq 1 60); do
    logs_have 'Config initialization finished' "$container-old" && break
    sleep 1
done
logs_have 'Config initialization finished' "$container-old" || fail "the old image did not start"
old_conf=$(engine exec "$container-old" cat /config/peer_laptop/peer_laptop.conf)
engine rm -f "$container-old" >/dev/null
report=$(engine run --rm --entrypoint awgctl -v "$volume-old":/config "$image" import-legacy /config --dry-run)
grep -q 'every peer conf is unchanged' <<<"$report" || { echo "$report"; fail "dry run: peer confs would change"; }
engine run --rm --entrypoint awgctl -v "$volume-old":/config "$image" import-legacy /config >/dev/null \
    || fail "import-legacy failed"
engine rm -f "$container" >/dev/null 2>&1 || true
engine run -d --name "$container" --cap-add NET_ADMIN -v "$volume-old":/config -e PUID=1000 -e PGID=1000 "$image" >/dev/null
for _ in $(seq 1 60); do
    logs_have 'Config initialization finished' && break
    sleep 1
done
logs_have 'Config initialization finished' || fail "the new image did not start on the imported volume"
new_conf=$(dx awgctl peer export laptop)
for k in PrivateKey PublicKey PresharedKey Endpoint H1 S1 I1 HeaderProtectionKey; do
    [[ "$(grep "^$k = " <<<"$old_conf")" == "$(grep "^$k = " <<<"$new_conf")" ]] || fail "the import changed $k of laptop"
done
dx sh -c 'ls -d /config/legacy-*' >/dev/null || fail "the old files were not kept in legacy-*"
ok "keys, endpoint and AWG values of each peer are unchanged after the import"
engine rm -f "$container" >/dev/null
passed "Import of an old volume"

# ----------------------------------------------------------------------------
# End to end: a server, a peer container (the same image running awg-quick on
# an exported conf), and a fake upstream resolver on a private network.
# Needs /dev/net/tun; required in CI, skipped locally without it.
# ----------------------------------------------------------------------------
section "End to end"
if [[ ! -c /dev/net/tun ]]; then
    if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
        echo "FAIL - /dev/net/tun is missing on the runner"; exit 1
    fi
    echo "  skip - no /dev/net/tun on this host"
    echo "- End to end: skipped (no /dev/net/tun)" >> "$summary"
else
    srv="$container-srv"; cli="$container-cli"; up="$container-up"
    e2e_fail() {
        echo "FAIL - $1"
        for c in "$srv" "$cli"; do echo "--- $c"; engine logs "$c" 2>&1 | tail -n 40; done
        exit 1
    }
    engine network create --subnet 10.199.53.0/24 "$network" >/dev/null
    engine run -d --name "$up" --network "$network" --ip 10.199.53.53 --entrypoint dnsmasq "$image" \
        -k --no-resolv --no-hosts --user=root --log-facility=- --address=/upstream.test/192.0.2.53 >/dev/null
    engine run -d --name "$srv" --network "$network" --ip 10.199.53.10 \
        --cap-add NET_ADMIN --device /dev/net/tun -v "$volume-srv":/config \
        -e SERVERURL=10.199.53.10 -e INTERNAL_SUBNET=10.58.58.0 -e DNS_UPSTREAM=10.199.53.53 "$image" >/dev/null
    wait_tunnel "$srv"
    logs_have 'Tunnel wg0 is up' "$srv" || e2e_fail "server tunnel did not come up"
    healthy "$srv" || e2e_fail "server with zero peers is not healthy"
    engine exec "$srv" awgctl peer add cli >/dev/null || e2e_fail "peer add"
    # No openresolv in the image: drop the DNS line and query the tunnel
    # address directly. A split tunnel needs no src_valid_mark.
    engine exec "$srv" awgctl peer export cli | sed -e '/^DNS = /d' \
        -e 's|^AllowedIPs = .*|AllowedIPs = 10.58.58.0/24|' > "$tmpdir/cli.conf"
    engine run -d --name "$cli" --network "$network" --cap-add NET_ADMIN --device /dev/net/tun \
        -v "$tmpdir":/t:ro --entrypoint sh "$image" -c 'cp /t/cli.conf /tmp/wg0.conf && awg-quick up /tmp/wg0.conf && sleep infinity' >/dev/null
    for _ in $(seq 1 20); do
        engine exec "$cli" ping -c1 -W2 10.58.58.1 >/dev/null 2>&1 && break
        sleep 1
    done
    engine exec "$cli" ping -c1 -W3 10.58.58.1 >/dev/null || e2e_fail "peer cannot reach the server through the tunnel"
    [[ "$(engine exec "$cli" awg show wg0 latest-handshakes | awk '{print $2}')" != 0 ]] || e2e_fail "no handshake"
    grep -q '192.0.2.53' <<<"$(engine exec "$cli" nslookup -type=a upstream.test 10.58.58.1)" \
        || e2e_fail "peer -> tunnel -> dnsmasq -> upstream lookup failed"
    healthy "$srv" || e2e_fail "server is not healthy with a connected peer"
    # A neighbour that routes the VPN subnet to the server reaches it, but
    # dnsmasq ignores DNS that does not arrive on wg0.
    engine run --rm --network "$network" --cap-add NET_ADMIN --entrypoint sh "$image" -c '
        ip route add 10.58.58.0/24 via 10.199.53.10 &&
        ping -c1 -W3 10.58.58.1 >/dev/null &&
        ! nslookup -type=a -timeout=2 -retry=1 health.amneziawg. 10.58.58.1 >/dev/null 2>&1
    ' || e2e_fail "a container outside the tunnel can query dnsmasq (or cannot reach the server)"
    # Live changes keep the session of the other peer.
    engine exec "$srv" awgctl peer add second >/dev/null || e2e_fail "live peer add"
    engine exec "$cli" ping -c1 -W3 10.58.58.1 >/dev/null || e2e_fail "a live peer add dropped the other peer"
    engine exec "$srv" awgctl peer disable cli >/dev/null || e2e_fail "peer disable"
    engine exec "$cli" ping -c1 -W3 10.58.58.1 >/dev/null 2>&1 && e2e_fail "a disabled peer still reaches the server"
    engine exec "$srv" awgctl peer enable cli >/dev/null || e2e_fail "peer enable"
    for _ in $(seq 1 10); do
        engine exec "$cli" ping -c1 -W2 10.58.58.1 >/dev/null 2>&1 && break
        sleep 1
    done
    engine exec "$cli" ping -c1 -W3 10.58.58.1 >/dev/null || e2e_fail "an enabled peer does not reach the server again"
    ok "handshake, DNS through the tunnel to the upstream, DNS closed to non-peers, live peer changes"
    passed "End to end"
fi

echo "All smoke tests passed!" >> "$summary"
echo "Smoke tests passed!"
