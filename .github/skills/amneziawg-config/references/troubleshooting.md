# Troubleshooting AmneziaWG

Run the linter first, on the server conf *and* a peer conf — many "it used to work" reports are a shared value that drifted between them:

```bash
scripts/awg-lint.py wg0.conf peer1.conf
```

## Symptom → cause

| Symptom | Most likely cause |
|---|---|
| **Upload ~2 Mbit/s, download nearly fine** | `RandomTrailers = on` with unequal `S1`-`S4` (~3.5% of data packets dropped) |
| **Download ~20-25% low, upload fine** | `ContentPaddingAddition` set (defeats `UDP_GRO` batching) |
| **Handshake and pings fine, downloads crawl** | Fragmentation: `S4 > 20` at the default 1420 MTU, an IPv6 endpoint, or a narrow path |
| **Tunnel never comes up** | A shared value differs between ends, or an endpoint is too old for a key |
| **`Unable to modify interface: Invalid argument`** | Kernel module older than the keys used (`cat /sys/module/amneziawg/version`; 3.1 switches need ≥ 3.1) |
| **`Line unrecognized: `I2='`** | Empty `I2`-`I5` line; delete it |
| **`Line unrecognized`** (other keys) | Userspace `awg` tools older than the keys used |
| **One peer stopped after a change** | Shared values were regenerated; re-issue that peer's conf |
| **Amnezia app reports "AWG 1.5"** | `H1`-`H4` are integers, not ranges |
| **Obfuscation seems inactive in the app** | `I1`-`I5` under `[Peer]` instead of `[Interface]` |
| **Keenetic: `invalid I1 value`** | Its parser caps `<r N>`; split the tag, see `parameters.md` "`<r N>` size" |

## Direction of the failure

- **Upload broken, download fine** — the server's receive path drops packets: the `RandomTrailers` + unequal `S` misclassification.
- **Download broken, upload fine** — client receive path or server send path: `ContentPaddingAddition` or MTU.
- **Both slow** — MTU or the link itself; test without the tunnel for a baseline.

## Confirming

**Misclassification**: it never shows as a handshake failure or a log line. Set `RandomTrailers = off` on both ends; if upload recovers immediately, either keep trailers with equal `S` values or leave them off.

**MTU**: find the path MTU from the client, then compare with `tunnel MTU + 60 + S4` (IPv4) / `+ 80 + S4` (IPv6):

```bash
lo=100; hi=1472
while [ $((hi-lo)) -gt 1 ]; do
    mid=$(((lo+hi)/2))
    if ping -c1 -W2 -M do -s $mid <server-ip> >/dev/null 2>&1; then lo=$mid; else hi=$mid; fi
done
echo "path MTU = $((lo+28))"
```

Value selection: `docs/mtu.md`.

**On the wire** (bulk transfer should show one dominant large and one small size; a smear means `ContentPaddingAddition`, sizes above `path MTU − 28` mean fragmentation):

```bash
sudo timeout 7 tcpdump -i any -nn -q udp port <port> \
  | grep -oE 'length [0-9]+' | awk '{print $2}' | sort -n | uniq -c | sort -rn | head
```

`awg show <interface>` prints the parameters actually in force — authoritative when the file and the live interface may disagree.
