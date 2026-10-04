# AmneziaWG parameter reference

This reference lists every obfuscation key, range, and constraint. All keys live in `[Interface]`. For throughput cost, see `docs/awg-performance.md`. For MTU, see `docs/mtu.md` at repository root.

## Junk packets — `Jc`, `Jmin`, `Jmax`

Random datagrams are sent before each handshake. They affect only the handshake and are free during a session. They may differ per endpoint.

| Key | Range | Typical |
|---|---|---|
| `Jc` | 0-128 | 3-8 (higher = more noise, slower handshake) |
| `Jmin` | 1-1279, `< Jmax` | 40-80 |
| `Jmax` | 2-1280 | 80-250 |

## Packet prefixes — `S1`-`S4`

Random bytes are prepended to each message type. They hide WireGuard's fixed sizes.

| Key | Message | Max | Wire length |
|---|---|---|---|
| `S1` | handshake initiation | 1132 | 148 + S1 |
| `S2` | handshake response | 1188 | 92 + S2 |
| `S3` | cookie reply | 64 | 64 + S3 |
| `S4` | **every transport packet** | 32 | payload + S4 |

- Keep **`S4 ≤ 20`**. You pay it on every packet. `awg-quick`'s 1420 MTU does not account for it.
- **`S1 + 56 ≠ S2`**. Otherwise, the padded initiation and response have the same length.
- **All ≥ 12 with `HeaderProtectionKey`**. The nonce is the first 12 prefix bytes. The kernel module and amneziawg-go reject shorter values.
- **All equal with `RandomTrailers = on`** (2.0+ ranges). Otherwise, about 3.5% of data packets are misclassified and dropped. The 56-byte gap is moot then because trailers randomize handshake lengths.
- AWG 1.5 fixes `S3 = S4 = 0`.

The maxima are conventions. They reserve payload budget against 1280. They are not runtime checks. amneziawg-go parses `s1`-`s4` as bare integers. The kernel module only enforces the ≥ 12 floor. Stay inside them anyway.

## Header values — `H1`-`H4`

These values replace WireGuard's message types 1-4. The message types are initiation, response, cookie, and transport.

- All ≥ 5, unique and, as ranges, **non-overlapping**.
- **2.0+ uses ranges** (`205127846-255127846`). The Amnezia app treats single integers as AWG 1.5. It then ignores `I1`-`I5`. The usual generator puts one 50,000,000-wide range in each quadrant of the value space.
- With `HeaderProtectionKey`, upstream allows `1,2,3,4`. The type field is encrypted anyway. This also removes the trailer misclassification risk. But it costs Amnezia-app version detection. Prefer ranges plus equal `S`.

## Signature packets — `I1`-`I5`

These packets are sent before the handshake and every 120 s. They imitate another UDP protocol. If `I1` is not set, `I2`-`I5` have no effect. All values must match on every endpoint. Put all values in `[Interface]` above `[Peer]`. Never write an empty `I2 =`. `awg` rejects the whole file. Values contain `=`. Split lines on the first `=` only.

Common default, a 1200-byte QUIC Initial (RFC 9000 §14.1). QUIC is ubiquitous and costly to block:

```
I1 = <b 0xc3><b 0x00000001><b 0x08><r 8><b 0x00><b 0x00><b 0x449e><r 4><r 1178>
```

### CPS tag syntax

| Tag | Meaning |
|---|---|
| `<b 0xHEX>` | literal bytes |
| `<r N>` | N random bytes |
| `<rc N>` | N random letters `[A-Za-z]` |
| `<rd N>` | N random digits |
| `<t>` | 32-bit Unix timestamp |

Example DNS-query disguise:

```
I1 = <b 0x0001><b 0x0100><b 0x00010000><b 0x00000000><rd 4><b 0x00000020><rc 8><b 0x076578616d706c6503636f6d00><b 0x0001><b 0x0001>
```

[AmneziaWG Architect](https://architect.vai-rice.space/) generates QUIC, DNS, DTLS, SIP and HTTP/3 disguises. Do not ask users to hand-write them.

### `<r N>` size

Current AmneziaWG has **no per-tag size limit**. The 1000-byte check existed only in amneziawg-go ≤ v0.2.15 (`device/awg/tag_generator.go:73`). `0361c54` removed it (PR #103). amneziawg-tools and the kernel module never had one. docs.amnezia.org still says "≤ 1000". Some third-party parsers enforce it. One observed example is Keenetic: `invalid I1 value`. Its threshold is unconfirmed. That error also has unrelated causes. For such a parser, split only the oversized tag. Use `<r 1178>` → `<r 1000><r 178>`. That is byte-identical on the wire. Adjacent random tags fill one run. I1-I5 are send-only. Thus, only that client's conf changes. Do not change generators or add size checks for it.

## AWG 3.0 parameters

| Key | Value | Typical | Must match |
|---|---|---|:---:|
| `HeaderProtectionKey` | 32 random bytes, base64 | random | **yes** |
| `ContentPaddingAddition` | `lo-hi` bytes or `0` | **`0`** | no |
| `RekeyAfterTime` | `lo-hi` s | 100-145 (WG 120) | no |
| `RekeyTimeout` | `lo-hi` s | 4-10 (WG 5) | no |
| `RejectAfterTime` | `lo-hi` s | derived (WG 180) | no |
| `KeepaliveTimeout` | `lo-hi` s | 8-22 (WG 10) | no |
| `MaxHandshakeAttempts` | `lo-hi` | 12-28 (WG 18) | no |

`HeaderProtectionKey` is a symmetric ChaCha20 key. Use `awg genpsk` or `head -c 32 /dev/urandom | base64`. Never use `genkey` because it is Curve25519-clamped. Each endpoint draws its own timer values. Keep `RejectAfterTime.lo > RekeyAfterTime.hi` and `> KeepaliveTimeout.lo + RekeyTimeout.lo`.

## AWG 3.1 switches

Independent booleans are valid with any version. Write the key only to enable it. `off` is the default. If you omit the key, older software can still read the file.

| Key | Effect | Must match |
|---|---|:---:|
| `RandomTrailers` | Random-length trailer on handshake packets, and on transport packets when `ContentPaddingAddition = 0` | **yes** — a peer without it drops padded handshakes |
| `DisableCookies` | No cookie replies under load (removes a probe response, gives up DoS mitigation) | no |

## Version detection

| Signal | Version |
|---|---|
| `HeaderProtectionKey` and `RandomTrailers = on` | 3.1 |
| `HeaderProtectionKey` | 3.0 |
| Range `H` values or `I1` | 2.0 |
| Integer `H`, `S3 = S4 = 0` | 1.5 |

## Checklist (what `awg-lint.py` enforces)

- `Jmin < Jmax ≤ 1280`, `0 ≤ Jc ≤ 128`
- `S1 ≤ 1132`, `S2 ≤ 1188`, `S3 ≤ 64`, `S4 ≤ 32` (prefer ≤ 20), `S1 + 56 ≠ S2`
- `S1`-`S4 ≥ 12` with `HeaderProtectionKey`; all equal with `RandomTrailers = on` on 2.0+
- `H1`-`H4 ≥ 5`, unique, non-overlapping, ranges for 2.0+
- `I1` before `I2`-`I5`, no empty values, all in `[Interface]`
- `ContentPaddingAddition = 0` unless wanted, never with `RandomTrailers`
- 3.x timer order above
- Explicit `MTU ≤ path − 60 − S4` (IPv4)

## Sources

[docs.amnezia.org](https://docs.amnezia.org/documentation/amnezia-wg) · [kernel module](https://github.com/amnezia-vpn/amneziawg-linux-kernel-module) · [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) · [Any Tech ARCHITECT](https://github.com/Vadim-Khristenko/Any-Tech-ARCHITECT)
