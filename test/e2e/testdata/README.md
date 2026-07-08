# e2e testdata

## `plain-wireguard.pcap`

A capture of a **plain (un-obfuscated) WireGuard** tunnel: the handshake
initiation/response plus a few transport-data packets, captured on the outer
UDP wire between two network-namespace endpoints driven by the Linux kernel
`wireguard` module (`wg`, no amnezia obfuscation, no wanbond).

It is the **positive control** for `TestP5DPI` (M9, requirement 6): the DPI
non-classification check runs `ndpiReader` on it and asserts nDPI DOES label it
`WireGuard`/VPN. If that assertion fails, the tool or the parser is broken and
the negative assertion (that the *obfuscated* wanbond flow is NOT classified)
would be vacuous. wanbond always obfuscates via its outer frame codec, so a
plain-WireGuard reference cannot be produced from wanbond itself — hence a
committed reference capture.

Regenerate with `gen-plain-wireguard.sh` under an unprivileged user+net
namespace (needs the host `wireguard` kernel module, `wg`, `iproute2`,
`tcpdump` — all in the dev shell / on the e2e host):

```sh
nix develop -c bash -c 'unshare -Urmn bash test/e2e/testdata/gen-plain-wireguard.sh test/e2e/testdata/plain-wireguard.pcap'
```

The exact bytes are not load-bearing — any capture containing a real
WireGuard handshake that `ndpiReader` classifies as `WireGuard` serves.
