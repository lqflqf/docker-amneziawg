# MTU for AmneziaWG tunnels

The short version is in the [README](../README.md#performance-and-mtu). This page explains where the numbers come from.

The container does not write an `MTU` line. Therefore, `awg-quick` uses the same rule as plain WireGuard. It takes the MTU of the route to the endpoint and subtracts 80. Most links have an MTU of 1500. On these links, the tunnel MTU is 1420. That 80 covers an IPv6 header, UDP, and the 32-byte WireGuard transport framing. It does **not** cover the extra bytes that AmneziaWG adds. This is why users on AWG 3.x report that MTU 1280 makes the tunnel faster, sometimes dramatically.

## What AmneziaWG adds to every transport packet

Each encrypted data packet on the wire is

```
IP (20 IPv4 / 40 IPv6) + UDP (8) + S4 + 16-byte header + payload (≤ MTU) + ContentPadding + 16-byte tag
```

compared with plain WireGuard, where `S4` and `ContentPadding` are both zero. The pieces behave differently:

| Component | Size | On a full-size packet | Notes |
|-----------|------|-----------------------|-------|
| `S4` | random 4-20 (2.0) / 12-20 (3.x), max 32 | **Adds to the datagram** | The only part `awg-quick`'s 80-byte allowance does not know about. It also carries the header-protection nonce in 3.x, so it cannot go below 12 there |
| `ContentPaddingAddition` (3.x) | random `lo-hi` (container: 16-128, or 0 with trailers) | A few bytes | Capped at the largest datagram seen, sent or received. Thus, full-size packets grow slightly. This is not enough to fragment, but it costs ~22% of download ([why](awg-performance.md#contentpaddingaddition-costs-22-of-download)). Set `AWG_CONTENT_PADDING=0` |
| `RandomTrailers` (3.1) on transport | random `0 … window − packet` | Nothing — capped at the largest datagram already seen | Only active on transport packets when `ContentPaddingAddition = 0`. With it, a 52-byte TCP ACK can become a ~1400-byte datagram |

With the default 1420 tunnel MTU, a full-size packet becomes `1420 + 60 + S4` bytes over IPv4. It becomes `1420 + 80 + S4` over IPv6. Over IPv4, that exceeds 1500 when `S4 > 20`. Over an IPv6 endpoint, it exceeds 1500 for any `S4 > 0`. The container draws `S4` at 20 or below. Older images drew up to 27, and saved parameters are reused. On an older deployment, check `AWG_S4` in `/config/server/awg_params`.

## Why an oversized packet is slow rather than broken

A UDP datagram larger than the path MTU is not rejected. The kernel fragments it into two IP packets. Each full-size download packet now costs two packets on the wire. The far end must reassemble them. The harmful part is that many carrier-grade NATs, mobile networks, cloud load balancers, and DPI boxes drop IP fragments. Some rate-limit them instead. Each dropped fragment loses the whole datagram. The TCP inside the tunnel sees loss, backs off, and throughput collapses. Small packets, such as pings, handshakes, and web pages, continue to work. Fragmented UDP is also a classic DPI fingerprint, which defeats the obfuscation.

The client side has the same problem in the other direction. Usually, it has a *smaller* path MTU than the server. Examples include PPPoE (1492), LTE/5G, IPv6-over-IPv4 transitions, and corporate Wi-Fi. LTE/5G is often 1400 or less, and iOS enforces path MTU strictly. `awg-quick` on the server cannot know this.

1280 leaves 220 bytes of headroom on a 1500-byte IPv4 path. That is enough for `S4`, UDP, IP, and a few hops of extra encapsulation. Its wire packets, `1280 + 60 + S4`, clear PPPoE (1492), LTE (~1400), and every ordinary path. This is why people converge on it. It is also why Amnezia's installers and the 3.1 upgrade guides set it by default.

Be precise about the "IPv6 minimum" argument. The 1280-byte floor applies to the packet **on the wire**. A tunnel MTU of 1280 produces wire packets of `1340 + S4` bytes over an IPv4 endpoint. It produces `1360 + S4` over an IPv6 one. On a path whose own MTU really is 1280, those packets still fragment. Examples are DS-Lite, some LTE, and tunnel-in-tunnel setups. The truly-safe-everywhere tunnel MTU is `1280 − 60 − S4` for an IPv4 endpoint. It is `1280 − 80 − S4` for IPv6. These values keep the outer packet at or below 1280. At `S4 = 12`, the values are 1208 and 1188. A capture verified this on such a path ([measurement](awg-performance.md#per-packet-overhead-and-the-handshake-burst-fix)). Measure your path with a `ping -M do` binary search. Use 1280 when the path is normal or unknown-but-probably-normal. Use `path − 60 − S4` (IPv4) or `path − 80 − S4` (IPv6) when you know the path is constrained.

## Which value to pick

| Situation | Tunnel MTU | Why |
|-----------|-----------:|-----|
| Mobile clients, PPPoE, unknown paths, anything that "works but is slow" | **1280** | Clears every ordinary path. It needs path MTU ≥ `1340 + S4` on the wire for an IPv4 endpoint. It needs `1360 + S4` for IPv6. The cost is a ~10% higher header-to-payload ratio, which is small next to fragmentation loss |
| Path that is itself constrained to ~1280 (DS-Lite, tunnel-in-tunnel, some LTE) | `path − 60 − S4` (IPv4) / `path − 80 − S4` (IPv6) | The outer packet must fit the *path*, not the IPv6 floor. At `S4 = 12`, a 1280-byte path needs tunnel MTU **1208** for an IPv4 endpoint or **1188** for IPv6. Measure with a `ping -M do` binary search |
| Wired clients on a clean 1500-byte path, IPv4 endpoint | 1400-1420 | `1500 − 20 − 8 − 32 − S4`. 1420 is the ceiling for the largest default `S4` (20). 1408 is the ceiling for the hard maximum (32). 1400 also survives one extra 8-byte encapsulation |
| IPv6 endpoint on a 1500-byte path | 1388-1400 | `1500 − 40 − 8 − 32 − S4`: 1400 for `S4 = 20`, 1388 for `S4 = 32` |
| You have set `AWG_S4` yourself | `path − 60 (IPv4) / 80 (IPv6) − S4` | Recompute when you change `S4` |

Do not go above the derived number to get more speed. The tunnel MTU is a ceiling. Each byte above the path limit returns as fragmentation. If you go lower than necessary, you only add per-packet overhead. On a normal path, a value below 1280 gives no benefit. On a constrained path, a value below the derived value of that path gives no benefit. At `S4 = 12` on a true 1280-byte path, those values are 1208 for IPv4 and 1188 for IPv6. The number is derived, not magic.

Some setups use `RandomTrailers` without `ContentPaddingAddition`, for example 3.1 with `AWG_CONTENT_PADDING=0`. In these setups, a lower MTU also helps in a second way. The trailer window tracks the largest datagram seen. Thus, a smaller MTU caps how far small packets can grow. This matters on asymmetric links where the upload ACK stream limits download speed.

## How to set it

For an existing deployment, add an `MTU` line to the `[Interface]` section of the generated confs. Then restart the container. For new deployments, put the line in `/config/templates/server.conf` and `/config/templates/peer.conf` instead. Do the same before the next regeneration. Templates are only read when configs are generated or regenerated. This happens on first start, or when a server-side or `AWG_*` variable changes:

```ini
[Interface]
Address = ...
MTU = 1280
```

Set it on both the server conf (`/config/wg_confs/wg0.conf`) and every peer conf. Peer conf examples are `/config/peerN/peerN.conf`. Then re-import on the device. QR codes and `.conf` files are regenerated from the templates only. Thus, hand-edited peer confs must be distributed again. The two sides do not have to agree. Each side's MTU only limits what *it* sends. But a peer left at 1420 still fragments its uploads. The AmneziaVPN app exposes MTU in the connection settings. On Windows, the WinTUN adapter ignores the config value and uses 1280 regardless.
