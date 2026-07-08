# wanbond — manual real-link verification checklist

The automated `-tags e2e` suite runs in netns/netem emulation. This checklist is
the manual counterpart, run on the real deployment (Starlink + 5G edge box and a
concentrator VPS with a public IP). Each phase appends its own section; run the
phase's section after that phase lands. Record date, build (`wanbond version`),
and observed numbers next to each item.

Prerequisites (all phases):
- [ ] Edge box has both uplinks up; router pins source IP A → Starlink, source IP
      B → 5G (path selection is external to wanbond).
- [ ] Concentrator reachable on its public IP; UDP not blocked end to end.
- [ ] `wanbond` running both ends from a `0600` config; `/metrics` reachable on
      localhost each end.

## P0 — spike / baseline
- [ ] Tunnel comes up edge ↔ concentrator (WG handshake completes).
- [ ] `ping` and a TCP transfer pass through the tunnel.
- [ ] Record single-path baseline throughput per uplink (iperf3).

## P1 — transparent failover
- [ ] Start a long-lived TCP flow (SSH session or iperf3) over the tunnel.
- [ ] Physically drop the active WAN (unplug / disable the Starlink uplink).
- [ ] Flow survives with NO reset; throughput restored within `P1RecoverySeconds`.
- [ ] Restore the WAN; no thrash. Repeat for the other uplink.
- [ ] Change the edge public IP on one path (carrier re-address); flow survives.

## P2 — aggregation + data-thrift
- [ ] Under saturating load, bonded throughput ≥ `P2BondedMinFraction` × (sum of
      per-path throughputs), read from `/metrics`.
- [ ] While Starlink is healthy and load fits, 5G bytes <
      `P2MeteredMaxByteFraction` of total (`/metrics`).

## P3 — fixed-ratio FEC
- [ ] Induce loss on a path; at the configured parity ratio, ≥
      `P3MinRecoveredFraction` of lost DATA frames recovered without retransmit.
- [ ] FEC overhead ≤ `P3MaxOverheadFactor` × parity ratio (`/metrics`).

## P4 — adaptive FEC
- [ ] Under steady `P4SteadyLossRate` path loss, adaptive total overhead ≤ the
      fixed-FEC baseline for equal masking.
- [ ] Post-recovery residual loss ≤ `P4ResidualLossMax` (`/metrics`).

## P5 — DPI resistance
- [ ] From a hostile-ish network (e.g. a hotel/guest Wi-Fi), the tunnel connects.
- [ ] Capture the flow; nDPI / Suricata do not classify it as WireGuard or any
      identified VPN.

## P1 — scripted real-setup run (Starlink + 5G edge, VPS concentrator)

Scripted counterpart of the P1 section above for the real deployment. Install
per docs/install.md first (binary at `/usr/local/bin/wanbond`, 0600 configs,
systemd units enabled, concentrator tunnel-interface firewall ACCEPT in place).
Inner addresses below assume concentrator `10.77.0.1`, edge `10.77.0.2`; adjust
to your `allowed_ips`. Record date, `wanbond version` output, and observed
numbers next to each item.

### Setup
- [ ] Concentrator: `systemctl start wanbond-concentrator`, then
      `systemctl status wanbond-concentrator` shows active and
      `journalctl -u wanbond-concentrator -n 20` shows `tunnel interface up`.
- [ ] Concentrator firewall ordering verified: `iptables -S INPUT` lists
      `-i wanbond0 -j ACCEPT` BEFORE any `-j REJECT` line (OCI default-REJECT
      caveat, docs/install.md §5) and a UDP ACCEPT for the listen port.
- [ ] Edge: `systemctl start wanbond-edge`; status active; journal shows
      `tunnel interface up` with both paths.
- [ ] Handshake: edge `ping -c 3 10.77.0.1` succeeds.
- [ ] TCP through the tunnel: concentrator `iperf3 -s -B 10.77.0.1`; edge
      `iperf3 -c 10.77.0.1 -t 5` completes (guards the firewall caveat — if
      ping passes but iperf3 fails with "No route to host", the REJECT rule
      is ahead of the tunnel ACCEPT).
- [ ] Both paths live: edge
      `curl -s http://127.0.0.1:9090/metrics | grep wanbond_path` shows
      starlink and 5g.

### Failover: drop Starlink
- [ ] Start the long-lived flow: edge `iperf3 -c 10.77.0.1 -t 120` (or an
      interactive SSH session to 10.77.0.1) and, in a second terminal,
      `ping -i 0.2 10.77.0.1`.
- [ ] Physically drop Starlink (unplug its ethernet/PoE — a real link drop,
      not `ip link set down`).
- [ ] Flow survives with NO reset; ping gap and iperf3 stall ≤
      `P1RecoverySeconds` (3 s). Record the observed gap.
- [ ] Restore Starlink; wait ~30 s; journal shows the path recovering with no
      up/down thrash (no repeated failover lines).

### Failover: drop 5G
- [ ] Repeat the block above dropping the 5G uplink (pull the modem's power
      or antenna). Same acceptance: no reset, gap ≤ 3 s, clean recovery.

### Carrier re-address
- [ ] With the flow running, force a public-IP change on one path (5G: toggle
      airplane mode / `mmcli -m 0 --simple-disconnect && --simple-connect`;
      or power-cycle the Starlink router if it re-NATs). The edge's outbound
      source may also be changed at the router NAT.
- [ ] Flow survives; concentrator journal shows the path's endpoint roaming
      to the new address; ping gap ≤ 3 s.

### Teardown / restart discipline
- [ ] `systemctl reload wanbond-edge` (SIGHUP) with an unchanged config is a
      no-op: journal logs `config reloaded`, tunnel stays up, flow unaffected.
- [ ] `systemctl restart wanbond-edge` recovers the tunnel within seconds;
      a NEW flow passes end to end afterwards.

## P2 — scripted real-setup run (aggregation + data-thrift)

Scripted counterpart of the P2 summary above for the real deployment. Requires the
P1 setup already validated (both uplinks up, both daemons running from `0600`
configs, `/metrics` reachable on `127.0.0.1:9090` each end) AND the edge configured
with the weighted-aggregation scheduler so bonding engages under load:

```toml
[scheduler]
policy = "weighted"
# per_path_capacity_fps sizes the aggregation gate to ~one uplink's frame rate;
# tune it to the slower uplink's sustained frame rate (bytes/s ÷ inner MTU).
```

Inner addresses assume concentrator `10.77.0.1`, edge `10.77.0.2`. `THRU()` below is
`curl -s http://127.0.0.1:9090/metrics | grep wanbond_path_throughput`; `TX(path)` is
`... | grep wanbond_path_tx_bytes_total | grep <path>`. Record date, `wanbond version`,
and observed numbers.

### Baseline: per-uplink solo throughput
- [ ] Record each uplink's SOLO saturated throughput: bring the tunnel up with only
      Starlink configured, run `iperf3 -c 10.77.0.1 -t 20`, and read the Starlink
      `wanbond_path_throughput_bits_per_second` from `/metrics`. Repeat with only 5G.
      Record `T_starlink` and `T_5g` (Mbit/s, from `/metrics`).

### Aggregation under saturating load
- [ ] Bring the tunnel up with BOTH uplinks. Start a saturating flow:
      concentrator `iperf3 -s -B 10.77.0.1`; edge `iperf3 -c 10.77.0.1 -t 30`.
- [ ] Mid-flow, read BOTH paths' `wanbond_path_throughput_bits_per_second` from the
      edge `/metrics` and sum them: `T_bonded`. Confirm both paths are non-zero
      (aggregation engaged, not single-path fallback).
- [ ] Cross-check the far end: the concentrator `/metrics` shows
      `wanbond_path_rx_bytes_total` climbing on BOTH paths.
- [ ] Assert `T_bonded ≥ P2BondedMinFraction × (T_starlink + T_5g)` (0.85). Record the
      measured fraction.

### Data-thrift: metered path stays idle while primary is healthy
- [ ] With both uplinks healthy, run a SUB-capacity flow that fits Starlink alone
      (e.g. edge `iperf3 -c 10.77.0.1 -t 30 -b <~40% of T_starlink>`).
- [ ] Sample `wanbond_path_tx_bytes_total` for both paths at the start and end of the
      flow; take the DELTA over the window.
- [ ] Assert the 5G delta is `< P2MeteredMaxByteFraction × (Starlink + 5G deltas)`
      (0.01) — the metered uplink carried effectively no bytes. Record the fraction.
- [ ] Confirm `wanbond_path_up{path="starlink"}` was `1` throughout (the thrift
      guarantee is conditioned on a healthy primary).

### Aggregation teardown discipline
- [ ] Stop the saturating flow; within a few seconds the edge journal / `/metrics`
      show egress collapsing back to Starlink only (5G tx flat again).
- [ ] `systemctl reload wanbond-edge` after changing `[metrics] listen`: journal logs
      `metrics endpoint rebound`; the new address serves `/metrics`, the old one stops;
      the tunnel and any running flow are unaffected.

## P5 — scripted real-setup run (DPI resistance)

Scripted counterpart of the P5 summary above for the real deployment. This is the
manual, real-link mirror of the automated `TestP5DPI` (netns) check: it confirms that
on a real access network the obfuscated wanbond flow is **not** classified as WireGuard
or any identified VPN by nDPI or Suricata, and it exercises the UDP-block limitation
(docs/install.md §8) as an understood failure mode — not a wanbond defect. Requires the
P1 setup validated (both daemons up from `0600` configs) AND an `[amnezia]` obfuscation
block set IDENTICALLY on both ends (obfuscation ON — plain WireGuard is trivially
classified and is NOT what ships). Run the capture from a realistic *hostile-ish*
network (hotel / guest / captive-portal Wi-Fi, or a lab uplink with a DPI appliance
in path). Install `ndpi` (`ndpiReader`) and `suricata` on the capture host. Record
date, `wanbond version`, the access-network description, and each tool's verdict.

### Positive control FIRST (prove the detectors have teeth)
- [ ] On the capture host, run the shipped positive-control capture through nDPI:
      `ndpiReader -i test/e2e/testdata/plain-wireguard.pcap` and confirm the
      **Detected protocols** block lists **WireGuard** (and category **VPN**). If it
      does NOT, the tool/parse is broken and every "not classified" result below is
      vacuous — fix the tooling before trusting the negative checks.
- [ ] (Informational) Run the same capture through Suricata
      (`suricata -r test/e2e/testdata/plain-wireguard.pcap -l ./sur-pos -k none`) and
      note whether `eve.json` reports `app_proto: wireguard`. The stock Suricata config
      ships no WireGuard app-layer parser, so `failed`/`unknown` here is EXPECTED —
      nDPI carries the WireGuard-specific positive control; Suricata provides the
      app-layer/anomaly negative check.

### Connect + capture the obfuscated wanbond flow
- [ ] From the hostile-ish network, bring the tunnel up (edge `systemctl start
      wanbond-edge`); confirm handshake: edge `ping -c 3 10.77.0.1` succeeds. If the
      network **blocks UDP wholesale**, the handshake will NOT complete — see the
      UDP-block step below; that is the documented limitation, not a bug.
- [ ] Capture the outer WAN UDP while driving representative traffic (a bulk transfer
      + interactive traffic for ~30 s): on the edge uplink interface,
      `tcpdump -i <wan-if> -n -p -U -w wanbond.pcap 'udp port 51820'`
      (adjust the port to your `wireguard.listen_port` / concentrator endpoint).
      Confirm `wanbond.pcap` is non-empty.

### nDPI — negative assertion (the requirement)
- [ ] `ndpiReader -v 2 -i wanbond.pcap`; on the per-flow line for the wanbond flow the
      `[Confidence: …]` field is **NOT** a payload/content match to WireGuard/VPN — a
      `[proto: …/Unknown]` (or QUIC/DNS/etc.) is fine. **Ignore a `Confidence: Match by
      port` "WireGuard/VPN" label if you captured on port 51820** — that is a port guess,
      not a payload classification (see docs/install.md §Limitations); to remove the
      ambiguity, deploy/capture on a **non-registered UDP port** so nDPI cannot
      port-guess. A WireGuard/VPN label with `Confidence: DPI` (a PAYLOAD match) is a
      requirement-6 DEFECT — file it; do not rationalise it away.

### Suricata — negative assertion
- [ ] `suricata -r wanbond.pcap -l ./sur-neg -k none`; inspect `./sur-neg/eve.json`:
      no flow's `app_proto` and no `alert.signature`/`alert.category` names WireGuard
      or a VPN. Record the observed `app_proto` (expected: `failed`/`unknown`). A
      WireGuard/VPN app-proto or alert is a requirement-6 DEFECT.

### UDP-block limitation (understood failure mode, not a defect)
- [ ] On a network (or a test firewall rule) that blocks UDP wholesale, confirm the
      tunnel FAILS to connect (no handshake, `ping 10.77.0.1` fails) and the edge
      journal shows only outbound handshake attempts with no response. Confirm this is
      the EXPECTED behaviour: wanbond has no TCP/TLS fallback transport (explicit
      non-goal, docs/install.md §8). Record that the flow does not silently downgrade
      to an unobfuscated or plaintext fallback (there is none — it simply does not
      connect).
- [ ] Where a UDP-allowing network is available again, confirm the tunnel reconnects
      once UDP egress is restored (no manual intervention beyond the network change).
