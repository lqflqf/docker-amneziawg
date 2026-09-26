# MTU for AmneziaWG tunnels

The short version is in the [README](../README.md#performance-and-mtu). This page explains where the numbers come from.

The container does not write an `MTU` line, so `awg-quick` does what it does for plain WireGuard: it takes the MTU of the route to the endpoint (1500 on most links) and subtracts 80, giving a tunnel MTU of 1420. That 80 covers an IPv6 header, UDP and the 32-byte WireGuard transport framing. It does **not** cover the bytes AmneziaWG adds on top, and that is why users on AWG 3.x report that dropping the MTU to 1280 makes the tunnel faster — sometimes dramatically.

## What AmneziaWG adds to every transport packet

Each encrypted data packet on the wire is

```
IP (20 IPv4 / 40 IPv6) + UDP (8) + S4 + 16-byte header + payload (≤ MTU) + ContentPadding + 16-byte tag
```

compared with plain WireGuard, where `S4` and `ContentPadding` are both zero. The pieces behave differently:

| Component | Size | On a full-size packet | Notes |
|-----------|------|-----------------------|-------|
| `S4` | random 4-20 (2.0) / 12-20 (3.x), max 32 | **Adds to the datagram** | The only part `awg-quick`'s 80-byte allowance does not know about. Also carries the header-protection nonce in 3.x, which is why it cannot go below 12 there |
| `ContentPaddingAddition` (3.x) | random `lo-hi`, container default within 16-128 | A few bytes | Capped at the largest datagram seen so far, not at the MTU — and that high-water mark counts packets *received* as well as sent, so it drifts above the current packet size and full-size packets do grow a little. Not enough to fragment, but it costs ~22% of download by breaking the receiver's UDP batching. Set `AWG_CONTENT_PADDING=0` |
| `RandomTrailers` (3.1) on transport | random `0 … window − packet` | Nothing — capped at the largest datagram already seen | Only active on transport packets when `ContentPaddingAddition = 0`. With it, a 52-byte TCP ACK can become a ~1400-byte datagram |

So with the default 1420 tunnel MTU a full-size packet becomes `1420 + 60 + S4` bytes over IPv4, and `1420 + 80 + S4` over IPv6. Over IPv4 that exceeds 1500 as soon as `S4 > 20`; over an IPv6 endpoint it exceeds 1500 for any `S4 > 0`. The container now draws `S4` at 20 or below, but images before that drew values up to 27, and saved parameters are reused: check `AWG_S4` in `/config/server/awg_params` on an older deployment. That is why one deployment is fine and the next one is "slow for no reason".

## Why an oversized packet is slow rather than broken

A UDP datagram larger than the path MTU is not rejected; the kernel fragments it into two IP packets. Every full-size packet of a download now costs two packets on the wire, the far end has to reassemble them, and — the part that actually hurts — many carrier-grade NATs, mobile networks, cloud load balancers and DPI boxes drop IP fragments outright or rate-limit them. Each dropped fragment loses the whole datagram, the TCP inside the tunnel sees loss, backs off, and throughput collapses while small packets (pings, handshakes, web pages) keep working. Fragmented UDP is also a classic fingerprint for DPI, which undoes the point of the obfuscation.

The client side has the same problem in the other direction, and it usually has a *smaller* path MTU than the server: PPPoE (1492), LTE/5G (often 1400 or less, and iOS enforces path MTU strictly), IPv6-over-IPv4 transitions, corporate Wi-Fi. `awg-quick` on the server has no way of knowing any of this.

1280 leaves 220 bytes of headroom on a 1500-byte IPv4 path — enough for `S4`, UDP, IP and a few hops of extra encapsulation — and its wire packets (`1280 + 60 + S4`) clear PPPoE (1492), LTE (~1400) and every ordinary path. That is why it is the value people converge on, and why Amnezia's own installers and the 3.1 upgrade guides set it by default.

Be precise about what the "IPv6 minimum" argument guarantees, though: the 1280-byte floor applies to the packet **on the wire**, and a tunnel MTU of 1280 produces wire packets of `1340 + S4` bytes over an IPv4 endpoint and `1360 + S4` over an IPv6 one. On a path whose own MTU really is 1280 — DS-Lite, some LTE and tunnel-in-tunnel setups — those still fragment. The truly-safe-everywhere tunnel MTU is `1280 − 60 − S4` for an IPv4 endpoint (1208 at `S4 = 12`) or `1280 − 80 − S4` for IPv6 (1188), which keeps the outer packet at or under 1280. We verified the IPv4 case on such a path: at tunnel MTU 1208, 118,559 of 118,565 full-size datagrams measured exactly 1280 on the wire (the remainder were the handshake-burst outliers documented in [docs/awg-performance.md](awg-performance.md)), and tunnel MTU 1280 would have fragmented every one of them. Measure your path (`ping -M do` binary search) rather than assuming; use 1280 when the path is normal or unknown-but-probably-normal, and `path − 60 − S4` (IPv4) / `path − 80 − S4` (IPv6) when you know the path is constrained.

## Which value to pick

| Situation | Tunnel MTU | Why |
|-----------|-----------:|-----|
| Mobile clients, PPPoE, unknown paths, anything that "works but is slow" | **1280** | Clears every ordinary path (needs path MTU ≥ `1340 + S4` on the wire for an IPv4 endpoint, `1360 + S4` for IPv6); the cost is a ~10% higher header-to-payload ratio, which is nothing next to fragmentation loss |
| Path that is itself constrained to ~1280 (DS-Lite, tunnel-in-tunnel, some LTE) | `path − 60 − S4` (IPv4) / `path − 80 − S4` (IPv6) | The outer packet must fit the *path*, not the IPv6 floor: at `S4 = 12` a 1280-byte path needs tunnel MTU **1208** (IPv4 endpoint) or **1188** (IPv6). Measure with a `ping -M do` binary search |
| Wired clients on a clean 1500-byte path, IPv4 endpoint | 1400-1420 | `1500 − 20 − 8 − 32 − S4`. 1420 is the ceiling for the largest default `S4` (20), 1408 for the hard maximum (32); 1400 also survives one extra 8-byte encapsulation |
| IPv6 endpoint on a 1500-byte path | 1388-1400 | `1500 − 40 − 8 − 32 − S4`: 1400 for `S4 = 20`, 1388 for `S4 = 32` |
| You have set `AWG_S4` yourself | `path − 60 (IPv4) / 80 (IPv6) − S4` | Recompute when you change `S4` |

Do not go above the derived number expecting more speed: the tunnel MTU is a ceiling, and every byte above the path limit is paid back as fragmentation. Going lower than you need only adds per-packet overhead: on a normal path there is no reason to drop under 1280, and on a constrained path no reason to drop under that path's own derived value (1208 IPv4 / 1188 IPv6 on a true 1280-byte path at `S4 = 12`). The number is derived, not magic.

If you rely on `RandomTrailers` without `ContentPaddingAddition` (3.1 with `AWG_CONTENT_PADDING=0`), a lower MTU also helps in a second way: the trailer window tracks the largest datagram seen, so a smaller MTU caps how far small packets can be inflated, which matters on asymmetric links where the upload ACK stream is what limits download speed.

## How to set it

For an existing deployment, add an `MTU` line to the `[Interface]` section of the generated confs and restart the container. For new deployments (or before the next regeneration), put the line in `/config/templates/server.conf` and `/config/templates/peer.conf` instead — templates are only read when the configs are (re)generated, which happens on first start or when a server-side or `AWG_*` variable changes:

```ini
[Interface]
Address = ...
MTU = 1280
```

Set it on both the server conf (`/config/wg_confs/wg0.conf`) and every peer conf (`/config/peerN/peerN.conf`, then re-import on the device — QR codes and `.conf` files are regenerated from the templates only, so hand-edited peer confs must be re-distributed). The two sides do not have to agree — each side's MTU only limits what *it* sends — but a peer left at 1420 still fragments its uploads. The AmneziaVPN app exposes MTU in the connection settings; on Windows the WinTUN adapter ignores the config value and uses 1280 regardless.
