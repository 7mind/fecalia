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
- [ ] *Optional: measure per-link bandwidth and RTT for pacing tuning* (see
      [install.md §3a](install.md#3a-tuning-per-link-bandwidth-and-pacing) if
      you plan to enable pacing).

The manual items above are automated end to end by the **`just p0-baseline`**
pre-pilot procedure against the two standing worker machines — see
[§P0 — automated real-link baseline](#p0--automated-real-link-baseline-realhosts-tier)
below. Run that command to capture the baseline report, then read/interpret the
numbers by hand; the baseline is INFORMATIONAL (report-only), not a pass/fail
gate.

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
- [ ] Run `sudo -E go test -tags e2e -run '^TestP4AdaptiveFEC$' -v ./test/e2e`
      on the slowest supported worker (including a 1-vCPU aarch64 host); each
      60-second phase must collect at least 2,000 DATA frames.
- [ ] Under steady `P4SteadyLossRate` path loss, adaptive total overhead ≤ the
      fixed-FEC baseline for equal masking.
- [ ] Post-recovery residual loss ≤ `P4ResidualLossMax` (`/metrics`).

## P5 — DPI resistance
- [ ] From a hostile-ish network (e.g. a hotel/guest Wi-Fi), the tunnel connects.
- [ ] Capture the flow; nDPI / Suricata do not classify it as WireGuard or any
      identified VPN.

## P0 — automated real-link baseline (realhosts tier)

The **single, repeatable pre-pilot procedure** that replaces the manual §P0 steps
above. It is a thin orchestration layer over the existing `realhosts` tier (which
drives the two standing worker machines over SSH: the amd64 edge behind symmetric
NAT ↔ the aarch64 concentrator on its public IP) — it does NOT re-implement any
test logic. One command provisions both ends, natively builds `wanbond` on each,
brings the tunnel up over the real internet path, runs the aggregation +
loaded-RTT + link/hub-failover smoke, and TEES a timestamped baseline report.

REPORT-ONLY / NON-BLOCKING (Q19): the orchestrated tests assert **liveness only**
(handshake completed, both paths reached `up`, every iperf3 sample returned a
positive rate, failover recovered) — **no Mbit/s or ms threshold gates the run**.
The emitted numbers are informational input to the operator's pilot-gate
decision, which stays a human judgement call, not an automated gate.

### Run the baseline
- [ ] From the dev shell (`nix develop`) at the repo root, run:

      ```
      WANBOND_SSH_KEY=/run/agenix/llm-ssh-key just p0-baseline
      ```

      `WANBOND_SSH_KEY` defaults to `/run/agenix/llm-ssh-key`, so on a host where
      that key is already in place `just p0-baseline` alone suffices. Host
      addresses/public IP default to the two standing workers and can be overridden
      with `WANBOND_EDGE_HOST` / `WANBOND_CONC_HOST` / `WANBOND_CONC_PUBLIP`. No
      root is required. The command is NEVER part of `just test` or CI.
- [ ] The command orchestrates these EXISTING tests (`go test -tags realhosts
      -run '^(TestRealP0Smoke|TestRealAggregationBufferbloat|TestRealMidTransferWANKill)$' -v`)
      and tees the full `-v` output to
      `test/realhosts/reports/p0-baseline-<UTC-timestamp>.log` (gitignored). A
      **non-zero exit** means the run itself could not complete (a host was
      unreachable or the tunnel never came up) — NOT that a performance number
      missed a target.

### What the baseline report contains
- [ ] **`TestRealP0Smoke`** — single-uplink bring-up: WG handshake OK, ping avg
      RTT (ms), and three iperf3 measurements (single-flow TCP Mbit/s + retransmits,
      8×-parallel TCP Mbit/s + retransmits, UDP goodput/loss/jitter). See the
      `=== P0 SMOKE RESULTS ===` block.
- [ ] **`TestRealAggregationBufferbloat`** — per-path and bonded throughput and
      their **aggregation ratio**, plus the **idle-vs-loaded RTT (bufferbloat)
      delta** measured with a ping running inside a saturating transfer.
- [ ] **`TestRealMidTransferWANKill`** — mid-transfer **LINK-failover** and
      **HUB-failover** (T57) recovery: the observed gap/switch timings before the
      flow resumes over the surviving link / standby concentrator.

### What stays manual
- [ ] **Reading and interpreting the numbers.** The command emits measurements; a
      human decides whether the aggregation ratio, loaded-RTT delta, and failover
      gaps look acceptable for the intended pilot.
- [ ] **The pilot-gate decision itself** is a NON-BLOCKING human call. The baseline
      informs it; it does not automate or gate it. Record the report path, date,
      and the go/no-go decision alongside the numbers.
- [ ] **Exit criterion (Q19):** the capped-fixture aggregation/bufferbloat
      measurement (netns, `TestFixtureImpairment`, W2) PLUS this report-only
      real-link baseline (`just p0-baseline`, W4) are SUFFICIENT to proceed to a
      SUPERVISED pilot. The longer soak runs DURING the pilot, NOT as a pre-gate.
      Full statement:
      [runbook.md §7 Pilot exit criterion](runbook.md#7-pilot-exit-criterion-non-blocking).

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

### Hub failover: active concentrator goes fully unreachable (T57)
Distinct from the per-uplink drops above: here the *concentrator* is lost, so NO
uplink can reach it and the edge must move to a STANDBY concentrator. Requires a
SECOND concentrator VPS reachable from the edge, sharing the peer's SAME WireGuard
static key (the standby presents the same peer identity). Configure the edge peer
with an ORDERED list — `endpoints = ["<hubA ip:port>", "<hubB ip:port>"]` (index 0
= hubA active, hubB standby); IP:port only, no hostnames.
- [ ] Bring the tunnel up; confirm traffic flows via hubA (`ping -i 0.2 10.77.0.1`
      steady; hubA journal shows the handshake + the edge endpoint learned).
- [ ] Make hubA fully unreachable from the edge — stop `wanbond-concentrator` on
      hubA, OR block its `listen_port` at hubA's firewall (a REAL hub outage, so
      every path's liveness to hubA goes DOWN together, not just one uplink).
- [ ] Within the hub-failover budget (all-paths-DOWN detection ≈ `DownAfter` +
      the `hubFailoverSettle` 3 s dwell), the edge journal shows a
      `hub failover: all paths to active concentrator down; switched endpoint`
      line advancing to hubB, and hubB's journal shows a FRESH handshake (a new
      session — no hub-to-hub state handoff). Record the observed gap.
- [ ] The flow re-establishes over hubB. (A long-lived TCP flow tied to the old
      session resets — a fresh session is deliberate; a NEW flow, or ping,
      resumes.)
- [ ] Single-concentrator GUARD: with the edge configured with only ONE endpoint
      (legacy single `endpoint`, or a one-element `endpoints`), repeat the hub
      outage — the edge must take NO failover action (no `hub failover` journal
      line, no endpoint switch); behaviour is identical to pre-T57. Recovery
      happens only when hubA itself returns.

### Startup with a not-yet-assignable path (tolerant bind)
- [ ] Bring one uplink's interface DOWN (so its configured `source_addr` is not held
      by any interface), then `systemctl restart wanbond-edge`. The daemon comes up
      instead of crash-looping: journal shows the tunnel bound on the surviving
      uplink and the absent path recorded as deferred / `Down`; a NEW flow passes end
      to end over the survivor. Then bring the interface back UP WITHOUT restarting:
      the background reconcile (T55) re-binds and promotes the deferred path
      automatically within ~1 s (`DefaultReconcileInterval`), with no `restart` — and
      both paths then carry traffic.
- [ ] With EVERY uplink's `source_addr` absent, `systemctl restart wanbond-edge`
      FAILS fast (journal shows a fatal "no configured path could bind" and the unit
      enters `failed` / restart-loops) — no transport means no tunnel.
- [ ] A MALFORMED `source_addr` in the config still fails at config load with a
      validation error, distinct from the tolerated not-yet-assignable case.

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

## D65 — pacing field validation (bufferbloat control)

Defect D65 identifies bufferbloat (standing queue build-up under sustained load)
on last-mile links (observed on Starlink, D65) that caps single-flow TCP throughput
at ~3.67 Mbps against a link independently capable of ≥6.9 Mbps. Pacing (per-path
token buckets, enabled via `[scheduler] pacing_enabled = true`) bounds the queue
so loaded RTT stays near idle baseline, and single-flow TCP can saturate to the link's
true rate. This section validates pacing's effectiveness on the real deployment
(Pi4-edge/Starlink/o3 topology).

**IMPORTANT:** The netns/e2e fixture test suite MUST NOT assert absolute
throughput thresholds (e.g., "single-flow TCP ≥ X Mbps"). This is a
**manual/real-host tier validation only**, because the netns fixture is
CPU-bound and cannot build the standing queues pacing is designed to prevent.
Absolute throughput assertions belong in this section (real links), never in
automated netns e2e tests. `TestFixtureImpairment` and any similar capped-fixture
netns tests remain throughput-measurement **report-only** (informational) with
no pass/fail gate on absolute numbers (see [design.md pacing section](design.md#send-side-scheduler--internalsched)).

### Setup and prerequisites

- [ ] Edge and concentrator both have the tunnel up with both uplinks alive (per §P1
      setup). Both daemons running from `0600` configs, `/metrics` reachable on
      `127.0.0.1:9090` each end.
- [ ] Record date, `wanbond version` output, current build (`git log --oneline -1`).
- [ ] Measure and record the **idle RTT** and **measured throughput** per uplink
      (instructions in [install.md §3a](install.md#3a-tuning-per-link-bandwidth-and-pacing));
      these become the basis for pacing config (`link_bandwidth` / `link_rtt`).

### Test 1: Three-way iperf3 attribution (no pacing)

This test measures throughput under three distinct tunnel configurations to
isolate wanbond's egress path (the pacing effect) from network capacity:

#### 1a. Direct WAN, no tunnel

Baseline: throughput on each uplink without wanbond. Brings one uplink down to
measure each independently:

**Concentrator (server, for both sub-measurements):** `iperf3 -s -p 5201`

**Starlink only (5G disabled or down):**
- [ ] Edge: `iperf3 -c <concentrator-public-ip> -p 5201 -t 20`
      (FORWARD mode — edge SENDS — so this measures edge UPLOAD/egress, the same
      direction wanbond paces and that legs 1b/1c measure; adjust port if the
      concentrator listens elsewhere)
- [ ] Record the **edge upload throughput** (edge's iperf3 summary). This is the
      Starlink solo UPLOAD capacity — the direction the pacer shapes.

**5G only (Starlink disabled or down):**
- [ ] Edge: `iperf3 -c <concentrator-public-ip> -p 5201 -t 20`
- [ ] Record the **edge upload throughput**. This is the 5G solo upload capacity.

Record both as `T_starlink_direct` and `T_5g_direct` (Mbit/s, UPLOAD direction — so
they are valid baselines for the upload-shaped tunnel legs 1b/1c).

#### 1b. Through tunnel, pacing OFF (default)

Single-flow TCP through the tunnel with pacing disabled (the default behavior
that exhibits D65 bufferbloat). Both uplinks up, active-backup scheduler:

- [ ] Edge: `systemctl stop wanbond-edge` (stop the daemon if running from pacing-enabled
      config, or confirm config has `pacing_enabled = false` / omitted).
- [ ] Edge: Verify config `[scheduler]` does NOT contain `pacing_enabled = true`
      and NO `link_bandwidth` / `link_rtt` are declared on `[[paths]]` blocks:
      ```sh
      grep -A 5 '\[scheduler\]' /etc/wanbond/edge.toml
      grep 'link_bandwidth\|link_rtt' /etc/wanbond/edge.toml
      ```
      (should show nothing for pacing-related lines, or `pacing_enabled = false`).
- [ ] Edge: `systemctl start wanbond-edge`; wait for tunnel up (journal shows both
      paths `up`), then `ping -c 3 10.77.0.1` succeeds.
- [ ] Concentrator: `iperf3 -s -B 10.77.0.1`
- [ ] Edge: `iperf3 -c 10.77.0.1 -t 30` (30-second single-flow TCP, allows queue to
      build to steady state)
- [ ] Record the **mean throughput** (final summary line of iperf3 output).
      Record as `T_tcp_pacing_off` (Mbit/s).
- [ ] In a separate terminal on edge, while iperf3 is running:
      `ping -i 0.1 10.77.0.1 | tee ping-pacing-off.txt`
      Let it run for the full iperf3 duration, then note the **min/avg/max/stddev** RTT.
      Record as `RTT_pacing_off` (ms, especially the **avg** and how much it increased
      from idle baseline).
- [ ] On edge, while iperf3 is still running, read retransmits:
      ```sh
      watch -n 1 'ss -i | grep -A 1 10.77.0.1'
      ```
      Note the `Recv-Q`, `Send-Q`, and retransmit count. Record the peak retransmit
      count observed as `Retrans_pacing_off`.

#### 1c. Through tunnel, pacing ON

Single-flow TCP through the tunnel with pacing enabled. This test uses the
same topology but with pacing active, demonstrating the fix. Both uplinks up,
active-backup scheduler with pacing:

- [ ] Edge: `systemctl stop wanbond-edge`
- [ ] Edge: Update the edge config to enable pacing and declare link properties.
      Edit `/etc/wanbond/edge.toml`, find the `[scheduler]` block and add
      `pacing_enabled = true`, then add `link_bandwidth` and `link_rtt` to each
      `[[paths]]` block:
      ```toml
      [scheduler]
      pacing_enabled = true
      
      [[paths]]
      name = "starlink"
      source_addr = "192.168.1.10"          # adjust to your interface
      link_bandwidth = "50Mbit"               # from §Setup measurement
      link_rtt = "21ms"                       # from §Setup measurement
      
      [[paths]]
      name = "5g"
      source_addr = "192.168.2.10"           # adjust to your interface
      link_bandwidth = "10Mbit"               # adjust per your measurement
      link_rtt = "45ms"                       # adjust per your measurement
      ```
      (Use your actual measured values; values shown are placeholders.)
- [ ] Edge: `systemctl start wanbond-edge`; wait for tunnel up.
      Verify pacing loaded without errors:
      ```sh
      journalctl -u wanbond-edge -n 20 | grep -E 'config|pacing|Pacing'
      ```
      Should see `config loaded` or `config reloaded` with no errors.
- [ ] Concentrator: `iperf3 -s -B 10.77.0.1`
- [ ] Edge: `iperf3 -c 10.77.0.1 -t 30` (same 30-second single-flow TCP)
- [ ] Record the **mean throughput** from the final summary. Record as
      `T_tcp_pacing_on` (Mbit/s).
- [ ] In a separate terminal on edge, while iperf3 is running:
      `ping -i 0.1 10.77.0.1 | tee ping-pacing-on.txt`
      Let it run for the full iperf3 duration, then note the **min/avg/max/stddev** RTT.
      Record as `RTT_pacing_on` (ms, note the **avg** and bufferbloat delta from idle).
- [ ] On edge, while iperf3 is still running:
      ```sh
      watch -n 1 'ss -i | grep -A 1 10.77.0.1'
      ```
      Note retransmits. Record the peak retransmit count as `Retrans_pacing_on`.

### Expected observations (D65 validation)

Record your measurements in the table below. These values define success: pacing
enables single-flow TCP to saturate toward the link's true capacity (UDP goodput),
loaded RTT stays near idle baseline (no standing queue), and retransmits drop.

| Metric | Pacing OFF | Pacing ON | Expected (pre-fix baseline) | Status |
|--------|-----------|-----------|-------|--------|
| **Single-flow TCP (Mbit/s)** | `T_tcp_pacing_off` | `T_tcp_pacing_on` | OFF: ~3.67 (bufferbloated); ON: approaches `T_starlink_direct` (~6.9 in D65 pre-fix test) | ✓ if ON ≥ 1.5× OFF |
| **Idle RTT (ms, baseline)** | — | — | (from ping before iperf3, e.g. ~21 ms) | — |
| **Loaded RTT (ms, during iperf3)** | `RTT_pacing_off` | `RTT_pacing_on` | OFF: inflates to ~1000+ ms (standing queue); ON: stays within 5–10 ms of idle | ✓ if ON is close to idle |
| **Loaded RTT delta (Δ, ms)** | `RTT_pacing_off - idle` | `RTT_pacing_on - idle` | OFF: ~980 ms; ON: ~5 ms | ✓ if ON ≪ OFF |
| **Retransmits (peak count)** | `Retrans_pacing_off` | `Retrans_pacing_on` | OFF: ~13 per 10s; ON: <1 per 10s | ✓ if ON ≪ OFF |

### Interpretation

- **TCP throughput:** If pacing ON (`T_tcp_pacing_on`) is significantly higher
  than pacing OFF, and approaches the measured UDP goodput or solo-uplink capacity,
  pacing is working — bufferbloat was indeed capping the flow.
- **RTT inflation:** If loaded RTT stays near idle under pacing ON, the queue is
  bounded as designed. If it still inflates to ~1s under pacing ON, the declared
  `link_bandwidth` or `link_rtt` may be incorrect (re-measure per
  [install.md §3a](install.md#3a-tuning-per-link-bandwidth-and-pacing)).
- **Retransmit drop:** Fewer retransmits under pacing ON indicate a more stable path
  (no TCP timeout storms from queue delay). Compare counts, not just raw numbers.

### Record and close-out

- [ ] Date: _____________
- [ ] `wanbond version`: _____________
- [ ] Build (`git log --oneline -1`): _____________
- [ ] Idle RTT per uplink (from Step 1 measurement): _____________
- [ ] Measured throughput per uplink (from Step 1 measurement): _____________
- [ ] Test 1a (direct WAN, no tunnel):
  - Starlink direct: _____________
  - 5G direct: _____________
- [ ] Test 1b (through tunnel, pacing OFF):
  - Single-flow TCP: _____________
  - Idle RTT: _____________
  - Loaded RTT: _____________
  - Retransmits: _____________
- [ ] Test 1c (through tunnel, pacing ON):
  - Single-flow TCP: _____________
  - Idle RTT: _____________
  - Loaded RTT: _____________
  - Retransmits: _____________
- [ ] Go/no-go decision:
  - Bufferbloat controlled (loaded RTT ≈ idle)? YES / NO
  - TCP throughput improved with pacing? YES / NO
  - Configuration valid (no load errors)? YES / NO
  - Proceeding with pacing enabled for field deployment? YES / NO
  - Notes: _____________
