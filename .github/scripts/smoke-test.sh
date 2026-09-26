#!/usr/bin/env bash
# Smoke-test a locally built image (no tunnel: CI runners have no /dev/net/tun).
#
# Usage: smoke-test.sh <image>
#
# Every check must fail the script on its own. Do not write `test ... && echo`:
# a failing left side of && is exempt from errexit, so only the last line of a
# group would count.
set -euo pipefail

image="${1:?usage: smoke-test.sh <image>}"
summary="${GITHUB_STEP_SUMMARY:-/dev/null}"
container="awgci-$$"
volume="awgci-vol-$$"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; docker volume rm -f "$volume" >/dev/null 2>&1 || true' EXIT

# Runs a list of checks inside the image. Each argument is one shell
# condition; the first one that fails stops the group with a non-zero exit.
in_image() {
    local title=$1
    shift
    echo "### ${title}"
    echo "### ${title}" >> "$summary"
    docker run --rm --entrypoint /bin/sh "$image" -c '
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

echo "## Smoke tests" >> "$summary"

in_image "Binaries" \
    'test -x /usr/bin/awg' \
    'test -x /usr/bin/awg-quick' \
    'test -x /usr/bin/amneziawg-go' \
    'test -L /usr/bin/wg' \
    'test -L /usr/bin/wg-quick' \
    'test -L /etc/wireguard'

s6=/etc/s6-overlay/s6-rc.d
in_image "s6-overlay services and branding" \
    "test -f $s6/init-adduser/branding" \
    "test -f $s6/user/contents.d/init-amneziawg-module" \
    "test -f $s6/user/contents.d/init-amneziawg-confs" \
    "test -f $s6/user/contents.d/svc-unbound" \
    "test -f $s6/user/contents.d/svc-amneziawg" \
    "test -x $s6/init-amneziawg-module/run" \
    "test -x $s6/init-amneziawg-confs/run" \
    "test -x $s6/svc-unbound/run" \
    "test -x $s6/svc-amneziawg/run" \
    "test -x $s6/svc-amneziawg/finish"

in_image "Service types" \
    "test \"\$(cat $s6/init-amneziawg-module/type)\" = oneshot" \
    "test \"\$(cat $s6/init-amneziawg-confs/type)\" = oneshot" \
    "test \"\$(cat $s6/svc-unbound/type)\" = longrun" \
    "test \"\$(cat $s6/svc-amneziawg/type)\" = oneshot"

in_image "Dependency chain" \
    "test -f $s6/init-amneziawg-module/dependencies.d/init-config" \
    "test -f $s6/init-amneziawg-confs/dependencies.d/init-amneziawg-module" \
    "test -f $s6/svc-unbound/dependencies.d/init-amneziawg-confs" \
    "test -f $s6/svc-unbound/dependencies.d/init-services" \
    "test -f $s6/svc-amneziawg/dependencies.d/init-amneziawg-confs" \
    "test -f $s6/svc-amneziawg/dependencies.d/svc-unbound"

in_image "Scripts and defaults" \
    'test -x /app/show-peer' \
    'test -x /app/healthcheck' \
    'test -f /defaults/server.conf' \
    'test -f /defaults/peer.conf' \
    'test -s /build_version'

in_image "Unbound" \
    'command -v unbound' \
    'command -v unbound-checkconf' \
    'test -f /defaults/unbound.conf'

# Generate a real AWG 3.1 config set and check the keys land in the right
# sections. Tunnel activation is expected to fail on a CI runner; the confs are
# written by init-amneziawg-confs before that, and s6 keeps the container up
# either way.
wait_for_peer() {
    # Wait on the .png, not the .conf: generate_confs renders into a staging
    # directory and moves the set into place with wg0.conf first and the pngs
    # last, so once the png exists every conf is final.
    local png=$1 label=$2
    for _ in $(seq 1 45); do
        docker exec "$container" test -f "$png" 2>/dev/null && return 0
        sleep 2
    done
    echo "${label}: peer confs were never generated"
    docker logs "$container"
    exit 1
}

# Every key must sit in [Interface], i.e. above the first [Peer] line.
check_iface_key() {
    local conf=$1 key=$2
    docker exec "$container" awk -v k="$key" '
        /^\[Peer\]/ { exit }
        $1 == k { found = 1 }
        END { exit(found ? 0 : 1) }
    ' "$conf" && return 0
    echo "AWG 3.1: $key missing from [Interface] of $conf"
    docker exec "$container" cat "$conf"
    exit 1
}

echo "### AWG 3.1 config generation"
echo "### AWG 3.1 config generation" >> "$summary"
docker run -d --name "$container" --cap-add NET_ADMIN \
    -e PEERS=2 -e SERVERURL=ci.example.com -e INTERNAL_SUBNET=10.55.55.0 \
    -e AWG_VERSION=3.1 -e AWG_DISABLE_COOKIES=on \
    "$image" >/dev/null
wait_for_peer /config/peer2/peer2.png "AWG 3.1"

for conf in /config/wg_confs/wg0.conf /config/peer1/peer1.conf /config/peer2/peer2.conf; do
    for key in RandomTrailers DisableCookies HeaderProtectionKey; do
        check_iface_key "$conf" "$key"
    done
    echo "  ok   - $conf: 3.1 switches + header protection in [Interface]"
done

# HeaderProtectionKey must be identical everywhere; the switches must be on.
hpk_server=$(docker exec "$container" awk '/^HeaderProtectionKey/ {print $3; exit}' /config/wg_confs/wg0.conf)
hpk_peer=$(docker exec "$container" awk '/^HeaderProtectionKey/ {print $3; exit}' /config/peer1/peer1.conf)
if [[ -z "$hpk_server" || "$hpk_server" != "$hpk_peer" ]]; then
    echo "AWG 3.1: HeaderProtectionKey differs between server and peer"
    exit 1
fi
docker exec "$container" grep -q '^RandomTrailers = on$' /config/peer1/peer1.conf \
    || { echo "AWG 3.1: RandomTrailers not set to on"; exit 1; }
docker exec "$container" grep -q '^DisableCookies = on$' /config/peer1/peer1.conf \
    || { echo "AWG 3.1: DisableCookies not set to on"; exit 1; }
echo "- AWG 3.1 config generation: OK" >> "$summary"

# Default 2.0 must not emit the 3.x keys.
echo "### Default AWG 2.0 config generation"
echo "### Default AWG 2.0 config generation" >> "$summary"
docker rm -f "$container" >/dev/null
docker run -d --name "$container" --cap-add NET_ADMIN \
    -e PEERS=1 -e SERVERURL=ci.example.com -e INTERNAL_SUBNET=10.56.56.0 \
    "$image" >/dev/null
wait_for_peer /config/peer1/peer1.png "default 2.0"
if docker exec "$container" grep -qE '^(RandomTrailers|DisableCookies|HeaderProtectionKey)' /config/peer1/peer1.conf; then
    echo "default AWG_VERSION=2.0 leaked 3.x keys into the peer conf"
    docker exec "$container" cat /config/peer1/peer1.conf
    exit 1
fi
echo "  ok   - default 2.0: no 3.x keys emitted"
echo "- Default AWG 2.0 config generation: OK" >> "$summary"

# The healthcheck and s6 must agree with whether wg0 actually came up. On a
# runner it normally does not (no kernel module, no /dev/net/tun), but check
# both outcomes rather than assume one.
echo "### Health check"
echo "### Health check" >> "$summary"
for _ in $(seq 1 30); do
    grep -qE 'All tunnels are now (active|down)' <<<"$(docker logs "$container" 2>&1)" && break
    sleep 2
done
grep -qE 'All tunnels are now (active|down)' <<<"$(docker logs "$container" 2>&1)" \
    || { echo "svc-amneziawg never finished"; docker logs "$container"; exit 1; }
if grep -qw wg0 <<<"$(docker exec "$container" awg show interfaces)"; then
    docker exec "$container" /app/healthcheck \
        || { echo "healthcheck failed although wg0 is up"; docker logs "$container"; exit 1; }
    echo "  ok   - healthy with wg0 up"
else
    if docker exec "$container" /app/healthcheck; then
        echo "healthcheck passed although wg0 is down"; docker logs "$container"; exit 1
    fi
    if grep -qx svc-amneziawg <<<"$(docker exec "$container" s6-rc -a list)"; then
        echo "svc-amneziawg reported up although its tunnel failed"; docker logs "$container"; exit 1
    fi
    echo "  ok   - unhealthy and svc-amneziawg down without a tunnel"
fi
echo "- Health check: OK" >> "$summary"

# Checked here rather than with in_image: the seeded config needs the
# /config/unbound directory and root.key that init creates.
echo "### Unbound at runtime"
echo "### Unbound at runtime" >> "$summary"
docker exec "$container" unbound-checkconf /config/unbound/unbound.conf
docker exec "$container" pgrep -x unbound >/dev/null \
    || { echo "unbound is not running"; docker logs "$container"; exit 1; }
unbound_user=$(docker exec "$container" sh -c 'stat -c %U /proc/$(pgrep -x unbound | head -n1)')
[[ "$unbound_user" == abc ]] \
    || { echo "unbound runs as ${unbound_user}, expected abc"; exit 1; }
listeners=$(docker exec "$container" ss -Hlnu 'sport = :53')
if grep -q '0.0.0.0:53' <<<"$listeners" || ! grep -q '10.56.56.1:53' <<<"$listeners"; then
    echo "unbound should listen on 127.0.0.1 and the tunnel address only:"; echo "$listeners"; exit 1
fi
echo "  ok   - unbound config valid, running as abc on loopback + tunnel address"
echo "- Unbound at runtime: OK" >> "$summary"

# ----------------------------------------------------------------------------
# Stateful behavior: one persistent /config volume across restarts, the way a
# real deployment sees it.
# ----------------------------------------------------------------------------
echo "### Persistent state"
echo "### Persistent state" >> "$summary"

fail() {
    echo "FAIL - $1"
    docker logs "$container" 2>&1 | tail -n 60
    exit 1
}
# (Re)create the container on the shared volume with the given extra args and
# wait until init-amneziawg-confs has finished.
start() {
    docker rm -f "$container" >/dev/null 2>&1 || true
    docker run -d --name "$container" --cap-add NET_ADMIN -v "$volume":/config \
        -e SERVERURL=ci.example.com -e INTERNAL_SUBNET=10.57.57.0 "$@" "$image" >/dev/null
    for _ in $(seq 1 60); do
        grep -q 'Config initialization finished' <<<"$(docker logs "$container" 2>&1)" && return 0
        sleep 1
    done
    fail "init did not finish ($*)"
}
logs_have() { grep -qF -- "$1" <<<"$(docker logs "$container" 2>&1)"; }
wg0_sum() { docker exec "$container" sha256sum /config/wg_confs/wg0.conf | cut -d' ' -f1; }
iface_value() { docker exec "$container" awk -v k="$2" '$1 == k { print $3; exit }' "$1"; }
ok() { echo "  ok   - $1"; }

start -e PEERS=2
for f in /config/server/awg_params /config/server/privatekey-server /config/peer1/peer1.conf \
         /config/peer1/peer1.png /config/wg_confs/wg0.conf /config/.donoteditthisfile; do
    mode=$(docker exec "$container" stat -c %a "$f")
    [[ "$mode" == 600 ]] || fail "$f has mode $mode, expected 600"
done
ok "secrets are mode 600"
logs_have "QR code (conf file" && fail "QR codes (private keys) were logged without LOG_CONFS=true"
ok "no QR codes in the log by default"

sum=$(wg0_sum)
start -e PEERS=2
logs_have "No changes to parameters" || fail "restart with the same settings regenerated"
[[ "$(wg0_sum)" == "$sum" ]] || fail "wg0.conf changed on an unchanged restart"
ok "unchanged restart keeps the configs"

start -e PEERS=2 -e AWG_VERSION=1.5
logs_have "AWG_VERSION changed from 2.0 to 1.5" || fail "2.0 -> 1.5 not detected"
[[ "$(iface_value /config/peer1/peer1.conf H1)" =~ ^[0-9]+$ ]] || fail "1.5 kept an H range"
[[ "$(iface_value /config/peer1/peer1.conf S3)" == 0 && "$(iface_value /config/peer1/peer1.conf S4)" == 0 ]] \
    || fail "1.5 kept nonzero S3/S4"
docker exec "$container" grep -q '^I1' /config/peer1/peer1.conf && fail "1.5 kept I1"
ok "2.0 -> 1.5 regenerates 1.5-shaped parameters"

start -e PEERS=2 -e AWG_VERSION=2.0
[[ "$(iface_value /config/peer1/peer1.conf H1)" == *-* ]] || fail "2.0 kept an integer H"
docker exec "$container" grep -q '^I1' /config/peer1/peer1.conf || fail "2.0 has no I1"
ok "1.5 -> 2.0 regenerates 2.0-shaped parameters"

sum=$(wg0_sum)
start -e PEERS=2 -e AWG_VERSION=3.0 -e AWG_S1=8
logs_have "must be >= 12" || fail "S1 < 12 under 3.0 not rejected"
logs_have "Config generation failed" || fail "invalid 3.0 params did not stop generation"
[[ "$(wg0_sum)" == "$sum" ]] || fail "invalid 3.0 params changed wg0.conf"
ok "a pinned value awg would reject stops generation"

start -e PEERS=2 -e SERVER_ALLOWEDIPS_PEER_1=192.168.77.0/24
docker exec "$container" grep -qx 'AllowedIPs = 10.57.57.2/32,192.168.77.0/24' /config/wg_confs/wg0.conf \
    || fail "SERVER_ALLOWEDIPS_PEER_1 change was not applied"
ok "a SERVER_ALLOWEDIPS_PEER_* change regenerates"

sum=$(wg0_sum)
start -e PEERS=1,1 -e SERVER_ALLOWEDIPS_PEER_1=192.168.77.0/24
logs_have "listed more than once" || fail "duplicate peer not rejected"
[[ "$(wg0_sum)" == "$sum" ]] || fail "duplicate peers changed wg0.conf"
ok "duplicate peers are rejected"

start -e PEERS=1 -e SERVER_ALLOWEDIPS_PEER_1=192.168.77.0/24
docker exec "$container" test ! -e /config/peer2 || fail "removed peer2 was not archived"
docker exec "$container" sh -c 'ls -d /config/removed_peers/peer2-*' >/dev/null || fail "peer2 is not in removed_peers"
docker exec "$container" grep -qx '# peer2' /config/wg_confs/wg0.conf && fail "peer2 still in wg0.conf"
start -e PEERS=1,new -e SERVER_ALLOWEDIPS_PEER_1=192.168.77.0/24
[[ "$(iface_value /config/peer_new/peer_new.conf Address)" == 10.57.57.3 ]] \
    || fail "the address of the removed peer was not reused"
ok "removed peers are archived and free their address"

run_new() { start -e PEERS=1,new -e SERVER_ALLOWEDIPS_PEER_1=192.168.77.0/24 "$@"; }
sum=$(wg0_sum)
run_new -e 'SERVERURL=bad;host'
logs_have 'is not a valid host name' || fail "invalid SERVERURL accepted"
[[ "$(wg0_sum)" == "$sum" ]] || fail "invalid SERVERURL changed wg0.conf"
ok "invalid SERVERURL is rejected"

docker exec "$container" sh -c 'echo "# ci" >> /config/templates/server.conf && chmod 666 /config/templates/server.conf'
run_new
logs_have "is world-writable" || fail "world-writable template was used"
[[ "$(wg0_sum)" == "$sum" ]] || fail "world-writable template changed wg0.conf"
ok "world-writable templates are refused"

docker exec "$container" sh -c 'chmod 600 /config/templates/server.conf && echo "Bogus = 1" >> /config/templates/server.conf'
run_new
logs_have "rejected by awg" || fail "template with an unknown key passed validation"
[[ "$(wg0_sum)" == "$sum" ]] || fail "rejected template changed wg0.conf"
ok "configs awg cannot parse are never installed"
echo "- Persistent state: OK" >> "$summary"

echo "All smoke tests passed!" >> "$summary"
echo "Smoke tests passed!"
