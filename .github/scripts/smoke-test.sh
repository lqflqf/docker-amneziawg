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
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

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
unbound_pid=$(docker exec "$container" pgrep -x unbound) \
    || { echo "unbound is not running"; docker logs "$container"; exit 1; }
unbound_uid=$(docker exec "$container" awk '/^Uid:/ {print $2}' "/proc/${unbound_pid}/status")
abc_uid=$(docker exec "$container" id -u abc)
if [[ "$unbound_uid" != "$abc_uid" ]]; then
    echo "unbound runs as uid ${unbound_uid}, expected abc (${abc_uid})"; exit 1
fi
echo "  ok   - unbound config valid, running as abc (uid ${abc_uid})"
echo "- Unbound at runtime: OK" >> "$summary"

echo "All smoke tests passed!" >> "$summary"
echo "Smoke tests passed!"
