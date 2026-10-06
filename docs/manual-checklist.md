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
- [ ] On both roles with `[monitor].listen` enabled, `sudo wanbond monitor`
      shows new frames every second; Ctrl+C restores the terminal. Check
      `sudo wanbond monitor --once` emits plain text and exits.
- [ ] On a multi-exit edge, `sudo wanbond set-exit <standby>` moves the
      monitor's active exit to that peer with `Policy <standby>`, and
      `sudo wanbond set-exit auto` shows `Policy auto`; an unknown name exits
      non-zero with the daemon's 400 message.
- [ ] Open the web monitor at desktop and phone widths. Switch the system
      between light and dark appearance with the page open; text, status colors,
      sparklines, and controls remain readable without horizontal scrolling.
      Check WG-session status in the top bar and per-peer status beside each
      peer name. Use the keyboard to select an exit and expand Path details;
      verify the exit selector's focus and expanded details survive live updates.
- [ ] With Amnezia obfuscation enabled, run
      `(cd third_party/amneziawg-go && go test -race ./device -run '^(TestJunkPacketsConcurrentUse|TestProtocolStateIsPerDevice)$' -count=5)`;
      concurrent junk generation reports no race and devices keep independent
      Amnezia settings.

## Adaptive transport and autonomous VM verification

For adaptive-policy comparisons, measure each bound physical uplink immediately
before its tunnel phase and bracket the candidate with the deployed baseline.
Record offered rates, executable hashes, boot identities, local-clock voice
windows, receiver goodput bounds and RX+TX usage. Host-loaded lab throughput
and latency are diagnostic; field comparisons determine performance gains.
TBF backlog divided by rate estimates service time, not per-packet delay.

- [ ] Record the running daemon's commit/time from `wanbond monitor` or the
      dashboard, its executable hash and uptime before each measurement.
      `wanbond version` identifies the invoked local binary; it need not be
      the daemon. Commit time is UTC source time, not compilation time.
      Retain `-dirty` together with the patch/hash; `unknown` does not prove
      source identity. Builds preceding this feature provide no monitor
      commit/time fields.
- [ ] For `v0.0.2`, retain the C8 source/binary identity and the
      [operator release record](drafts/20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04).
      Field approval does not establish adaptive stage 1 acceptance.
      Upload and loaded-latency spread remain open; preserve the failed
      model gates and incomplete both-family regression series.
- [ ] Use observed `b444920` as the new operational baseline, retaining
      `f75668e` for original failing-test provenance. The 2026-10-04 bounded
      healthy uploads reach 3 Mbit/s, including after a 10 Mbit/s download;
      they do not reproduce the earlier 0.33 Mbit/s observation. Capture
      phase-aligned backpressure and lane samples before proposing its fix.
      Preserve the observed exit policy on all cleanup paths, including a
      partially failed setup. Use disk-backed `/var/tmp` for test logs;
      `/run` exhaustion has interrupted collection. Archive and verify
      obsolete artifacts before removing them, checking process/timer
      references and retaining the active runtime config. See the
      [installed-baseline checkpoint](drafts/20261002-1730-adaptive-policy-plan.md#10-installed-baseline-and-upload-investigation--2026-10-04).
- [ ] Run the [KVM lab procedure](../test/vm/README.md): calibrate each WAN before
      interpreting tunnel throughput; save the profile and binary hash.
- [ ] For adaptive-policy scenarios, retain actual voice send stamps and TCP
      test-start events. Iperf's connection timestamp does not establish the
      measurement origin. Use `adapt.py check` with independent phase goodput
      and idle-latency references; inconclusive checks do not satisfy a gate.
- [ ] Interleave baseline and candidate field rounds on the 5G/Starlink links,
      retaining contemporaneous lane and scheduler observations. Arm and verify
      removal timers before each qdisc change; report mobile RX+TX bytes.
      Before a WAN blackout, establish management access safety. The operator
      confirms ZeroTier access survives either single wanbond VLAN blackout
      on the field edge; keep that scope and check its current reply route.
      An observed Linux reply route alone does not establish the provider.
      Measure direct Starlink and 5G properties immediately before tunnel
      properties, preserving raw variation and imposing field rate caps.
      Retain both executable hashes and startup states: a warm candidate and
      a fresh baseline do not isolate a policy difference. A capped flow
      reaching its offered ceiling establishes only that service lower bound.
      Report VLAN counter intervals separately from management transfers
      whose provider is unknown. See the [stage 1 trial record](drafts/20261004-1105-adaptive-stage1-trial.md)
      for the measured comparisons and restoration checks.
- [ ] For the current standby comparison, preserve the operator's 0.5 Mbit/s
      symmetric Starlink and 100 down/10 up 5G caps separately from measured
      service. Use deployed → candidate → deployed rounds with recorded
      startup ages and fresh direct references. Voice-only comparisons precede
      TCP. A bounded TCP offer must exceed the survivor's expected residual
      service to expose it; the earlier 8 kB/s offer hid differences. Retain
      sender backpressure, receiver byte timestamps and exact payload hashes;
      pacing must not create a catch-up burst after a blocked write. Hold a
      single-WAN blackout when either lane is already DOWN or management
      access is intermittent. Preserve incomplete rounds as collection
      evidence, without a policy verdict.
- [ ] Verify loaded voice is actually sending during every measured tunnel
      transfer. A voice-only preflight is not a loaded-latency measurement.
      Keep tunnel traffic out of raw standby references; it consumes the
      service being calibrated. Retain transfer/voice timestamps and clock
      uncertainty when selecting common windows. Verify explicitly stamped
      commit/time after a scratch candidate build that lacks Go VCS metadata.
      The [flight-allowance comparison](drafts/20261002-1730-adaptive-policy-plan.md#isolated-flight-allowance-field-comparison--2026-10-05)
      corrects both collection errors and records no established field gain.
- [ ] For capped TCP timing, use a write quantum small enough to expose the
      measurement window (`iperf3 -l 1200` in the controlled uplink trial).
      Default 128 KiB writes can produce zero/burst receiver reports. Retain
      whole-report bounds; interpolating partial reports cannot prove a
      deadline. Collect remote logs as gzip archives to bound transfer time.
      Stop voice senders before their remote echo servers, or restrict analysis
      to guarded intervals when both remain active. The
      [controlled upgrade record](drafts/20261002-1730-adaptive-policy-plan.md#controlled-uplink-upgrade-and-two-lane-reproduction--2026-10-05)
      retains these collection defects and the rejected candidate's result.
- [ ] Compare both the low-rate and recovery windows, with simultaneous voice.
      A recovery goodput increase can coincide with worse low-rate service,
      latency and loss. The
      [estimator field comparison](drafts/20261002-1730-adaptive-policy-plan.md#estimator-replacement-field-tradeoff--2026-10-05)
      observes that tradeoff; retain all baseline sets and report uncertainty
      rather than selecting the baseline that makes the candidate look best.
      The [eligible-backlog comparison](drafts/20261002-1730-adaptive-policy-plan.md#eligible-backlog-field-comparison--2026-10-05)
      retains the three subsequent rejected candidates and their voice tails.
      Raw repair, expiry and AQM counters include other peer traffic and do
      not establish useful TCP efficiency. Preserve and verify the operator's
      selected exit policy across candidate and baseline restarts.
      Check `small_queue_drops_total` by `class` and `cause`; its six series
      must sum to `interactive_queue_drops_total`. Treat `realtime` as the
      size/protocol heuristic, not proof of voice-specific loss. On the
      single-peer concentrator select the observed `peer=""` series and fail
      analysis on an empty counter window. The
      [fresh-evidence pacing set](drafts/20261002-1730-adaptive-policy-plan.md#fresh-evidence-pacing-field-comparison--2026-10-05)
      improves one bounded throughput window but fails voice loss. Retain
      local CPU/observer timestamps; observer wake delay does not establish
      wanbond's own scheduler delay or exclude a busy individual core.
      The [counter-only diagnostic](drafts/20261002-1730-adaptive-policy-plan.md#small-queue-attribution-in-the-field--2026-10-05)
      observes real-time backlog shedding. Preserve startup samples outside
      the guarded performance window; metric intervals and missing echo
      sends give timing correlation, not identification of the lost leg.
      The [qualified-cohort set](drafts/20261002-1730-adaptive-policy-plan.md#qualified-cohort-field-comparison--2026-10-05)
      has no small-queue drops but still has a 173 ms hub voice tail. Measure
      local queue residence separately from path transit before assigning
      that latency to a scheduling mechanism.
      Collect interval increments of `small_queue_residence_seconds` count,
      sum and buckets on both hosts alongside the local echo timestamps and
      kernel shaper backlog. Reject reset/missing histogram windows. Queue
      residence ends at first transport submission and excludes subsequent
      socket or path waits; class histograms are not per-echo traces.
      The [residence diagnostic](drafts/20261002-1730-adaptive-policy-plan.md#queue-residence-field-diagnostic--2026-10-05)
      bounds local wait p99 by 5/1 ms despite 141/163 ms voice RTT p99.
      Echo handling excludes waiting before `recvfrom`; retain raw shaper
      backlog and actual sample durations alongside lane thresholds. Validate
      archive member names and explicit size limits before local extraction;
      expanded metric logs can exceed earlier harness limits. Preserve failed
      manifests and record local recovery without repeating field traffic.
      The accepted-policy component control retains zero guarded loss and
      64/59 ms voice RTT p99, but its direct 5G reference changes. Preserve
      those reference rates and reject raw cross-run throughput comparisons.
      Later lane ACK progress cannot establish receipt of every older real-time
      original; test missing-original recovery under continuing physical
      progress and measure additional copies/MB before accepting a change.
      The overdue-original trial passes that functional reproduction but
      regresses field voice to about 1.1–1.2% loss and 205 ms p99, with 32
      edge real-time/stale queue drops. It is rejected. Preserve returning
      baseline results and fresh direct rates; model recovery alone is not
      evidence of a field improvement.
      Verify fresh real-time queue wait while older recovery copies compete
      for a surviving lane. The isolated scheduler model changes 26 ms wait
      to zero while recovering all 19 originals; field verification is still
      required before claiming an end-to-end improvement.
- [ ] Record the removal timer's accuracy and actual firing time. For a
      latency-rise measurement, remove netem after receipt collection:
      deleting it while voice packets are queued can contaminate the loss
      measurement. Preserve lane-up samples; a round with an unplanned WAN
      outage does not isolate the two-live-lane delay scenario.
- [ ] Benchmark upload and download, confirming bulk bytes on both WANs and
      goodput above the profile's acceptance threshold.
- [ ] Repeat with opposite upload/download capacities and seeded jitter.
- [ ] Include idle-to-load transitions and at least 10 ms of jitter. Confirm
      an idle sender retains its pacing target and a faster WAN carries bulk
      after the idle period. Run `go test ./internal/bond` and the VM
      `profiles/jitter.json` benchmark with `--idle-seconds 30`.
- [ ] Run `profiles/radio.json` with `--idle-seconds 60 --warmup 10`: LTE
      propagation varies by 30 ms in each direction, with loss on Starlink.
      Retain failures; the milder jitter profile alone missed the deployed
      rate-collapse defect.
- [ ] Run `TestAdaptiveSmallFlowIsolation` and
      `TestAdaptiveCumulativeACKCoalescing` against both transport adapters,
      `TestACKCoalescingPreservesTCPInformation`, and the vendored engine's
      `TestEncryptedFlowMetadata`. Check that metadata stays local, duplicate
      ACKs/control information survive, and encrypted packets remain intact.
- [ ] Run `TestStandbyLinkStartupDoesNotFloodVoice` and
      `TestReverseJitterDoesNotMaskForwardCongestion` before the VM radio gates.
      Preserve the zero-loss startup bound and distinguish forward congestion
      from varying ACK return delay; a passing deterministic model does not
      replace the two-direction VM continuity test.
- [ ] Run `TestAdaptiveSmallBacklogCannotStarveBulk` against both adapters.
      Run `TestAdaptiveCumulativeACKCoalescing` with constant, increasing and
      decreasing windows; retain window-only updates, zero-window transitions,
      duplicate ACKs and SACKs. In the VM, verify TCP receiver progress with
      simultaneous upload, download and voice; separate one-way runs miss
      reverse ACK starvation.
- [ ] Run `TestBulkRepairLifetimeStartsWithFirstTransmission`. Queue residence
      must not prevent the first bulk repair; retries must not extend the fixed
      deadline, and small packets must keep their enqueue-relative expiry.
- [ ] Run the opt-in `progression` tests for capacity discovery and sustained
      ACK/voice contention, using the [lab commands](../test/vm/README.md).
      These currently fail. Retain their output alongside full VM results;
      default test-suite success does not establish those requirements.
- [ ] In a disposable Linux guest, run the tagged
      `TestStartupRemovesOnlyTheObsoleteTUNShaper` test. On a persistent-interface
      upgrade, check `tc qdisc show dev wanbond0`: the HTB/bfifo cap left by an
      older build must be removed, with the interface and addressing preserved.
- [ ] Run simultaneous bidirectional TCP and 50 Hz UDP during a capacity drop,
      five-second failure of either WAN, recovery and seeded packet loss.
      Check UDP loss, p99 RTT, maximum receive gap, and ongoing TCP progress in
      each direction during each outage. Use receiver reports and 1 KiB blocks
      as in `continuity.py`: default 128 KiB iperf blocks can report zero bytes
      while the kernel continues delivering. A surviving process alone is not
      proof of continuing traffic.
- [ ] On a production rollout, run a build with this transport at both
      endpoints, with the retired configuration keys removed
      ([install.md, upgrade note](install.md#upgrading-from-a-build-with-the-removed-transports));
      confirm WireGuard handshake and actual tunnel traffic in addition to
      green outer probes. Production deployment is operator-owned.
- [ ] Compare lane target/send/delivery-rate metrics with observed per-WAN bytes;
      verify a slow Starlink path does not monopolize concentrator downlink.
- [ ] Repeat the voice test with the real application's packet sizes and codec.
      Record live RF results separately from reproducible emulation results.

## P0 — spike / baseline
- [ ] Tunnel comes up edge ↔ concentrator (WG handshake completes).
- [ ] `ping` and a TCP transfer pass through the tunnel.
- [ ] Record single-path baseline throughput per uplink (iperf3).
The manual items above are automated end to end by the **`just p0-baseline`**
pre-pilot procedure against the two standing worker machines — see
[§P0 — automated real-link baseline](#p0--automated-real-link-baseline-realhosts-tier)
below. Run that command to capture the baseline report, then read/interpret the
numbers by hand; the baseline is INFORMATIONAL (report-only), not a pass/fail
gate.

## P1 — transparent failover
- [ ] Start a long-lived TCP flow (SSH session or iperf3) over the tunnel.
- [ ] Physically drop one WAN (unplug / disable the Starlink uplink).
- [ ] Flow survives with NO reset; throughput restored within `P1RecoverySeconds`.
- [ ] Restore the WAN; no thrash. Repeat for the other uplink.
- [ ] Change the edge public IP on one path (carrier re-address); flow survives.

## P2 — aggregation
- [ ] Under saturating load, both uplinks carry bulk and the bonded throughput
      is recorded against the sum of the per-path throughputs, read from
      `/metrics` (scripted run below).
- [ ] While idle, each uplink carries only probes and keepalives: record the
      idle byte rate per path. The transport does not keep a metered uplink
      idle under load.

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
      [runbook.md §6 Pilot exit criterion](runbook.md#6-pilot-exit-criterion-non-blocking).

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

### Multi-exit promotion after a late standby handshake
- [ ] With two exit-capable peers, make the boot-default exit unreachable before
      the standby completes its WireGuard handshake. Confirm the active exit stays
      on the boot default at first, then switches to the standby after its session
      becomes healthy and the boot exit's endpoint list is exhausted. Check `active exit switched`
      with `reason=auto-promotion` and confirm new tunnel traffic uses the standby.
- [ ] Repeat with the boot exit recovering before the standby session becomes
      healthy; confirm no promotion occurs after recovery.
- [ ] With `exit = "auto"`, compare both exits' up-path RTTs. Confirm the lower
      best RTT becomes active, a changed ranking does not switch again before
      five minutes, and a fully failed active exit promotes immediately.
- [ ] On the token-authenticated remote monitor, select a fixed exit and then
      `auto`. Confirm the combobox tracks `exitMode` while the active badge
      tracks `activeExit`, and a restart restores the configured selection.
- [ ] Open the exit selection menu while live snapshots arrive. Confirm it
      stays open, keeps keyboard focus, and a choice sends one switch request.

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

## P2 — scripted real-setup run (aggregation)

Scripted counterpart of the P2 summary above for the real deployment. Requires the
P1 setup already validated (both uplinks up, both daemons running the same build
from `0600` configs, `/metrics` reachable on `127.0.0.1:9090` each end). No
configuration selects aggregation: the transport uses every usable lane under
load.

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
      (both lanes carry bulk).
- [ ] Cross-check the far end: the concentrator `/metrics` shows
      `wanbond_path_rx_bytes_total` climbing on BOTH paths.
- [ ] Record `T_bonded / (T_starlink + T_5g)` and the iperf3 receiver goodput.
      The lab's gate for comparison is 75% of combined wire capacity for one TCP
      flow in each direction ([test/vm/README.md](../test/vm/README.md)).
- [ ] Record, per lane, `wanbond_adaptive_target_rate_bytes_per_second`,
      `wanbond_adaptive_send_rate_bytes_per_second`,
      `wanbond_adaptive_delivery_rate_bytes_per_second` and
      `wanbond_adaptive_discovering` at the middle of the flow.

### Reload discipline
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

## Adaptive-policy field checkpoint — 2026-10-05, 23:05 UTC

Observed C8/fresh-small-priority/C8 single-WAN trials retain lower candidate
voice p99 but fail the consecutive-loss gate; matching deterministic scenarios
also expose bulk-progress regressions. The candidate is unaccepted and both
hosts are independently verified restored to `b444920`. Preserve the workload,
contemporaneous direct references and metering with each repeat; see
[the measurement record](drafts/20261002-1730-adaptive-policy-plan.md#c8-fresh-small-priority-repeat-and-tcp-tradeoff--2026-10-05-2305-utc).

- 2026-10-06 demand-step ceiling removal: complete C8/candidate/C8 voice-only
  field set delivers candidate 2,750/2,750 both ways with zero queue drops,
  but increases mobile bytes 39–41% and exceeds the 150 ms gap gate.
  Candidate is unaccepted; both hosts, exit, timers, qdiscs and firewall are
  independently restored. Comparison 17.685 MB, through-cleanup 18.144 MB
  (nested), staging 0.866 MB (separate), all mobile RX+TX with background.
  Preserve the 137/130 ms model failure while improving capacity measurement
  and fallback-copy allowance; do not claim throughput from this voice set.

- 2026-10-06: conservative-cohort source `b70cb58` completed capped
  C8/candidate/C8 upload with immediate physical references and voice preflight.
  Early upload bounds improved, late bounds overlapped, loaded voice p99
  regressed; no promotion. Both C8 binaries, original exit, qdiscs, firewall,
  timers and runtime overrides were independently verified restored after
  owned cleanup. Mobile RX+TX: 27.154 MB through cleanup plus 0.874 MB staging.
  Evidence: `capacity-cohort-window-field-20261006` under
  `/srv/nvme/tmp/wanbond-adaptive-evidence`; full detail in the adaptive-policy plan.

- 2026-10-06: held-service flight-window source `4406761` completed capped
  C8/candidate upload; the budget guard held the final baseline. Early upload
  improved, loaded voice tails worsened and late bounds overlapped. No
  promotion or repeatability claim. Both C8 hashes, exit, timers, qdiscs,
  firewall and owned-artifact cleanup were independently verified. Mobile
  RX+TX: 22.168 MB through cleanup plus 0.779 MB separate staging. Evidence:
  `observed-service-window-field-20261006` under
  `/srv/nvme/tmp/wanbond-adaptive-evidence`; limitations remain in the plan.


- 2026-10-06: source `d1b3f74` completes one C8/candidate/C8 capped upload
  comparison. Early goodput bounds improve; loaded voice tails worsen and
  late bounds overlap. No promotion. Both C8 hashes, original exit, qdiscs,
  firewall, timers and owned runtime cleanup are independently verified.
  Mobile RX+TX: 27.769 MB through cleanup plus 0.777 MB separate staging.
  Evidence: `observed-ack-window-field-20261006` under the evidence root;
  full failures, RF context and cadence-age follow-up remain in the plan.


- 2026-10-06 cadence-age field comparison: source `1366136`, ARM SHA-256
  `3d6998b7ce621a3308729e4b7a259e1fab7904b80e801f36409f651fb8c3d255`;
  complete capped C8/candidate/C8 upload set, voice first and immediate physical
  references. Upload bounds overlap surrounding baselines; loaded voice p99
  130.8/152.0 ms worsens against both. All 2750 echoes per direction arrive.
  No promotion. Both hosts' deployed C8 hashes, boots, policy and cleanup are
  independently verified. Metered disjoint intervals total 27.333 MB with
  background. See `fresh-ack-cadence-field-20261006/` and the plan for limits.

- 2026-10-06 qualified ACK-volume field comparison: `d09b0b7` with 19 full
  bond failures completes capped C8/candidate/C8. Early upload bounds exceed
  both baselines, but late bounds overlap and hub voice p99 152.9 ms worsens
  against both. No source is promoted. Both hosts' C8 binaries, original exit,
  timers, firewall and qdiscs are independently verified restored before and
  after owned cleanup. Disjoint measured mobile intervals total 29.831 MB
  including background. Evidence: `qualified-aggregation-field-20261006/`;
  retain the plan's RF context and partial-improvement limits.


The 2026-10-06 current-physical-RTT experiment (`4e036a9`) completes one
capped upload B/C/B comparison with adjacent direct references. Early upload
bounds improve, but loaded voice p99 worsens against both C8 baselines and
late throughput overlaps. All 2750 echoes per host/phase arrive. The candidate
is rejected; hosts are independently verified restored and owned artifacts
cleaned. Measured mobile RX+TX is 29.536 MB over disjoint intervals including
background. This does not prove lab profiles or any full stage gate; exact
source, failures, references and limits are in the adaptive-policy plan.


The ACK-prefix trial (`2636afb`, 2026-10-06) retains zero guarded TCP-active
voice loss in both directions. Download reaches the 6 Mbit/s offer on all
revisions; upload report bounds overlap and returning-baseline latency reaches
the candidate's level. No performance improvement is established. Preserve the
incomplete download manifest from its budget hold and the separately metered
final baseline; never turn that into a completed three-phase runner. Whole
returning-upload streams miss one echo per host outside the guarded window.
Direct download reference code must enforce requested byte/rate bounds on the
remote sender; its previously unused mode sent a fixed 12.5 MB. All trial state
is independently verified restored and owned `/run` references/binaries removed.
Measured disjoint mobile intervals total 77.264 MB including background; see
[the ACK-prefix measurement record](drafts/20261002-1730-adaptive-policy-plan.md#ack-prefix-field-comparison--2026-10-06-06350656-utc)
for identities, exact windows and the unproven original-loss occurrence.


Combined receipt correction `8a64750` completes capped C8/candidate/C8 upload
with 1750/1750 echoes each host/phase and no guarded TCP-active loss. Its receiver
bounds overlap and whole-phase candidate p99 exceeds both baselines; retain
those tails rather than claiming non-regression from guarded windows alone.
Both installed identities and temporary state are independently verified
restored; owned runtime references and candidate binaries are removed. Measured
mobile RX+TX with background is 26.901 MB over disjoint intervals. The combined
receipt checks total 104.166 MB excluding gaps; no performance release follows.


The isolated qualified-capacity/drain source `307ca74` completes one capped
C8/experiment/C8 field upload comparison. All 1750 echoes per host/phase arrive;
whole-phase p99 is lower for the experiment, but upload-active p99 exceeds the
returning baseline and throughput report bounds overlap. Native/ARM builds
pass, while 24 default bond outcomes and 70 tagged entries fail. The source
is unmerged and supplies no completed stage or all-metric improvement.
Deployed binaries and temporary state are independently verified restored;
owned runtime references are byte-archived and removed. Disjoint measured
mobile RX+TX with background is 28.296 MB. `repair_packets_total` includes
ordinary replication and must not be read as timeout-only repair. Exact
sources, raw outcomes, RF references, accounting and continuing investigation
are in the [field record](drafts/20261002-1730-adaptive-policy-plan.md#qualified-capacitydrain-field-comparison--2026-10-06-09020916-utc).


A passing capacity-drop queue check requires substantial measured service
before the drop. Preserve latency, bulk and discovery outcomes together;
selected model passes are insufficient when the full suite still regresses.
The original baseline's three-run settled-drop passes and the unmerged
physical-drain corrections are retained in the
[continuing policy record](drafts/20261002-1730-adaptive-policy-plan.md#settled-capacity-drops-and-physical-drain-qualification--2026-10-06). No new metered field run is claimed.


The isolated post-drain current-delay source `2099735` completes one capped
C8/experiment/C8 field comparison. All 1750 echoes per host/phase arrive and
medians fall, but whole-phase p99 rises from approximately 50 ms to 134/108 ms
and upload bounds overlap. It is rejected as a performance candidate. Source
checks retain 25 default top-level failures and 113 tagged entries; native/ARM
builds pass. Both deployed identities, original network state and owned cleanup
are independently verified. Disjoint mobile RX+TX including background is
27.461 MB through cleanup. The new TCP propagation/loss regression and the
unresolved packet-placement/fallback hypothesis are retained in the
[field record](drafts/20261002-1730-adaptive-policy-plan.md#post-drain-current-delay-field-comparison--2026-10-06-10131026-utc). The accepted baseline remains `b444920` / `v0.0.3`;
no new stage, repeated field acceptance or release is claimed.
