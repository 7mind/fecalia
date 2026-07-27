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
- [ ] Run
      `go test ./internal/bind ./internal/reseq ./internal/telemetry -run 'TestAdaptiveControllerUsesAuthenticatedActiveCarrierDataLoss|TestAdaptiveControllerIgnoresSingleCarrierFeedbackWhileWeighted|TestDataLossFeedback|TestFeedbackOnly|TestMixedVersionFeedbackCadencePreservesBaseRecoveryWindow|TestLossObserverReportsExactFinalizedSequenceRange|TestProbePayload' -count=1`
      and repeat with `-race`. This pins final-gap raising, inferred
      parity-recovered loss, exact outcome conservation, carrier-transition
      non-influence for final and recovered prior-epoch outcomes, clean
      dwell/shedding, stale/replay/identity rejection, per-peer isolation,
      bidirectional legacy recovery negotiation under ordered and reordered
      delivery, feedback-only evidence preservation and exact priority
      accounting, and the weighted multi-carrier boundary.
- [ ] Run `sudo -E go test -tags e2e -run '^TestP4AdaptiveFEC$' -v ./test/e2e`
      on the slowest supported worker (including a 1-vCPU aarch64 host); each
      60-second phase must collect at least 2,000 DATA frames.
- [ ] Under steady `P4SteadyLossRate` path loss, adaptive total overhead ≤ the
      fixed-FEC baseline for equal masking.
- [ ] Post-recovery residual loss ≤ `P4ResidualLossMax` (`/metrics`).
- [ ] On an active-backup real link, perform two independent loaded 30-second
      cycles from a clean `M=0` baseline. For each cycle record
      `wanbond_path_loss_ratio`, `wanbond_fec_eligible_path_loss`,
      `wanbond_fec_adaptive_parity`, `wanbond_fec_recovered_packets_total`, and
      `wanbond_fec_residual_loss_ratio`: bounded active-carrier DATA loss must
      make eligible loss and M non-zero; a subsequently missing DATA frame must
      increment recovery without TCP-equivalent residual loss; after clearing
      the impairment, M must return to zero only after the configured controller
      dwell. Preserve both metric captures with the commit/config hashes.

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
- [ ] **Exit criterion (Q19):** the capped-fixture functional
      impairment/counter check (netns, `TestFixtureImpairment`, W2; no
      throughput or loaded-RTT claim) PLUS this report-only real-link baseline
      (`just p0-baseline`, W4) are SUFFICIENT to proceed to a SUPERVISED pilot.
      The longer soak runs DURING the pilot, NOT as a pre-gate. Full statement:
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
# per_path_capacity_fps sizes the aggregation gate to ~one uplink's WIRE frame
# rate (data + any FEC parity); tune it to the slower uplink's sustained wire
# frame rate (bytes/s ÷ on-wire frame bytes, install.md §3a).
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
at ~3.67 Mbps against a link independently capable of ≥6.9 Mbps. Pacing
(bounded per-(peer,path) exact-byte shapers, enabled via
`[scheduler] pacing_enabled = true`) applies backpressure instead of
pacer-induced loss, bounds the queue so loaded RTT stays near idle baseline,
and lets single-flow TCP approach the declared link rate. This section
validates pacing's effectiveness on the real deployment
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

### T304 synchronized RPi4-to-o3 field acceptance

Run on the RPi4 edge with Starlink active, 5G standby, active-backup policy,
and adaptive FEC enabled. Preserve the starting state before the first cycle:

Before the real-link cycles, validate the T309/T318/T323 FEC sender-owner invariants:

- [ ] Run
      `go test -tags failfirst ./internal/bind -run '^TestFailFirstFEC' -count=1`
      and the same command with `-race`; both pass. These deterministic checks
      assert that open-group DATA/PARITY stays hidden, exact deadline dispatch
      stays within the bind-local 10ms grace, an expired group wins over queued
      admissions, a 257-frame `Send` publishes one owner batch and preserves
      exact payload/sequence order, writer-prefix failure consumes no suffix
      sequence numbers, shaped and exclusive unshaped-recovery serial sub-*K*
      sends acknowledge owned caller buffers before terminal group service,
      uncontracted direct sends stay synchronous, post-ack failures retire only
      the exact generation, and Close rejects unowned suffixes while joining
      every acknowledged owner-side completion.
- [ ] Run
      `go test ./internal/bind -run 'TestProductionBatch128CappedShapedSerialAdmissionFillsGroups|TestFECSendStreamsBeyondOwnerMailboxCapacity|TestMultipathFECDeadlineEmitsPartialGroupParity' -count=1`;
      the capped production batch-128 fixture, 257-buffer offload batch, and
      underfilled deadline group all pass. The production fixture must report
      exact size-closed *K*+*M* fill, zero deadline decisions/misses and terminal
      errors, accepted=emitted conservation, and nonzero configured-vs-observed
      byte-rate/count evidence.
- [ ] During one Down/Up cycle with a deliberately underfilled FEC group,
      capture the outer UDP stream. Confirm no DATA/PARITY from the old group
      appears after the old transport generation closes; the first post-Up
      group starts from a fresh FEC sender generation.

- [ ] Record the edge and o3 commit IDs, binary hashes, config hashes, service
      states, and the original pacing declarations. The last step must restore
      and re-hash this exact state.
- [ ] Use one 30-second edge-to-o3 TCP upload per tunnel leg, discarding the
      first 5 seconds as warmup. During the whole leg collect timestamped loaded
      ping, one-second `iperf3` intervals/retransmits, FEC counters, and every
      `wanbond_path_shaper_*` series. Take synchronized counter scrapes at
      `t=0`, the final-window boundary `t=10s`, and the leg end `t=30s` (or
      continuously sample with timestamps at least this precisely). Compute the
      final-20-second outer rate and counter deltas strictly from `t=10s` through
      `t=30s`; retain the `t=0` scrape for whole-leg error/cancellation checks.
- [ ] Run three cycles with condition order rotated to reduce order bias:

      | cycle | first | second | third |
      |---|---|---|---|
      | 1 | pacing off | `8Mbit` | `5Mbit` |
      | 2 | `8Mbit` | `5Mbit` | pacing off |
      | 3 | `5Mbit` | pacing off | `8Mbit` |

      Pacing-off uses the direct complete-batch path. Each paced condition sets
      the active Starlink `link_bandwidth` to the named cap and keeps valid
      declarations on every path; wait for both paths and the WG session to
      return UP before the 5-second warmup.
- [ ] Immediately before and after every `8Mbit` or `5Mbit` tunnel leg, stop
      tunnel load and measure direct Starlink upload to o3 in the same direction.
      Treat that bracket as a valid raw pair only when **both** raw measurements
      are at least `1.03 * cap` and
      `abs(raw_before-raw_after)/max(raw_before,raw_after) <= 0.15`.
      A raw-invalid interval is inconclusive and cannot close the criterion.
      Repeat as needed until at least **two valid raw pairs per cap** remain.
- [ ] Capture `iperf3 --json` receiver bytes/retransmits and timestamped
      `ping -i 0.1` samples so loaded RTT p95 and retransmits per GiB of receiver
      payload can be calculated, rather than inferred from summary averages.

For each cap, at least two raw-valid paired cycles must satisfy all of these
predeclared gates:

- [ ] Over each paced leg's final 20 seconds, total outer egress — shaped
      emitted bytes plus successful direct outer-priority bytes — stays within
      **±5%** of the declared cap.
- [ ] No ordinary queue loss occurs: no scheduler-shedding record appears, and
      deltas for admission-canceled datagrams, terminal shaped-call errors, and
      asynchronous generic/`EMSGSIZE` writer errors are all zero.
- [ ] TCP receiver goodput is at least **70%** of emitted DATA bytes. Reconcile
      DATA, FEC parity, inner control, and direct outer-priority bytes rather
      than treating parity as application goodput.
- [ ] Each paced loaded-RTT p95 is at most **60%** of its cycle's unpaced
      loaded-RTT p95.
- [ ] Across the retained valid runs, median retransmits/GiB at `5Mbit` are at
      most **1.25×** median retransmits/GiB at `8Mbit`.
- [ ] While the paced upload fills the DATA budget, confirm every configured
      path remains live and
      `wanbond_path_probe_send_errors_total` does not increase. Generated
      authenticated PROBE/echo frames reserve retained priority capacity and
      use the same serialized path writer as DATA/control/recovery. The counter includes
      unexpected socket-write failures for locally-originated ordinary and PMTU
      PROBE frames: an ordinary failure is counted but not returned to a caller,
      while an unexpected PMTU failure is counted and returned to discovery.
      Expected PMTU `EMSGSIZE` too-large verdicts and reactive reflected-echo
      write failures are excluded. A padded PMTU request substitutes in an
      eligible local probe slot; the following slot must be ordinary liveness
      before another PMTU attempt. PMTU does not add a second local producer,
      while reactive echo replies remain immediate. Confirm the PMTU timestamp
      begins in its selected slot (cadence wait does not inflate the RTT sample).
- [ ] During a paced transfer, remove and re-add the standby path through the
      normal config reload. Confirm the transfer on the retained active path
      continues, the removed path emits no post-removal datagrams, and its
      replacement begins with zero `wanbond_path_shaper_*` counters (a fresh
      shaper/socket generation).
- [ ] With pacing off, confirm
      `curl -s http://127.0.0.1:9090/metrics | grep wanbond_path_shaper_`
      returns no series. Enable pacing and confirm the series appear for each
      paced path with fixed `path` (and, where applicable, `peer`) labels.
- [ ] During a paced saturated transfer, confirm
      `shaper_data_budget_bytes >= shaper_max_datagram_bytes` (`B>=Lmax`),
      `shaper_control_reserve_bytes = shaper_max_datagram_bytes` (`C=Lmax`),
      and `shaper_queue_budget_bytes =
      shaper_data_budget_bytes + shaper_control_reserve_bytes` (`Q=B+C`).
      Also confirm
      `shaper_queue_data_bytes <= shaper_data_budget_bytes`,
      `shaper_queue_control_bytes <= shaper_control_reserve_bytes`, and
      `shaper_queue_bytes <= shaper_queue_budget_bytes`, with
      `shaper_priority_retained_bytes <= shaper_priority_burst_bytes`,
      `shaper_fec_group_owned_bytes` equal to either zero or the configured
      `Fgroup`, and `shaper_memory_retained_bytes <=
      shaper_memory_bound_bytes` (`Mtotal=B+C+P+Fgroup+Lio`). At full B/Q,
      `shaper_admission_waits_total` and
      `shaper_admission_wait_seconds_total` should rise while asynchronous
      generic/`EMSGSIZE` errors and canceled datagrams remain flat.
- [ ] Read `shaper_priority_debt_bytes` (`P0`),
      `shaper_rate_bytes_per_second` (`R`),
      `shaper_priority_rate_bytes_per_second` (`Rp`), and
      `shaper_priority_burst_bytes` (`Pburst`). Confirm the exported
      `shaper_priority_delay_bound_seconds` equals
      `(P0+Pburst)/(R-Rp)`, including on the configured path with the largest
      `Rp/R` ratio.
- [ ] After traffic quiesces, reconcile
      `accepted_bytes = emitted_bytes + async_write_error_bytes +
      async_write_emsgsize_bytes + queue_bytes + in_flight_bytes`. Compare
      emitted parity bytes with `wanbond_fec_repair_bytes_total`; direct
      generated PROBE/echo bytes remain separately visible in
      `shaper_outer_priority_bytes_total`. Repeat during load: accepted bytes
      linearize at queue reservation, so a pending-copy placeholder appears in
      both `accepted_bytes` and `queue_bytes`; cancellation counts only buffers
      that never reserved capacity.
- [ ] With FEC active on a naturally single-path group, saturate B/C/P and
      confirm the complete native DATA+parity tranche finishes before the
      derived `A` and before 250 ms for first-, middle-, and last-DATA loss.
      Confirm later probe/echo arrivals do not enter the tranche and a second
      group backpressures. Mixed-path/shared-socket groups must report the
      finite contract disabled and retain the conservative fallback.
- [ ] Capture ordinary unpadded PROBEs during FEC+pacing operation. Confirm each
      contract-bearing OFFER/ACK is 102 outer bytes (75-byte base plus the
      canonical 27-byte payload), remains within `P=2*Lmax`, and increments
      `shaper_outer_priority_bytes_total` by its exact wire size. Repeat with
      either FEC or pacing off and confirm ordinary probes remain the legacy
      75-byte form; padded PMTU probes must carry no contract. While a
      conservative FEC gap is armed with no acknowledged receiver venue, send
      legacy probes faster than 250 ms and confirm they neither advance the
      receiver generation nor move the original deadline. After acknowledging
      a venue, confirm the first legacy probe revokes it and later legacy
      probes remain idempotent. Repeat while the ACK echo write is blocked
      after admission: the first legacy probe must invalidate that admission,
      and the stale write completion must not restore a venue. Fail an ACK
      write terminally and confirm the next legacy probe moves neither
      generation nor deadline. With two concurrent admissions, fail one and
      confirm the survivor remains revocable; fail both and confirm no
      revocable evidence remains.
- [ ] During a saturated transfer, remove/re-add the standby path. Confirm
      DATA/inner-control pauses while PROBEs continue, the old staged FEC group
      completes, no old-service write appears during the 250 ms quiet interval,
      and the retained path resumes either after the fresh authenticated ACK or
      at the 250 ms legacy fallback. OuterSeq and FEC GroupID must remain
      monotonic. Replay the old OFFER and an inconsistent same-identity OFFER;
      neither may produce an ACK or re-enable fast recovery.
- [ ] Drop the first OFFER, the first renewal, and a later renewal at the 200 ms
      probe cadence. Confirm DATA resumes conservatively at 250 ms without
      retiring the live OFFER, a later exact ACK succeeds only while at least
      250 ms of its own validity remains, and a lost renewal disables fast
      recovery before the prior lease becomes unsafe. With an incomplete
      receiver FEC group, accept a higher same-session ContractID carrying the
      identical service value and confirm the group still recovers; repeat with
      a changed value or new SessionID and confirm the incomplete group clears
      before ACK. Hold an old OFFER's socket write across a ContractID rotation
      and confirm its completion cannot authorize the new identity.
- [ ] With active-backup FEC and an exact successfully emitted ACK, record `A`
      and the authenticated SRTT of the current DATA carrier. An idle Up backup
      must neither inflate `H` nor gate freshness. Lose one
      DATA frame on the ACKed composite path/source and confirm the receiver
      releases at
      `W=min(250ms,A+clamp(4*max(SRTT),10ms,250ms))`: repair at `W-1ns`
      fills exactly once in order, while repair at `W` loses the gap and
      releases the successor. Repeat with no ACK, wrong path/source, a second
      delivering key, absent/stale RTT, less than 250 ms contract/RTT validity,
      `A>=250ms`, weighted policy, rebaseline, and membership/session/contract
      transitions; every case must use or re-arm a fresh 250 ms fallback. Hold
      an old ACK completion and an old window refresh across each transition,
      source roam, resequencer replacement, and teardown; neither may restore
      fast evidence. Force a bounded-window advance that exposes a second gap
      near the first gap's expiry and confirm the second gap receives a fresh
      full hold with strictly ascending, exactly-once delivery.
- [ ] Pause one recovery-window refresh after it snapshots a primary-only ACK
      venue and lower RTT. Complete a standby ACK and record a newer higher
      authenticated RTT sample, publish the newer refresh, then release the old
      refresh. Confirm the standby venue remains usable and `H` retains the new
      maximum RTT. Repeat with the old refresh paused after publication-revision
      reservation to cover reversed resequencer commit order. If a gap was
      already armed before the same-topology update, confirm its exact deadline
      does not change and the new venue/RTT applies only to the next gap.
- [ ] Pause each topology transition after the coordinator generation advances
      but before the explicit resequencer publication: ContractID renewal and
      service change, SessionID adoption, membership add/remove, same-key roam,
      rebaseline, resequencer replacement, and teardown. At the old `W`
      boundary, concurrent receive/`Pop` must not release; it must observe the
      authoritative generation and set the fallback deadline to the topology
      transition instant plus 250 ms, not the observation instant plus 250 ms.
      Pause receive until after that bound and confirm immediate release. Burst
      several advances while receive remains parked and confirm their wake
      coalesces to the latest coherent generation/time without allocating
      another timer or spinning; Close must still prevent post-close delivery.
      Repeat with FEC off and confirm its existing non-FEC gap deadline remains
      unchanged.
- [ ] Change same-name pacing/FEC service inputs (`R/Rp/B/C/P/Lmax/Kdata/Mmax/I`)
      and reload. Confirm the daemon warns that the change remains unapplied
      until restart and the running service retains its old ContractID. After a
      daemon restart, confirm the peer adopts a new authenticated SessionID.
      Separately force an engine Bind Close→Open within one process and confirm
      ContractID rotates while SessionID, OuterSeq, and FEC GroupID remain
      monotonic.
- [ ] Delay a FEC deadline decision beyond the dispatch grace. Confirm the
      current group completes, the next DATA group blocks immediately, and a
      fresh OFFER appears only after the asynchronous drain plus 250 ms quiet
      interval; Close during that interval must cancel the waiter. Reopen before
      releasing an old-generation deadline worker and confirm it neither
      invalidates nor rotates the replacement contract. Repeat through two rapid
      Close/Open generations, queue a current-generation miss while the stale
      worker releases ownership, and confirm the current request still reaches
      an exact ACK or the 250 ms fallback rather than leaving DATA blocked.
- [ ] Force recovery deadline-install, clear, and running-writer failures.
      Confirm each exact socket generation immediately rejects admission,
      disappears from its peer/scheduler/remote view, and quiesces on Close;
      every already-accepted owner-side terminal completion must preserve the
      originating cause (a shaped caller already acknowledged at ownership is
      not retroactively failed),
      and accepted bytes must reconcile exactly into emitted plus generic or
      `EMSGSIZE` terminal bytes with no retained remainder. For an interrupted
      in-flight syscall, confirm completion reports the published cause while
      call/byte metrics retain the actual syscall error class;
      then reopen and confirm a delayed stale failure cannot retire the new
      generation.
- [ ] During probe saturation, distinguish bounded priority outcomes:
      `wanbond_path_probe_priority_coalesced_total` may rise for skipped
      ordinary cadences, `wanbond_path_pmtu_admission_canceled_total` only when
      generation close cancels a wait for `P` before probe generation (not an
      `ErrClosed` from generation or writer after reservation), and
      `wanbond_path_echo_priority_overflow_total` only for non-blocking reactive
      echo drops. None should coincide with receive-loop blocking.
- [ ] Cycle the edge daemon Down/Up once with pacing enabled. Confirm no send
      remains blocked, the tunnel reconnects, and the journal contains no
      post-close socket-write errors from the prior generation. Repeat several
      cycles when validating a lifecycle change; goroutine count should return
      to its pre-cycle baseline.

For interpreting a transient, let `P0` denote generated-priority debt at the
start of a send call. With one coincident post-call `Pburst=2*Lmax`, sustained
generated priority no greater than `Rp=Pburst/200ms`, and configured byte rate
`R>Rp`, admission is bounded by
`Dp=(P0+Pburst)/(R-Rp)` (not `P0/R`). Once admitted, local egress adds at most
`Q/R+Lmax/R`. For an exclusive single-path FEC recovery cut, additionally use
`A=I=10ms`; it must remain below 250 ms. The prefix `B+C+P` runs before any
successor DATA can arm a receiver gap and therefore does not enter `A`.
Therefore call-to-receiver delivery is bounded by
`Dp+Q/R+Lmax/R` plus the active resequencer hold for a missing lower outer
sequence. Record any observed inner-control call and check it against that
complete bound. These bounds cover the built-in PROBE/echo producer, including
PMTU probes that occupy ordinary local cadence slots. Priority arrivals in the
half-open interval
`[call, call+Dp)` update the registered waiter under the shaper lock even when
its goroutine does not observe the change until the former deadline; once it
matures, an exact-boundary debit changes future reservations but cannot revoke
the current call's eligibility, so admission needs no extra timer or packet
step. Do not rely on these bounds if a future authenticated outer CONTROL
producer sustains traffic beyond the declared `Rp`/`Pburst` model; that condition
constitutes explicit overload.

Canonical record notation: `D=250ms`; `G=10ms`; `F=1200ms`;
`B/C/P/Fgroup/Lio/Mtotal`; `R/Rp/I`; `Sdevice`;
`H=clamp(4*max(SRTT),10ms,D)` over qualified fresh DATA carriers only;
`W=min(D,A+H)`; `Ecompletion`. Record
`SessionID` restart versus same-process `ContractID` rotation separately and
verify `OuterSeq` continuity across rotation. Require the exact authenticated
`ACK` and `A+H<D` for fast recovery; otherwise require installed `W=D` and
record the labelled fallback, including `saturated`. Validate
geometry from config/contract state—there is no zero-parity inference.

- [ ] With pacing enabled on the edge and disabled on a single-peer
      concentrator, confirm both directions publish and ACK `A=10ms`, report
      `fast_eligible=1` when `A+H<D`, and install `W<250ms`. A shared
      multi-peer concentrator socket must continue to publish disabled.
- [ ] Under deterministic or recorded sparse loss, confirm several gaps whose
      successors arrived within one `W` release at their observation-based
      deadlines rather than accumulating one fresh `W` per gap. A successor
      first observed later must retain its remaining recovery interval.

- [ ] During the run, capture FEC staged/decision/deadline series, recovery
      contract status/events/reasons/freshness for both bounded
      `sender`/`receiver` directions, receiver RTT age/H/W, shaper
      outer-priority outcomes/cut/high-water series, and resequencer
      arm/deadline/wake/fill series. Confirm the monitor reports the same
      current values.
- [ ] Confirm `group_decisions_total` reconciles with resolved staged groups,
      each active cut's datagram membership reconciles with its socket-call
      delta, and aggregate accepted/emitted/error bytes remain the cumulative
      authority while DATA/control queue gauges report current pressure.

### T304 result record

Record every cycle, including invalid raw brackets; do not silently discard an
interval. For each leg retain the raw before/after values, raw-valid verdict,
final-20-second outer rate, receiver goodput/emitted-DATA ratio, loaded RTT p95,
retransmits/GiB, queue maxima, error/cancellation deltas, and DATA/parity/
priority reconciliation. Summarize how many raw-valid pairs remain at each cap
and the medians used for the `5Mbit` versus `8Mbit` retransmit comparison.

The field gate passes only when at least two raw-valid pairs per cap meet every
threshold above. A below-cap or unstable raw bracket is **inconclusive**, not a
product failure and not a passing sample.

### Record and close-out

- [ ] Date: _____________
- [ ] Starting edge/o3 commits, binary hashes, config hashes, service states:
      _____________
- [ ] Idle RTT per uplink (from Step 1 measurement): _____________
- [ ] Measured throughput per uplink (from Step 1 measurement): _____________
- [ ] Cycle 1 (`off -> 8 -> 5`) report/artifact path: _____________
- [ ] Cycle 2 (`8 -> 5 -> off`) report/artifact path: _____________
- [ ] Cycle 3 (`5 -> off -> 8`) report/artifact path: _____________
- [ ] Valid raw pairs: `8Mbit` _____ / `5Mbit` _____ (each must be >=2).
- [ ] Threshold table and exact-byte reconciliation: PASS / FAIL / INCONCLUSIVE
- [ ] Restore original binaries/configs/service states on edge and o3; record
      final hashes and confirm they equal the starting hashes: _____________
- [ ] Go/no-go decision:
  - T304 exact field gate: PASS / FAIL / INCONCLUSIVE
  - Configuration/envelope valid (`B>=Lmax`, `C=Lmax`, `Q=B+C`,
    `Rp<R`, exact `Pburst`/`P0`/`Dp`): YES / NO
  - Proceeding with pacing enabled for field deployment? YES / NO
  - Notes: _____________
