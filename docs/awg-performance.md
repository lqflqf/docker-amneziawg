# AmneziaWG obfuscation: speed and latency

This page shows the throughput cost of each obfuscation parameter, the reason for the cost, and the recommended values. Each statement comes from upstream source code. A measurement on a real internet path confirms each statement.

Short version: **`RandomTrailers` combined with unequal `S1`-`S4` costs 98% of upload throughput.** The container now draws one shared `S` value when trailers are on. On the next start, it also drops saved unequal values. Hand-written configs and other generators can still make this broken shape. See [Recommended parameters](#recommended-parameters).

## Which parameters cost anything

Obfuscation has two groups of work. The first group occurs during the handshake. The second group occurs for each packet. Only the second group can affect steady-state speed.

| Parameter | Per-packet cost | Where |
|---|---|---|
| `Jc`, `Jmin`, `Jmax` | none — handshake only | `send.c:71-89` |
| `S1`, `S2`, `S3` | none — init/response/cookie only | `send.c:89,161,179` |
| `I1`-`I5` | none — sent every 120s | — |
| `H1`-`H4` | none — a value substituted into an existing field | `send.c:293` |
| `S4` | **+S4 bytes + one CSPRNG call per packet** | `send.c:297-298` |
| `HeaderProtectionKey` | one `chacha_init` + 16-byte XOR per packet | `header_protection.c:6-23` |
| `ContentPaddingAddition` | **+random bytes per packet** | `peer.h:110-124` |
| `RandomTrailers` | **+random bytes per packet** | `peer.h:98-108` |

The three padding mechanisms are mutually exclusive. The precedence is not obvious (`send.c:253-260`, mirrored in `amneziawg-go` `device/send.go:607-614`):

```c
if (!u16_range_is_zero(content_padding_addition))      // wins if set
    padding_len = wg_peer_skb_randomize_padding_addition(...);
else if (peer->device->random_trailers)                // only if CPA is unset
    padding_len = wg_peer_skb_random_trailer(...);
else
    padding_len = calculate_skb_padding(skb);          // stock: pad to multiple of 16
```

Thus, `ContentPaddingAddition` silently disables `RandomTrailers` **on the send path**. It does *not* disable it on the receive path. See below.

## The upload collapse

`RandomTrailers` changes how a receiver identifies packet types. With it off, each type must have an exact length. With it on, the length is only a lower bound (`receive.c:51,62,73`):

```c
random_trailers ? skb->len >= expected_len : skb->len == expected_len
```

The receiver then tries init, response, cookie, and transport in order. For each type, it reads a 4-byte type field at that type's padding offset. These offsets are `S1`, `S2`, `S3`, and `S4`. It then tests the field against the related `H` range.

When `>=` always passes, only the type check separates the branches. With independently drawn `S1`-`S4`, the values differ. Thus, the init, response, and cookie branches read the type field at the **wrong offset**. They get garbage. Garbage lands inside a 50,000,000-wide `H` range with probability 50e6 / 2³² = 1.16% per branch. There are three branches, so:

**≈3.49% of data packets are misclassified as handshake messages and dropped.**

The Mathis model uses that loss, the measured 22ms RTT, and a 1140-byte MSS. It predicts 2.7 Mbit/s. Measured speed is 1.7-2.4 Mbit/s.

Set `S1 = S2 = S3 = S4` to remove the failure fully. Each branch then reads the *true* type field. Because `H1`-`H4` must not overlap, a transport packet cannot match another type range. This is what the [upstream docs](https://docs.amnezia.org/documentation/amnezia-wg) mean by "when using `RandomTrailers` it is recommended to set the same values for `S1`, `S2`, `S3` and `S4`".

Set `H1`-`H4` to `1,2,3,4` to fix it too. This shrinks each range to one value. Upstream recommends that separately when `HeaderProtectionKey` is set. Header protection already encrypts the type field, so custom `H` values add nothing.

### `ContentPaddingAddition` does not save you

`ContentPaddingAddition` suppresses `RandomTrailers` on send (`send.c:254`). But the receive-side match reads the flag directly (`receive.c:47`):

```c
bool random_trailers = wg->random_trailers;   // not gated on CPA
```

If you set both, you get the loose match and its misclassification risk. You do not get trailer obfuscation. This is the worst of the two options. Therefore, the container leaves `ContentPaddingAddition` at `0` when trailers are on.

## Measurements

Client: this repo's image, userspace `amneziawg-go` 3.1.20260814, 20-core i5-14600K.
Server: `amneziawg` kernel module 3.1.20260812, 1 vCPU Xeon 6230R.
Path: 22ms RTT, client path MTU 1280, tunnel MTU 1180. Without the tunnel, the link does 107↑ / 181↓ Mbit/s. Server CPU stayed at 36-40% throughout. Therefore, no measurement here is CPU-bound.

| Configuration | ↑ Mbit/s | ↓ Mbit/s | wire bytes per 64-byte ping |
|---|---:|---:|---:|
| plain WireGuard (no obfuscation) | 100.5 | 132.4 | 170 |
| `HeaderProtectionKey` + `S=12`, no CPA/RT | 99.7 | 131.6 | 182 |
| ↑ plus `RandomTrailers` | 99.7 | 125.3 | 537 |
| ↑ plus `ContentPaddingAddition 1-16` | 99.7 | 102.4 | 186 |
| **unequal `S`, `RandomTrailers` + CPA (old container 3.1 default)** | **1.7** | 115.0 | 258 |
| ↑ at `awg-quick`'s default MTU 1420 | **0.5** | 75.5 | 263 |

The small-packet column is measured *after* a bulk transfer. Thus, the trailer window has grown to full MTU. This is the realistic case for mixed traffic.

Each variant independently repairs the collapse. This confirms the mechanism:

| Variant | ↑ Mbit/s |
|---|---:|
| unequal `S`, wide `H`, `RandomTrailers=on` | 1.7 / 2.4 |
| same but `RandomTrailers` off | 98.0 / 98.6 |
| same but `S1=S2=S3=S4` | 95.1 / 99.4 |
| same but `H1..H4 = 1,2,3,4` | 98.5 / 95.3 |

### `HeaderProtectionKey` is effectively free

The values are 131.6 vs 132.4 Mbit/s. It costs one ChaCha20 init and a 16-byte XOR per packet. It also forces the `S4 ≥ 12` floor (`netlink.c:810`). Keep it.

### `RandomTrailers` costs little bulk, a lot on small packets

Bulk throughput changes little: 125.3 vs 131.6 down. The cause is that the trailer short-circuits for full-size packets (`peer.h:105`). When the packet already *is* the largest seen, `udp_window > size` is false. Small packets get the cost. A 64-byte ping costs 537 wire bytes instead of 182, about 3x. That cost affects TCP ACKs, DNS, and VoIP. It is also metered traffic on mobile.

### `ContentPaddingAddition` costs 22% of download

The values are 102.4 vs 131.6 Mbit/s. Six runs reproduced this result, with zero retransmits and server CPU at 37%. `tcpdump` on the server shows why:

```
no CPA:   88789 packets of 1228 bytes,  39297 of 108      <- two uniform sizes
with CPA:  4421 of 1232, 4416 of 1235, 4406 of 1233, ...  <- smeared over ~16 sizes
```

`amneziawg-go` receives with socket-level `UDP_GRO` (`conn/gso_linux.go`). The kernel only coalesces **consecutive equal-sized** datagrams into one read. Random packet lengths defeat that batch process. Thus, the client pays per-datagram syscall and process overhead instead of per-batch overhead. `RandomTrailers` avoids this because it leaves full-size packets alone. `ContentPaddingAddition` does not, because its clamp is the observed UDP window instead of the MTU. The window ratchets above the current packet size.

The 22% figure is measured directly and reproduced. The GRO attribution is an inference from the size distributions and the code. It was not isolated by a test that toggles `UDP_GRO`.

The padding cap is `udp_window - packet_len` (`peer.h:110-124`), not the MTU. `udp_window` is a high-water mark over each datagram sent *and received* (`send.c:243`, `receive.c:571`). Receiving padded packets raises it. Thus, full-size sends do get padded: 1228 becomes 1229-1244 above.

## Per-packet overhead and the handshake-burst fix

Choose a tunnel MTU with [mtu.md](mtu.md). This section gives the evidence behind it.

Per-packet overhead is `20 (IPv4) + 8 (UDP) + 32 (AWG header + tag) + S4` = **`60 + S4`**.

The 32 is structural, not folklore. `struct message_data` is a 4-byte type + 4-byte key index + 8-byte counter = 16. `noise_encrypted_len` adds a 16-byte Poly1305 tag (`messages.h:25,106-114`). A wire capture verified this. The tunnel MTU was 1208, with `S4 = 12`. A bidirectional bulk transfer showed **118,559 of 118,565 full-size datagrams at exactly 1252 bytes of UDP payload**. That is `1208 + 12 + 16 + 16`. With 28 bytes of IPv4+UDP headers, it reaches the 1280-byte path MTU exactly. The `MTU − 80` default, or 1420 on a 1500 link, is `set_mtu_up()` in `awg-quick`. This was confirmed by a conf with no `MTU` line.

The remaining 6 datagrams were server-side packets of 1264-1443 bytes payload. They occurred in bursts that matched handshake attempts. This was an open anomaly at first capture. Upstream has now found the root cause. Kernel modules built before **`4569c4c6`** (2026-09-06) appended a random trailer to *every* raw buffer sent to a peer. This happened because `wg_socket_send_buffer_to_peer()` applied the trailer unconditionally. That included I1-I5 signature packets and dummy junk packets. The commit is titled "do not append random trailers to I1-I5 and dummy junk packets". A handshake burst sends exactly one I1 plus `Jc` junk packets. This matches the observed 6-7 outliers per burst with `Jc = 6`. The sizes exceeded the `udp_window` cap computed from current source. This also shows that the measured module predates the current cap helpers. Use a module built from `4569c4c6` or newer to remove the oversized packets. On older modules, they cost at most rekey latency on narrow paths because handshakes retry.

### Checking whether your module has the fix

**`/sys/module/amneziawg/version` cannot answer this.** Upstream did not bump `version.h` in the fix commit. Thus, a patched module still reports `3.1.20260812`. This is the same string as the module used to capture this anomaly. Anything that tests the date component of that string will misreport. Check the package build or the source instead:

```bash
# Debian/Ubuntu: the PPA version encodes the build date and the commit
dpkg -l amneziawg-dkms
# ii  amneziawg-dkms  1.0.0-0~202609061402+4569c4c~ubuntu20.04.1   <- has the fix

# Or read the source: the trailing bool parameter is the fix
grep -n 'bool trailer' /usr/src/amneziawg-*/socket.c
grep -n 'wg_socket_send_buffer_to_peer' /usr/src/amneziawg-*/send.c
# the I1-I5 and junk-packet call sites pass `false`
```

DKMS builds per kernel. Thus, also confirm that the *loaded* module is the one that was built. It must not be a stale image from before the upgrade. `srcversion` discriminates where `version` does not:

```bash
cat /sys/module/amneziawg/srcversion
modinfo amneziawg | grep -E '^(filename|srcversion)'   # must match
```

This does not affect the container's `check_awg31_kernel_support()`. That only compares the `3.1` major/minor to decide if the module understands the 3.1 switches at all. Upstream does maintain those two components.

Older images drew `S4` values up to 27. With `S4 = 27`, a 1420-MTU packet is 1507 bytes over IPv4. That packet fragments and causes the 75.5 Mbit/s row above.

## Recommended parameters

Best speed while still genuinely AWG 3.1:

```ini
Jc = 6
Jmin = 76
Jmax = 236
S1 = 12          # all four equal — required when RandomTrailers is on
S2 = 12
S3 = 12
S4 = 12          # HeaderProtectionKey floor; ≤ 20 keeps MTU 1420 safe
H1 = 205127846-255127846
H2 = 592243917-642243917
H3 = 1526611121-1576611121
H4 = 1669322812-1719322812
I1 = <b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1178>
HeaderProtectionKey = <32 random bytes, base64>
RandomTrailers = on
MTU = 1280       # or path MTU − 60 − S4; awg-quick's 1420 ignores S4
# ContentPaddingAddition deliberately NOT set
# DisableCookies deliberately NOT set — it costs nothing in throughput and
# gives up WireGuard's DoS mitigation, so enable it only for DPI reasons
```

`MTU` is per-endpoint and does not have to match. If a client has a constrained path, use a lower value. See [mtu.md](mtu.md).

Cost against plain WireGuard: **−1% upload, −5% download, +0.3ms RTT.**

If you also drop `RandomTrailers = on`, both directions stay within 1% of plain WireGuard. Small packets decrease to 182 bytes. This is the correct trade if two conditions are true: you carry much latency-sensitive or metered traffic, and you can give up trailer obfuscation.

Do not set `ContentPaddingAddition`. It costs 22% of download. If you set it with `RandomTrailers`, it also disables trailers on send while it keeps the receive-side risk.

## Reproducing

```bash
# per-packet padding precedence
git clone --depth 1 https://github.com/amnezia-vpn/amneziawg-linux-kernel-module
rg -n -A8 'u16_range_is_zero\(content_padding_addition\)' src/send.c

# receive-side type matching
rg -n 'random_trailers \? skb->len' src/receive.c

# the S >= 12 floor under header protection
rg -n -B2 -A6 'HEADER_PROTECTION_NONCE_SIZE' src/netlink.c
```

## Sources

- [amneziawg-linux-kernel-module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) — `src/send.c`, `src/receive.c`, `src/peer.h`, `src/netlink.c`, `src/header_protection.c`
- [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) — `device/send.go`, `device/receive.go`, `conn/gso_linux.go`
- [Official AmneziaWG parameter docs](https://docs.amnezia.org/documentation/amnezia-wg)
- [Any Tech ARCHITECT](https://github.com/Vadim-Khristenko/Any-Tech-ARCHITECT) — parameter generator and per-parameter rationale
