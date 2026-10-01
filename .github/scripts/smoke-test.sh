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
network="awgci-net-$$"
tmpdir=$(mktemp -d)
cleanup() {
    local c
    for c in "$container" "$container-up" "$container-srv" "$container-cli"; do
        docker rm -f "$c" >/dev/null 2>&1 || true
    done
    docker volume rm -f "$volume" "$volume-srv" "$volume-cli" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT

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
    '! test -e /usr/bin/wg && ! test -L /usr/bin/wg' \
    '! test -e /usr/bin/wg-quick && ! test -L /usr/bin/wg-quick' \
    '! test -e /etc/wireguard && ! test -L /etc/wireguard'

s6=/etc/s6-overlay/s6-rc.d
in_image "s6-overlay services and branding" \
    "test -f $s6/init-adduser/branding" \
    "test -f $s6/user/contents.d/init-amneziawg-module" \
    "test -f $s6/user/contents.d/init-amneziawg-confs" \
    "test -f $s6/user/contents.d/svc-dnsmasq" \
    "test -f $s6/user/contents.d/svc-amneziawg" \
    "test -x $s6/init-amneziawg-module/run" \
    "test -x $s6/init-amneziawg-confs/run" \
    "test -x $s6/svc-dnsmasq/run" \
    "test -x $s6/svc-amneziawg/run" \
    "test -x $s6/svc-amneziawg/finish"

in_image "Service types" \
    "test \"\$(cat $s6/init-amneziawg-module/type)\" = oneshot" \
    "test \"\$(cat $s6/init-amneziawg-confs/type)\" = oneshot" \
    "test \"\$(cat $s6/svc-dnsmasq/type)\" = longrun" \
    "test \"\$(cat $s6/svc-amneziawg/type)\" = oneshot"

in_image "Dependency chain" \
    "test -f $s6/init-amneziawg-module/dependencies.d/init-config" \
    "test -f $s6/init-amneziawg-confs/dependencies.d/init-amneziawg-module" \
    "test -f $s6/svc-dnsmasq/dependencies.d/init-amneziawg-confs" \
    "test -f $s6/svc-dnsmasq/dependencies.d/init-services" \
    "test -f $s6/svc-amneziawg/dependencies.d/init-amneziawg-confs" \
    "test ! -e $s6/svc-amneziawg/dependencies.d/svc-dnsmasq" \
    "test ! -e $s6/svc-unbound"

in_image "Scripts and defaults" \
    'test -x /app/show-peer' \
    'test -x /app/healthcheck' \
    'test -f /defaults/server.conf' \
    'test -f /defaults/peer.conf' \
    'test -f /defaults/dnsmasq.conf' \
    'test -s /build_version'

in_image "DNS forwarder" \
    'command -v dnsmasq' \
    '! command -v unbound' \
    'test ! -e /defaults/unbound.conf'

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
    # dnsmasq binds the tunnel address when it appears, which can lag the
    # "active" log line by a moment.
    for _ in $(seq 1 10); do
        docker exec "$container" /app/healthcheck >/dev/null && break
        sleep 1
    done
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

# The tunnel is up only if this host has the amneziawg kernel module (no
# /dev/net/tun is passed in); dnsmasq must be configured the same either way.
echo "### dnsmasq at runtime"
echo "### dnsmasq at runtime" >> "$summary"
dx() { docker exec "$container" "$@"; }
# Listeners on port 53 other than loopback and the given tunnel address.
# interface=wg0 makes dnsmasq add loopback, which only this container reaches.
foreign_listeners() {
    grep -vE "(127\.0\.0\.1|\[::1\]|${1//./\\.}):53 " <<<"$2" || true
}
for _ in $(seq 1 20); do
    [[ "$(dx cat /run/dnsmasq-state 2>/dev/null)" == running ]] && dx pgrep -x dnsmasq >/dev/null && break
    sleep 1
done
[[ "$(dx cat /run/dnsmasq-state 2>/dev/null)" == running ]] \
    || { echo "dnsmasq state is not running"; docker logs "$container"; exit 1; }
{ dx grep -qx 'interface=wg0' /run/dnsmasq/base.conf && ! dx grep -q '^listen-address' /run/dnsmasq/base.conf; } \
    || { echo "base.conf must restrict dnsmasq to wg0 (interface=, no listen-address=)"; dx cat /run/dnsmasq/base.conf; exit 1; }
[[ "$(dx cat /run/dnsmasq/address)" == 10.56.56.1 ]] || { echo "/run/dnsmasq/address is not the tunnel address"; exit 1; }
servers=$(dx grep '^server=' /config/dnsmasq/dnsmasq.conf)
[[ "$servers" == $'server=1.1.1.1\nserver=1.0.0.1' ]] \
    || { echo "default upstreams should be 1.1.1.1 then 1.0.0.1, got:"; echo "$servers"; exit 1; }
dx dnsmasq --test -C /run/dnsmasq/base.conf >/dev/null 2>&1 \
    || { echo "dnsmasq rejects its own config"; exit 1; }
# shellcheck disable=SC2016 # expanded inside the container
dnsmasq_user=$(dx sh -c 'stat -c %U /proc/$(pgrep -x dnsmasq | head -n1)')
[[ "$dnsmasq_user" == abc ]] || { echo "dnsmasq runs as ${dnsmasq_user}, expected abc"; exit 1; }
listeners=$(dx ss -Hlntu 'sport = :53')
if [[ -n "$(foreign_listeners 10.56.56.1 "$listeners")" ]]; then
    echo "dnsmasq should listen on the tunnel address and loopback only:"; echo "$listeners"; exit 1
fi
dx grep -qx 'DNS = 10.56.56.1' /config/peer1/peer1.conf \
    || { echo "peer DNS is not the tunnel address"; dx cat /config/peer1/peer1.conf; exit 1; }
if dx iptables -S | grep -q -- 'dport 53'; then
    echo "DNS is restricted by dnsmasq's interface=, not by firewall rules:"; dx iptables -S; exit 1
fi
old_pid=$(dx pgrep -x dnsmasq)
dx s6-svc -r /run/service/svc-dnsmasq
for _ in $(seq 1 20); do
    new_pid=$(dx pgrep -x dnsmasq || true)
    [[ -n "$new_pid" && "$new_pid" != "$old_pid" ]] && break
    sleep 1
done
[[ -n "$new_pid" && "$new_pid" != "$old_pid" ]] || { echo "dnsmasq did not come back after a restart"; exit 1; }
if grep -qw wg0 <<<"$(dx awg show interfaces)"; then
    for _ in $(seq 1 10); do
        dx /app/healthcheck >/dev/null && break
        sleep 1
    done
    dx /app/healthcheck || { echo "dnsmasq does not answer on the tunnel after a service restart"; exit 1; }
fi
echo "  ok   - dnsmasq runs as abc, restricted to wg0 (tunnel address + loopback), default upstreams"
echo "- dnsmasq at runtime: OK" >> "$summary"

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

# ----------------------------------------------------------------------------
# DNS config: the same rule as the tunnel confs. /config/dnsmasq/dnsmasq.conf
# is rendered from templates/dnsmasq.conf + DNS_UPSTREAM and rewritten only
# when one of those changed; DNS and tunnel inputs never regenerate each other.
# ----------------------------------------------------------------------------
echo "### DNS config on restart"
echo "### DNS config on restart" >> "$summary"
docker exec "$container" sed -i -e '/^Bogus = 1$/d' -e '/^# ci$/d' /config/templates/server.conf
dns_sum() { docker exec "$container" sha256sum /config/dnsmasq/dnsmasq.conf | cut -d' ' -f1; }
peer_sum() { docker exec "$container" sha256sum /config/peer1/peer1.conf | cut -d' ' -f1; }
dns_servers() { docker exec "$container" grep '^server=' /config/dnsmasq/dnsmasq.conf; }
# svc-dnsmasq writes its state after init has finished, so wait for it.
dns_state() {
    local state=""
    for _ in $(seq 1 20); do
        state=$(docker exec "$container" cat /run/dnsmasq-state 2>/dev/null || true)
        [[ -n "$state" ]] && break
        sleep 1
    done
    printf '%s' "$state"
}

run_new -e 'DNS_UPSTREAM=9.9.9.9, 127.0.0.1#5335'
[[ "$(dns_servers)" == $'server=9.9.9.9\nserver=127.0.0.1#5335' ]] || fail "DNS_UPSTREAM was not rendered in order"
[[ "$(dns_state)" == running ]] || fail "dnsmasq is not running"
ok "DNS_UPSTREAM renders one server= line per entry, in order"

w=$(wg0_sum); p=$(peer_sum)
docker exec "$container" sh -c 'echo "# hand edit" >> /config/dnsmasq/dnsmasq.conf'
d=$(dns_sum)
run_new -e 'DNS_UPSTREAM=9.9.9.9, 127.0.0.1#5335'
logs_have "No changes to DNS settings" || fail "an unchanged restart re-rendered dnsmasq.conf"
[[ "$(dns_sum)" == "$d" && "$(wg0_sum)" == "$w" && "$(peer_sum)" == "$p" ]] \
    || fail "an unchanged restart rewrote a config"
ok "recreating with the same settings keeps dnsmasq.conf (hand edit included), wg0.conf and peer confs"

run_new -e DNS_UPSTREAM=9.9.9.9
logs_have "DNS settings changed (DNS_UPSTREAM)" || fail "a DNS_UPSTREAM change was not detected"
[[ "$(dns_servers)" == server=9.9.9.9 ]] || fail "a DNS_UPSTREAM change was not rendered"
docker exec "$container" grep -q '# hand edit' /config/dnsmasq/dnsmasq.conf && fail "re-render kept the hand edit"
[[ "$(wg0_sum)" == "$w" && "$(peer_sum)" == "$p" ]] || fail "a DNS_UPSTREAM change rewrote the tunnel confs"
ok "a DNS_UPSTREAM change re-renders dnsmasq.conf only"

d=$(dns_sum)
run_dns() { run_new -e DNS_UPSTREAM=9.9.9.9 -e SERVERURL=ci2.example.com "$@"; }
run_dns
docker exec "$container" grep -q '^Endpoint = ci2.example.com:' /config/peer1/peer1.conf || fail "SERVERURL change not applied"
[[ "$(dns_sum)" == "$d" ]] || fail "a SERVERURL change rewrote dnsmasq.conf"
ok "a tunnel setting change leaves dnsmasq.conf alone"

p=$(peer_sum)
docker exec "$container" sh -c 'echo "address=/edit.test/192.0.2.7" >> /config/templates/dnsmasq.conf'
run_dns
logs_have "DNS settings changed (DNS_TEMPLATE_HASH)" || fail "a dnsmasq template change was not detected"
docker exec "$container" grep -qx 'address=/edit.test/192.0.2.7' /config/dnsmasq/dnsmasq.conf \
    || fail "the template edit was not rendered"
[[ "$(peer_sum)" == "$p" ]] || fail "a dnsmasq template change rewrote the peer confs"
ok "a dnsmasq template change re-renders dnsmasq.conf only"

d=$(dns_sum)
run_dns -e DNS_UPSTREAM=dns.example
logs_have "is not an IPv4 or IPv6 address" || fail "a host name in DNS_UPSTREAM was accepted"
logs_have "DNS config generation failed" || fail "an invalid DNS_UPSTREAM did not stop DNS generation"
[[ "$(dns_sum)" == "$d" && "$(dns_state)" == running ]] || fail "an invalid DNS_UPSTREAM changed or stopped dnsmasq"
run_dns -e 'DNS_UPSTREAM=9.9.9.9#0'
logs_have "has an invalid port" || fail "port 0 in DNS_UPSTREAM was accepted"
ok "an invalid DNS_UPSTREAM keeps the previous dnsmasq.conf running"

docker exec "$container" sh -c 'echo "bogus-option-ci" >> /config/templates/dnsmasq.conf'
run_dns
logs_have "rendered dnsmasq config is not valid" || fail "a template dnsmasq rejects passed validation"
[[ "$(dns_sum)" == "$d" && "$(dns_state)" == running ]] || fail "a rejected template changed or stopped dnsmasq"
docker exec "$container" sh -c 'sed -i "s/^bogus-option-ci$/# ci/" /config/templates/dnsmasq.conf && chmod 666 /config/templates/dnsmasq.conf'
run_dns
logs_have "dnsmasq.conf is world-writable" || fail "a world-writable dnsmasq template was used"
[[ "$(dns_sum)" == "$d" ]] || fail "a world-writable dnsmasq template changed dnsmasq.conf"
docker exec "$container" chmod 600 /config/templates/dnsmasq.conf
run_dns
logs_have "DNS settings changed (DNS_TEMPLATE_HASH)" || fail "the fixed dnsmasq template was not rendered"
ok "dnsmasq templates that are rejected or world-writable are never installed"

docker exec "$container" sh -c 'echo "bogus-option-ci" >> /config/dnsmasq/dnsmasq.conf'
run_dns
[[ "$(dns_state)" == invalid ]] || fail "a broken hand edit did not mark DNS invalid"
docker exec "$container" pgrep -x dnsmasq >/dev/null && fail "dnsmasq started with a broken config"
logs_have "No valid dnsmasq config" || fail "a broken hand edit was not reported"
docker exec "$container" rm /config/dnsmasq/dnsmasq.conf
run_dns
[[ "$(dns_state)" == running ]] || fail "a deleted dnsmasq.conf was not re-rendered"
ok "a broken hand edit stops dnsmasq; deleting the file renders it again"

p=$(peer_sum)
run_dns -e PEERDNS=8.8.8.8 -e USE_DNS=false
logs_have "PEERDNS is no longer supported" || fail "PEERDNS was not reported as ignored"
logs_have "USE_DNS is no longer supported" || fail "USE_DNS was not reported as ignored"
[[ "$(peer_sum)" == "$p" && "$(dns_state)" == running ]] || fail "PEERDNS/USE_DNS still have an effect"
docker exec "$container" sed -i 's/^ORIG_PEERDNS=.*/ORIG_PEERDNS="1.1.1.1"/' /config/.donoteditthisfile
docker exec "$container" mkdir -p /config/unbound
run_dns
logs_have "settings changed (PEERDNS" || fail "a saved custom PEERDNS did not regenerate"
docker exec "$container" grep -qx 'DNS = 10.57.57.1' /config/peer1/peer1.conf || fail "peer DNS is not the tunnel address"
logs_have "/config/unbound is no longer used" || fail "a leftover /config/unbound was not reported"
ok "PEERDNS/USE_DNS are ignored; an old custom PEERDNS is replaced by the tunnel address"
echo "- DNS config on restart: OK" >> "$summary"

# ----------------------------------------------------------------------------
# Client mode: only wg_confs/wg0.conf on the volume, PEERS unset. Nothing may
# be generated and dnsmasq must not run. The tunnel cannot come up here (the
# full-tunnel conf needs src_valid_mark=1, and usually there is no tun), so the
# container must fail closed.
# ----------------------------------------------------------------------------
echo "### Client mode"
echo "### Client mode" >> "$summary"
docker exec "$container" cat /config/peer1/peer1.conf > "$tmpdir/client.conf"
docker rm -f "$container" >/dev/null
docker volume rm "$volume" >/dev/null
# put_conf VOLUME FILE: a volume holding only wg_confs/wg0.conf.
put_conf() {
    docker run --rm -i -v "$1":/config --entrypoint sh "$image" \
        -c 'mkdir -p /config/wg_confs && cat > /config/wg_confs/wg0.conf' < "$2"
}
# wait_tunnels CONTAINER: until svc-amneziawg has given its verdict.
wait_tunnels() {
    for _ in $(seq 1 45); do
        grep -qE 'All tunnels are now (active|down)' <<<"$(docker logs "$1" 2>&1)" && return 0
        sleep 1
    done
    echo "FAIL - svc-amneziawg in $1 never finished"; docker logs "$1" 2>&1 | tail -n 60; exit 1
}
put_conf "$volume" "$tmpdir/client.conf"
docker run -d --name "$container" --cap-add NET_ADMIN -v "$volume":/config "$image" >/dev/null
wait_tunnels "$container"
logs_have "Client mode selected" || fail "client mode was not selected"
for f in /config/server /config/peer1 /config/peer_new /config/.donoteditthisfile /config/dnsmasq; do
    docker exec "$container" test ! -e "$f" || fail "client mode created $f"
done
[[ "$(wg0_sum)" == "$(sha256sum < "$tmpdir/client.conf" | cut -d' ' -f1)" ]] || fail "client mode changed wg0.conf"
[[ "$(dns_state)" == disabled ]] || fail "dnsmasq state is not disabled in client mode"
docker exec "$container" pgrep -x dnsmasq >/dev/null && fail "dnsmasq runs in client mode"
[[ -z "$(docker exec "$container" ss -Hlntu 'sport = :53')" ]] || fail "something listens on port 53 in client mode"
if grep -qw wg0 <<<"$(docker exec "$container" awg show interfaces)"; then
    docker exec "$container" /app/healthcheck >/dev/null || fail "client healthcheck failed although wg0 is up"
    docker exec "$container" grep -qx 'nameserver 10.57.57.1' /etc/resolv.conf || fail "client DNS was not applied"
    ok "client mode: nothing generated, no dnsmasq, tunnel up with its DNS"
else
    docker exec "$container" /app/healthcheck >/dev/null && fail "client healthcheck passed without a tunnel"
    [[ -z "$(docker exec "$container" sh -c 'ip -4 route show default table all; ip -6 route show default table all')" ]] \
        || fail "client mode kept a default route without a tunnel"
    ok "client mode: nothing generated, no dnsmasq, fails closed without a tunnel"
fi
echo "- Client mode: OK" >> "$summary"

# ----------------------------------------------------------------------------
# End to end: a server, a client-mode container using the server's peer1.conf,
# and a fake upstream resolver on a private network (no internet needed).
# Needs /dev/net/tun; required in CI, skipped locally without it.
# ----------------------------------------------------------------------------
echo "### End to end"
echo "### End to end" >> "$summary"
if [[ ! -c /dev/net/tun ]]; then
    if [[ "${GITHUB_ACTIONS:-}" == true ]]; then
        echo "FAIL - /dev/net/tun is missing on the runner"; exit 1
    fi
    echo "  skip - no /dev/net/tun on this host"
    echo "- End to end: skipped (no /dev/net/tun)" >> "$summary"
else
    srv="$container-srv"; cli="$container-cli"; up="$container-up"
    # A raw DNS query for health.amneziawg. over TCP (length-prefixed, as DNS
    # over TCP is); prints the number of bytes answered, 0 if refused.
    # shellcheck disable=SC2016 # $1 is expanded by sh in the container
    tcp_query='printf "\000\042\022\064\001\000\000\001\000\000\000\000\000\000\006health\011amneziawg\000\000\001\000\001" | nc -w 3 "$1" 53 | wc -c'
    # peer_queries: the peer gets answers through the tunnel over UDP and TCP.
    peer_queries() {
        docker exec "$cli" nslookup -type=a health.amneziawg. 10.58.58.1 >/dev/null || e2e_fail "peer UDP query failed"
        (( $(docker exec "$cli" sh -c "$tcp_query" sh 10.58.58.1) > 2 )) || e2e_fail "peer TCP query failed"
    }
    e2e_fail() {
        echo "FAIL - $1"
        for c in "$srv" "$cli"; do echo "--- $c"; docker logs "$c" 2>&1 | tail -n 40; done
        exit 1
    }
    # healthy CONTAINER: the healthcheck passes within 20s.
    healthy() {
        for _ in $(seq 1 20); do
            docker exec "$1" /app/healthcheck >/dev/null 2>&1 && return 0
            sleep 1
        done
        return 1
    }
    run_srv() {
        docker rm -f "$srv" >/dev/null 2>&1 || true
        docker run -d --name "$srv" --network "$network" --ip 10.199.53.10 \
            --cap-add NET_ADMIN --device /dev/net/tun -v "$volume-srv":/config \
            -e PEERS=1 -e SERVERURL=10.199.53.10 -e INTERNAL_SUBNET=10.58.58.0 \
            -e DNS_UPSTREAM=10.199.53.53 "$image" >/dev/null
        wait_tunnels "$srv"
        grep -q 'All tunnels are now active' <<<"$(docker logs "$srv" 2>&1)" || e2e_fail "server tunnel did not come up"
    }
    run_cli() {
        docker rm -f "$cli" >/dev/null 2>&1 || true
        docker run -d --name "$cli" --network "$network" --cap-add NET_ADMIN --device /dev/net/tun \
            -v "$volume-cli":/config "$@" "$image" >/dev/null
        wait_tunnels "$cli"
    }
    docker network create --subnet 10.199.53.0/24 "$network" >/dev/null
    docker run -d --name "$up" --network "$network" --ip 10.199.53.53 --entrypoint dnsmasq "$image" \
        -k --no-resolv --no-hosts --user=root --log-facility=- --address=/upstream.test/192.0.2.53 >/dev/null

    run_srv
    docker exec "$srv" sh -c 'echo "address=/edit.test/192.0.2.7" >> /config/templates/dnsmasq.conf'
    run_srv
    healthy "$srv" || e2e_fail "server is not healthy"
    # Server mode: any DNS state but "running" is unhealthy, a missing one included.
    for bad in missing bogus disabled; do
        if [[ "$bad" == missing ]]; then
            docker exec "$srv" rm -f /run/dnsmasq-state
        else
            docker exec "$srv" sh -c "echo $bad > /run/dnsmasq-state"
        fi
        docker exec "$srv" /app/healthcheck >/dev/null && e2e_fail "server healthy with DNS state '$bad'"
    done
    docker exec "$srv" sh -c 'echo running > /run/dnsmasq-state'
    healthy "$srv" || e2e_fail "server not healthy again after restoring the DNS state"
    docker exec "$srv" cat /config/peer1/peer1.conf > "$tmpdir/e2e-peer1.conf"
    put_conf "$volume-cli" "$tmpdir/e2e-peer1.conf"
    run_cli --sysctl net.ipv4.conf.all.src_valid_mark=1
    grep -q 'All tunnels are now active' <<<"$(docker logs "$cli" 2>&1)" || e2e_fail "client tunnel did not come up"

    docker exec "$cli" ping -c1 -W3 10.58.58.1 >/dev/null || e2e_fail "client cannot reach the server through the tunnel"
    [[ "$(docker exec "$cli" awg show wg0 latest-handshakes | awk '{print $2}')" != 0 ]] || e2e_fail "no handshake"
    healthy "$cli" || e2e_fail "client is not healthy"
    docker exec "$cli" grep -qx 'nameserver 10.58.58.1' /etc/resolv.conf || e2e_fail "client resolver is not the tunnel address"
    docker exec "$cli" nslookup -type=a upstream.test | grep -q '192.0.2.53' \
        || e2e_fail "client -> tunnel -> dnsmasq -> upstream lookup failed"
    docker exec "$cli" nslookup -type=a edit.test | grep -q '192.0.2.7' \
        || e2e_fail "a record from the edited dnsmasq template does not resolve"
    docker exec "$cli" ip route get 1.1.1.1 | grep -q 'dev wg0' || e2e_fail "client traffic does not use the tunnel"
    listeners=$(docker exec "$srv" ss -Hlntu 'sport = :53')
    if [[ -n "$(foreign_listeners 10.58.58.1 "$listeners")" ]] || ! grep -q '10.58.58.1:53 ' <<<"$listeners"; then
        e2e_fail "server dnsmasq should listen on 10.58.58.1 and loopback only: $listeners"
    fi
    # A neighbour that routes the VPN subnet to the server reaches it (ping),
    # but dnsmasq ignores DNS that does not arrive on wg0. The UDP query is the
    # regression check: with listen-address instead of interface=wg0 it is
    # answered. dnsmasq refused the neighbour's TCP query with either setting,
    # so the TCP check only documents that. The peer is queried around it
    # (UDP and TCP), so a dead dnsmasq cannot pass this.
    peer_queries
    docker run --rm --network "$network" --cap-add NET_ADMIN --entrypoint sh "$image" -c '
        ip route add 10.58.58.0/24 via 10.199.53.10 &&
        ping -c1 -W3 10.58.58.1 >/dev/null &&
        ! nslookup -type=a -timeout=2 -retry=1 health.amneziawg. 10.58.58.1 >/dev/null 2>&1 &&
        [ "$(sh -c "$1" sh 10.58.58.1)" -le 2 ]
    ' sh "$tcp_query" || e2e_fail "a container outside the tunnel can query dnsmasq (or cannot reach the server at all)"
    peer_queries
    ok "server + client-mode peer: handshake, DNS through the tunnel to the upstream, both healthy, DNS (UDP+TCP) closed to non-peers"

    run_cli
    grep -q 'All tunnels are now down' <<<"$(docker logs "$cli" 2>&1)" \
        || e2e_fail "a full-tunnel client without src_valid_mark=1 came up"
    docker exec "$cli" /app/healthcheck >/dev/null && e2e_fail "client healthy without a tunnel"
    [[ -z "$(docker exec "$cli" sh -c 'ip -4 route show default table all; ip -6 route show default table all')" ]] \
        || e2e_fail "client kept a default route without a tunnel"
    ok "full-tunnel client without src_valid_mark=1 fails closed"
    echo "- End to end: OK" >> "$summary"
fi

echo "All smoke tests passed!" >> "$summary"
echo "Smoke tests passed!"
