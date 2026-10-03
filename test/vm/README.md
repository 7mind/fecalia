# Reproducible WAN lab

Two persistent Alpine 3.24.2 KVM guests run actual wanbond binaries, WireGuard,
TUN interfaces, TCP and UDP. Each guest has a separate management NIC and two
private Ethernet links. `tc` applies bandwidth, delay, jitter and loss separately
to each WAN and direction. The concentrator has one wildcard UDP socket, so
downlink aggregation must use the two authenticated edge NAT/source mappings.

## Requirements and lifecycle

Run inside the yolo sandbox on an x86_64 KVM host with `SMIND_SANDBOXED=1`,
`YOLO_VM_STATE_DIR`, accessible `/dev/kvm`, Python 3.11+, QEMU, `qemu-img`,
`cloud-localds`, `ssh`, `ssh-keygen` and `mkpasswd`. The two guests use 4 vCPUs
and 2 GiB RAM each. Initial provisioning needs Internet access for the pinned
official cloud image and Alpine packages. The image SHA512 is verified;
installed package versions are retained in each result directory. Package
repositories are not snapshotted, so preserve the guest disks for exact reuse.

From the repository root:

```sh
nix build
python3 test/vm/lab.py up
python3 test/vm/lab.py status
python3 test/vm/calibrate.py
python3 test/vm/benchmark.py result/bin/wanbond
python3 test/vm/continuity.py result/bin/wanbond
python3 test/vm/lab.py stop
```

`up` reuses stopped disks. `stop` requests guest shutdown and verifies QEMU
exit; it retains disks, keys, logs and measurements. An interrupted or partial
startup retains `lab.json`, QEMU logs and serial logs for diagnosis. Do not
delete the manifest while either recorded QEMU process is running. A lock
prevents two scenario writers from sharing the same lab.

State lives under `$YOLO_VM_STATE_DIR/wanbond-bonding`, mode 0700. Only regular
disk images under the VM state root are attached. The guests receive no host
directory or device mounts. WAN links use private Unix sockets; management SSH
binds to random loopback ports and uses a fresh lab-only key. `up` checks both
key authentication and rejection of password/keyboard-interactive methods.
No host networking, production host, router or production credential is used.
Guest WAN changes cannot disconnect the management NIC.

A transport change is judged on both link profiles, `radio` and `gigaradio`,
interleaved with the baseline in one lab session, before it is proposed for
deployment. A result on one profile alone does not support a deployment
decision: on 2026-09-30 a candidate raised UDP at 300+300 Mbit/s and lowered
TCP there while the radio profile did not move.

## Scenarios and gates

| Scenario | Conditions | Pass criteria |
| --- | --- | --- |
| `calibrate.py` | Plain TCP diagnostics and 1300-byte UDP, both WANs concurrently, both directions | UDP on each WAN delivers at least 85% of its configured rate; TCP is reported separately |
| `benchmark.py` | One TCP flow, then its reverse; default 2+6 Mbit/s, 15/25 ms one-way delay | Each direction reaches 75% of combined wire capacity; both WAN byte counters advance by over 100 kB |
| `udp.py` | Constant-rate UDP of full 1311-byte datagrams, then its reverse, offered at the rate full datagrams could carry (86.6% of wire capacity) | Each direction delivers 80% of combined wire capacity after a 10-second warmup; both WAN byte counters advance by over 100 kB |
| `profiles/asymmetric.json` | 6+2 Mbit/s uplink, 1+7 downlink, 3 ms jitter | Same throughput gates; independent directional capacity estimates |
| `profiles/fast.json` | 32+96 Mbit/s in each direction | Same throughput gates; calibrate this profile before interpreting results |
| `profiles/jitter.json` | 2+6 Mbit/s, 15/40 ms delay, 4/10 ms jitter | Same throughput gates, including a 30-second idle period before load |
| `profiles/mobile.json` | 0.4+1.25 Mbit/s uplink, 0.5+100 downlink, 4/10 ms jitter | Same throughput gates; stress model for standby Starlink and asymmetric LTE |
| `profiles/radio.json` | Mobile capacities; Starlink 20±10 ms delay and 0.4% loss, LTE 40±30 ms delay, independently in each direction | Same throughput gates after 60 seconds idle; reproduces the collapse missed by milder jitter |
| `profiles/gigaradio.json` | 300+300 Mbit/s in each direction with the radio profile's delay, jitter and loss; delay correlation 95% | Same throughput gates; a model of an upgraded Starlink beside 5G |
| `profiles/gigasteady.json` with `wander.py` | 300+300 Mbit/s with steady netem delays that `wander.py`, run in each guest, moves as measured links' latency moves | Same throughput gates; the only scenario in which latency has memory |
| `single_lane.py` | One WAN dead from the start; on a cold tunnel a call in both directions, then one TCP flow three seconds later | TCP reaches 50% of the live WAN's rate; each voice stream has <1% loss |
| `flood.py` | A call in both directions while one side offers 400 Mbit/s of UDP to `profiles/fast.json` | Each voice stream has <1% loss |
| `continuity.py` | Simultaneous TCP in both directions plus two 50 Hz, 160-byte UDP echo streams | TCP completes 65 seconds and meets the progress rule below; each UDP stream has <1% loss and meets the gap and latency rules below (`continuity_gates.py`) |

Continuity accepts `--profile`, defaulting to `profiles/basic.json`. At 15
seconds WAN1 is capped at 0.5 Mbit/s in each direction; lower profile rates are
preserved. At 25 seconds it loses all packets, and at 30 seconds its profile
conditions are restored. WAN2 loses all packets from 40–45 seconds. WAN1 has
at least 1% random loss after 55 seconds. Directional delay and jitter remain
as configured. The phase manifest records actual application times and each
direction's conditions. Echo traffic exercises both directions;
RTT includes forward and return delay.

A profile condition may set `correlation` (percent), netem's delay
correlation: successive delays stay close, as on a radio link whose latency
drifts rather than scatters. Without it a 300 Mbit/s link with 30 ms of jitter
reorders thousands of datagrams, which no radio link does; at 1.25 Mbit/s the
same jitter reorders little, so the radio profile leaves it unset. Guests raise
their TCP buffer ceilings to 64 MB at start (`lab.py`): a 300 Mbit/s path with
150 ms of round trip needs a 6 MB window, and the kernel default of 6 MB would
cap the test. `udp.py` gives iperf3 a 4 MB socket buffer; with the default
208 kB its receiver overflowed on 2% of the tunnel's bursty deliveries and
under-counted.

netem draws its jitter anew for every datagram. Latency measured on the
production links (600 pings each at 5 Hz, idle, 2026-09-30) behaves
otherwise: the ~40 ms link has a round-trip standard deviation of 10 ms and an
autocorrelation of 0.52 at 0.2 s and 0.39 at 5 s, moving between levels 10-20
ms apart that last for seconds; the ~24 ms link has 6.6 ms and no memory
beyond a fraction of a second. `wander.py INTERFACE --delay --scatter
[--shift]` changes an interface's netem delay every 20 ms along such a walk.
With `--delay 20 --scatter 3 --shift 8` and `--delay 12 --scatter 4.5` the
lab's two WANs measured 6.3 ms (autocorrelation 0.55, 0.36) and 6.5 ms (0.01,
-0.02). Lowering a netem delay lets new datagrams overtake queued ones, so
this reorders within a WAN, which the real links are not known to do.

When switching profiles, `lab.impair` emits delay correlation explicitly,
including zero. Observed on 2026-10-02: omitting zero from `tc qdisc replace`
retained the preceding profile's 95% correlation. A two-step 95% → 0% kernel
check failed before the correction and reports zero afterwards. A result
whose recorded qdisc disagrees with its profile is not a valid calibration.

Voice latency is judged against the WANs that were up when a datagram was sent,
using the times recorded in the phase manifest:

| Condition | Rule |
| --- | --- |
| First 5 seconds (cold start, no capacity estimate) | Receive gaps under 200 ms; latency not gated |
| WAN with the lowest idle round trip up | p99 RTT under 150 ms over all such datagrams |
| Only a slower WAN up | p99 RTT under that WAN's idle p99 plus 50 ms, and never stricter than 150 ms |
| After the first 5 seconds | Receive gaps under 150 ms |

A WAN's idle p99 follows from its profile: one-way delays are uniform within
delay ± jitter in each direction, which gives 131.6 ms for the radio profile's
LTE. No tunnel can deliver below that figure on that WAN alone.

TCP must deliver to each receiver in every three-second window of an outage
and in the first whole one-second interval after recovery. With two voice
streams on a 0.4 Mbit/s WAN a single second without TCP delivery is accepted;
voice keeps priority. `continuity_gates.py DIRECTORY...` re-evaluates recorded
runs. Runs before 2026-09-29 20:30 were judged by the earlier rule: under
150 ms p99 RTT and gap over the whole run, TCP delivery in every second of an
outage.

TCP progress uses the receiving side of each flow, collecting server JSON with
`--get-server-output`. The earlier check used client reports and default
128 KiB application blocks. A zero-byte interval in those reports can reflect
blocked application I/O while the kernel continues delivering TCP payload.
An isolated 0.5 Mbit/s direct link reproduced eight such intervals in 15 seconds;
packet captures contained 57,920–60,816 payload bytes in each of them
(`20260928-213040-iperf-progress`). The replacement uses `-l 1K` and receiver
counters, preserving the one-second outage progress requirement. Raw TCP reports
are retained. Source: iperf 3.20's [send/receive counters](https://github.com/esnet/iperf/blob/3.20/src/iperf_tcp.c)
and [blocking I/O loops](https://github.com/esnet/iperf/blob/3.20/src/net.c).

```sh
python3 test/vm/calibrate.py --profile test/vm/profiles/fast.json
python3 test/vm/benchmark.py result/bin/wanbond \
  --profile test/vm/profiles/fast.json --warmup 15
python3 test/vm/benchmark.py result/bin/wanbond \
  --profile test/vm/profiles/asymmetric.json
python3 test/vm/benchmark.py result/bin/wanbond \
  --profile test/vm/profiles/jitter.json --idle-seconds 30
python3 test/vm/benchmark.py result/bin/wanbond \
  --profile test/vm/profiles/mobile.json --warmup 10
python3 test/vm/benchmark.py result/bin/wanbond \
  --profile test/vm/profiles/radio.json --idle-seconds 60 --warmup 10
python3 test/vm/continuity.py result/bin/wanbond \
  --profile test/vm/profiles/radio.json
```

The default warmup is 5 seconds; `--warmup` separates convergence from the
steady measurement. Raw iperf JSON retains omitted warmup intervals. Longer
warmup is not evidence of faster adaptation. Interface totals include warmup,
protocol overhead and repair traffic, and are not application goodput.
`--idle-seconds` records idle-start metrics and leaves the actual daemons running
before the first measurement; it tests whether sparse feedback damages later
capacity discovery. The mobile profile approximates the reported link asymmetry;
its limits are wire rates, whereas a speed-test result is application goodput.

HTB limits each egress independently. Netem uses fixed per-guest/path seeds and
holds a bandwidth-delay product plus a 100-packet router buffer; a flat packet
limit would falsely reduce fast-link capacity before adding congestion. Guest
scheduling is nondeterministic even with fixed random seeds. Fixed-size UDP
calibrates packet-rate capacity. TCP is a separate diagnostic: congestion
control under the profile's intended loss can leave capacity unused, and
segmentation offload changes queue occupancy.

## Inspection and artifacts

A profile may set `buffer_ms` to size its router buffer in milliseconds of
traffic at its configured rate, instead of 100 packets. `swing` and `grant_ms`
redraw the HTB rate uniformly within `rate*(1±swing)` every grant, using a
seeded loop in each guest. `profiles/cellular.json` varies the mobile lane
±60% every 100 ms with a 400 ms buffer. These are lab impairments, not daemon
configuration; the adaptive-policy baseline for them has not been measured.

```sh
python3 test/vm/lab.py exec hub 'cat /root/wanbond.log'
python3 test/vm/lab.py exec edge 'curl -sf http://127.0.0.1:9090/metrics'
python3 test/vm/lab.py impair edge 1 --rate 0.5 --delay 30 --jitter 3 --loss 1
```

Timestamped result directories contain JSON summaries, raw iperf output,
voice packet timing, daemon logs, metrics and candidate binary SHA256. Configs
and ephemeral keys stay inside the private lab state. Keep the full directory
when comparing candidates. Scripts exit nonzero on unmet gates; a failed
calibration makes a throughput conclusion inconclusive.

These are tests of an emulated network and real software, not measurements of
Starlink or LTE RF behavior, production NAT, or Pi CPU limits. The voice stream
tests the adaptive small-packet heuristic; it is not an application classifier.
Both paths failing together cannot preserve delivery. Startup, loss bursts and
rates outside the tested profiles need their own scenarios.

## Adaptive-policy stage 0: acceptance conflict — 2026-10-02

The first implementation audit stopped at stage 0 under the plan's stop rule.
The `lab-tooling` branch was merged; the guests were booted and their private
WAN reachability checked. No adaptive scenario or production measurement was
completed. Neither `window-bound` nor `cold-start` was merged.

Observed on `f75668e`, the deterministic survivor reproduction delivered
500/500 measured voice datagrams, with one-way p99 55 ms and maximum gap 38 ms.
Bulk delivery in the final five seconds was `[3600 3600 2400 3600 2400]` B/s,
failing scenario 1a's 37,500 B/s requirement in every second. This models a
healthy sole survivor, giving recovery every opportunity before measurement;
it omits random loss, jitter and the extra TCP ACK stream. It measures delivered
bulk datagrams, an upper bound on TCP payload goodput.

Independently of that controller result, the traffic budget makes the combined
gate infeasible on the radio uplink when WAN2 fails. Its WAN1 rate is
0.4 Mbit/s = 50,000 B/s. Two 50 Hz streams of 224-byte encrypted voice
datagrams require `100*(224+101+28)` = 35,300 B/s; the bulk gate alone
requires 37,500 B/s, before its own overhead. Over a 15-second outage,
even giving bulk three seconds with no delivery, voice plus bulk requires
979,500 bytes on a link that can carry 750,000. Accommodating bulk would
require dropping at least 651 voice datagrams: over 8% of the direction's
8,000 voice datagrams in the runner's 80-second blackout scenario, against
the under-1% gate. This is a conservation bound inferred from the profile
and wire format, not a measured capacity estimate. The same conflict applies
to 1c with this direction's survivor.

The exact baseline reproduction and output are in
[the acceptance-budget record](../../debug/20261002-180053-adaptive-budget.md).
The operator subsequently approved using available TCP goodput after required
voice traffic and protocol overhead for capacity percentages in scenarios
carrying voice. The deterministic wire budget and matched direct-link lab
calibration must be independent of candidate estimates, repairs and redundant
copies, and shared by baseline and candidate. Voice gates, progress rules and
adaptation deadlines remain unchanged. This removes the acceptance blocker;
stages 1–3 and full scenario validation remain unimplemented at this amendment.

## Initial validation — 2026-09-28, `8ff0c5e`

All three throughput gates passed on this host's KVM lab:

| Profile | Uplink TCP Mbit/s | Downlink TCP Mbit/s | Result directory |
| --- | ---: | ---: | --- |
| 2+6 Mbit/s | 6.23 | 6.19 | `20260928-193715-adaptive` |
| Asymmetric + 3 ms jitter | 6.18 | 6.13 | `20260928-193818-adaptive` |
| 32+96 Mbit/s, 15 s warmup | 98.15 | 96.68 | `20260928-193456-adaptive` |

The final 65-second continuity run (`20260928-193921-continuity`) passed both
TCP progress gates. Each UDP echo stream lost 2 of 3250 packets (0.062%). Hub
and edge p99 RTTs were 100.0/103.6 ms; maximum receive gaps were 75.3/95.5 ms.
The packaged binary ran the basic, asymmetric and continuity scenarios. The
fast scenario used the same transport code in a development build. Subsequent
startup validation rejects a secondary peer lacking authenticated probes
before starting workers; its failing-then-passing regression and race test
cover that error path.

The fast profile was independently calibrated at 30.97/92.97 Mbit/s UDP uplink
and 30.97/92.98 downlink (`20260928-190846-calibration`). Actual disk reuse,
key-only SSH enforcement, guest WAN routing and TUN creation passed an
unattended stop/start cycle. Guests were cleanly stopped after validation;
disks and all result directories remain under the state root above.

These measurements include encapsulation costs and controller headroom;
nominal WAN rate is not TCP goodput. Raw results retain binary SHA256 values,
profiles, warmup intervals, packet samples and failure artifacts from earlier
candidates. Rerun the gates after changing the policy or test host.

## Intermediate correction — 2026-09-28, `623d36f`

The mobile profile reproduces the deployed controller's collapse: revision
`8ff0c5e` delivered 0.574 Mbit/s uplink and 0.577 downlink
(`20260928-200507-adaptive`). Idle jitter reduced pacing targets to their
16 kB/s floor; receive batching and acknowledgement handling also limited
loaded performance. The correction is an intermediate candidate, not a pass
of every performance gate.

The final transport candidate has SHA256
`c77bb8a95694562ee5a42945d4f207a0240c0b91a462c5b93dcc8b38de7916b0`.
The subsequent monitor display fix changes no transport behavior.

| Scenario | Uplink TCP Mbit/s | Downlink TCP Mbit/s | Gate result / artifacts |
| --- | ---: | ---: | --- |
| Mobile, 10 s warmup, 20 s measurement | 1.204 | 57.148 | Both below 75% of nominal wire capacity; `20260928-210443-adaptive` |
| 32+96 Mbit/s, 15 s warmup, 20 s measurement | 95.025 | 97.780 | Uplink below 96 Mbit/s threshold; `20260928-210555-adaptive` |
| Mobile LTE only, 15 s warmup, 25 s measurement | — | 71.135 | Diagnostic isolation, not the two-link gate; `20260928-211133-mobile-isolation` |
| Mobile with both links restored, same running daemons | — | 72.687 | Longer convergence, not comparable to cold startup; same isolation directory |

Both WANs carried bulk traffic in the two-link benchmarks. In the final
65-second outage scenario (`20260928-210718-continuity`), the hub UDP stream
lost 1/3250 packets and the edge lost none. Their p99 RTTs were 101.3/100.1 ms
and maximum receive gaps were 75.9/80.1 ms. The UDP gates passed. TCP completed
without a connection reset, but one measured one-second outage interval reported
zero bytes, failing the original TCP progress gate. The counter defect described
above means this result alone does not establish a one-second delivery pause.

An earlier correction passed the 2+6 Mbit/s jitter profile after 30 seconds
idle at 6.437/6.449 Mbit/s (`20260928-210003-adaptive`); the final small-packet
replication budget and receive coalescing refinement were added afterwards.
Mobile calibration measured 94.58 Mbit/s direct LTE TCP downlink and all UDP
wire-rate checks passed (`20260928-202421-calibration`). The calibration's
overall TCP gate failed on the slow uplink, so it is not an overall pass.

Remaining work: slow convergence and download headroom on the mobile profile,
investigating repair/expiry counters without injected loss, and validating TCP
delivery during outages with the corrected counters. These VM measurements do not
establish production throughput. Deploy the correction to both ends before
comparing real links.

## Strong radio reproduction and feedback corrections — 2026-09-28

The deployed Pi and concentrator were verified to use the same binary (SHA256
`22c2ac182a1bbfcaba0bf1fb64747e7060a86bfe5d87053bb64e0a046aafced9`). During
the operator's rerun, the concentrator's LTE target remained near its 16 kB/s
floor despite healthy probes. Idle targets also fell by exactly 30% after
lost keepalives. Matching binaries and removal of the old HTB cap did not
resolve this second reproduction.

The stronger `radio.json` profile reproduced the collapse after 60 seconds idle:
0.417/0.472 Mbit/s uplink/downlink (`20260928-214147-adaptive`). Corrections now
cover idle keepalive timeouts, receipt windows older than the newest 256
packets, reordered ACKs, congestion-window release across lanes and at attempt
retirement, and the complete delivery-confirmation delay used for repair.
ACK encoding remains version 1. Reordered receipts merge once without rolling
back current rate feedback or refreshing liveness.

Focused failing-then-passing reproductions established that:

- 400 delivered datagrams could leave 100 unacknowledged after reordering beyond
  the newest global bitmap.
- A global receipt on another lane left the original lane's in-flight bytes
  occupied. Attempt retirement could also leak those bytes after an RTT spike.
- On a loss-free jittered path, the old repair timer produced 326 retransmissions
  for approximately 2500 delivered datagrams. Measuring full confirmation time
  reduced this to one in the first corrected run.
- A shallow 1 Mbit/s router buffer dropped 74 of 338 steady-state datagrams.
  Loss now reduces pacing by 10% and caps it at 105% of measured delivery;
  the corrected run dropped 24/280 (8.57%), below the 10% regression bound,
  and delivered 507,600 payload bytes in five seconds.

These corrections do **not** establish a complete performance fix:

| Candidate / scenario | Uplink / downlink TCP Mbit/s | Outcome / artifacts |
| --- | ---: | --- |
| Full confirmation timing, radio | 1.254 / 61.394 | Download gate failed; `20260928-223555-adaptive` |
| Plus reordered ACKs and shallow-buffer loss response, radio | 1.254 / 61.971 | Download gate failed; `20260928-225442-adaptive` |
| Same transport, fast profile | 89.201 / 95.003 | Both below 96 Mbit/s; `20260928-225813-adaptive` |
| Rejected reduction of the jitter allowance with sample count, radio | 1.150 / 47.867 | Both gates failed; `20260928-230633-adaptive`. This experiment was removed |

The radio continuity scenario also fails: one run recorded 14.65%/4.28% echo
loss and roughly 314/304 ms p99 RTT (`20260928-230515-continuity`). Diagnostic
counters showed thousands of small-packet queue drops at the edge, while the
hub's small-packet queue had none. TCP ACKs and voice share this size-based
priority class. In contrast, two simultaneous echo streams on standby Starlink
alone, without bulk load, had zero loss and 59/60 ms p99 RTT
(`20260928-230037-voice-capacity`). The remaining work includes reverse-path
contention, per-flow fairness, ACK overhead and convergence; changing delay
thresholds alone did not solve it. The 150 ms end-to-end latency gate and
throughput thresholds have not been relaxed.

Investigation logs and candidate binaries are retained under
`/srv/nvme/tmp/wanbond-audit-20260928/`; full VM artifacts remain in the lab state
directory. Production deployments remain operator-owned.

The retained transport plus diagnostic counters passed the original basic
continuity scenario (`20260928-231421-continuity`): TCP receiver progress passed,
echo losses were 0/3250 and 2/3250, p99 RTTs 135.7/127.0 ms, and maximum receive
gaps 93.0/96.8 ms. This does not substitute for the failing radio scenario.

A separate diagnostic installed CAKE before encryption, using **known emulator
capacities** at 85% of their sum and an explicit 165-byte encapsulation allowance.
With ACK filtering, CAKE removed 19,717 redundant ACKs and both echo streams lost
22/3250 packets (0.677%). Their p99 RTTs were still 284.7/225.5 ms; TCP progress
failed during the LTE outage (`20260928-231028-continuity`). Without ACK filtering,
loss was 3.94%/4.37% and p99 RTT 310.1/287.2 ms (`20260928-231213-continuity`).
This supports investigating reverse ACK contention, but does not validate an
automatic AQM integration or satisfy the latency target. No CAKE dependency or
production qdisc change was added.

A focused startup reproduction also measured 190 ms p99 one-way voice delay
on a 0.4 Mbit/s link with bulk offered traffic. Lower initial pacing with faster
startup growth, and a window capped by measured delivery, each caused other
capacity regressions. Those experiments were removed; the failing diagnostic
and logs remain in investigation scratch rather than being presented as fixes.

The retained feedback changes also passed a mixed-version data-plane check
against the intermediate candidate in both arrangements. Old concentrator/new
edge delivered 6.267/6.423 Mbit/s; new concentrator/old edge delivered
6.390/6.291 Mbit/s (`20260928-231901-mixed-versions`). The old-concentrator
restart needed about 16 seconds of handshake polling after addresses were
restored; the old-edge pairing answered immediately. The initial fixed
three-second check failed on this restart interval. This is functional wire
compatibility evidence, not a claim of uninterrupted traffic during a daemon
restart.

A further reproduction separated serialization from congestion: a lone large
datagram on an otherwise idle 0.4 Mbit/s link was assigned 24 ms of false queue
delay because its baseline came from a tiny keepalive. Transit minima now use
128-byte wire-size buckets. The test verifies both removal of that false delay
and continued detection of a real 40 ms queue. With this change the radio
benchmark measured 1.256/65.954 Mbit/s (`20260928-232608-adaptive`); download
still failed its gate. The radio continuity test still failed
(`20260928-232821-continuity`), so this correction does not resolve reverse
priority-queue contention.

## Local flow metadata and ACK coalescing — 2026-09-29

A 1024-packet small-packet burst delayed another flow past its service deadline
in both the pure transport and real UDP adapter. Per-flow round-robin scheduling
now passes that same contract. A separate real-engine test first confirmed that
plaintext reached the receiving TUN without any flow identity reaching the Bind;
the optional metadata interface now carries IP flow identity and pure-TCP-ACK
metadata through encryption. It is disabled for legacy Binds and adds no wire
fields. Pure-ACK parsing was fuzzed for five seconds (499,158 inputs).

Coalescing has separate failing-before/passing-after memory and UDP tests for
delivery of the newest cumulative ACK. Preservation tests cover duplicate and
backwards ACKs, window/sequence/traffic-class changes, control information,
timestamp regressions and different flows. It removes only unsent redundant
ACKs and reports `wanbond_adaptive_coalesced_tcp_acks_total` separately from
queue drops. It does not configure CAKE or require known emulator capacities.

| Candidate/scenario | Measured outcome | Verdict / artifact |
| --- | --- | --- |
| Per-flow scheduling, radio benchmark | 1.185 / 54.735 Mbit/s upload/download | Both gates failed; `20260928-234935-adaptive` |
| Per-flow scheduling, radio continuity | Hub/edge loss 0.769%/0.831%; p99 RTT 236.7/221.7 ms | Latency/gap and TCP progress failed; `20260928-234724-continuity` |
| Plus conservative ACK coalescing, radio benchmark | 1.286 / 57.918 Mbit/s | Upload passed; download failed; `20260928-235422-adaptive` |
| Plus ACK coalescing, radio continuity | Hub/edge loss 1.262%/0.585%; p99 RTT 239.4/232.0 ms | Latency/gaps, hub loss and TCP progress failed; `20260928-235305-continuity` |
| Plus ACK coalescing, basic continuity | Zero loss; hub/edge p99 RTT 124.1/140.5 ms; gaps 145.7/100.1 ms | All gates passed; `20260928-235848-continuity` |
| Plus ACK coalescing, fast benchmark | 69.347 / 93.271 Mbit/s | Both gates failed; `20260929-000005-adaptive` |
| Per-flow scheduling without ACK coalescing, fast benchmark | 71.050 / 94.687 Mbit/s | Both gates failed; `20260929-000219-adaptive` |

The radio benchmark rows include 60 seconds idle, 10 seconds warmup and 30
seconds measurement per direction. These two fast runs use five seconds warmup
and 20 seconds measurement; earlier fast runs used 15 seconds warmup. Their
early convergence curves are similar, so comparing their aggregate numbers
without matching warmup incorrectly suggests a large new regression.
Flow isolation improves one proven source
of contention; these results do not establish the requested radio continuity
or saturation goals. Production deployments and the Nix configuration pin were
left to the operator. The new engine API also requires refreshing the Nix
vendor hash when its local source changes, even if `go.mod` is unchanged.

## Startup, directional feedback and reverse ACK contention — 2026-09-29

Further failing-before/passing-after reproductions established these defects:

- A first data packet was declared lost before propagation, serialization and
  the delayed ACK could complete. The initial timeout now includes uncertainty.
- Loaded RTT variation and a loaded RTT minimum could expand the sending window.
  The window now uses independent unloaded history, bounded initial delivery
  credit, and a single-datagram overshoot for priority service. A 0.4 Mbit/s
  startup model delivered all 100 voice datagrams; its p99 one-way delay fell
  from 215 ms to 72 ms in the final candidate. This is a deterministic
  model, not a measured radio result.
- Reverse-only jitter made the controller increase pacing from 125,000 to
  134,000 bytes/s despite a real 30 ms forward queue. The forward delay allowance
  now uses idle forward-transit variation, independently of ACK return delay.
- Strict size priority starved bulk under continuous small-packet load in both
  memory and real UDP adapters. One reserved bulk turn after 8 KiB of priority
  service passes that same contract. An actual 30-second bidirectional capture
  had upload stop around 12 seconds while download continued; captures and raw
  iperf reports are in investigation scratch under `bidir-capture/`.
- Only 100 of 15,745 consecutive, advancing ACK triples in the captured download
  had an unchanged advertised window. Requiring window equality defeated
  coalescing. Constant, growing and shrinking nonzero windows now share the
  same delivery contract; the newest advancing ACK retains current window state.
  Separate tests preserve window-only changes, window closure/reopening,
  duplicate/backwards ACKs, SACKs and other control information.

The first earlier-repair experiment copied small datagrams even while fresh
feedback continued. It increased general latency and was narrowed to lanes
with no ACK since the original send. An attempt to put all flows into a common
deficit scheduler removed starvation but lost 49 of 100 startup voice packets;
weighted variants still lost 26–29. Those scheduler experiments were discarded.
The retained reservation leaves the small-flow queue in place. Test thresholds
and emulator capacities remain unchanged.

| Candidate/scenario | Measured outcome | Verdict / artifact |
| --- | --- | --- |
| ACK-coalescing baseline, fast with matching 15 s warmup | 93.102 / 88.867 Mbit/s upload/download | Both gates failed; `20260929-000422-adaptive` |
| Startup window/timer candidate, radio | 1.219 / 68.472 Mbit/s | Both gates failed; `20260929-002744-adaptive` |
| Same candidate, radio continuity | Hub/edge loss 1.815%/2.123%; p99 RTT 240.6/241.6 ms | Failed; `20260929-002626-continuity` |
| Plus directional jitter correction, radio | 1.187 / 70.431 Mbit/s | Both gates failed; `20260929-003456-adaptive` |
| Same candidate, radio continuity | Hub/edge loss 0.8%/0.4%; p99 RTT 202.5/208.1 ms | TCP progress, gaps and latency failed; `20260929-003339-continuity` |
| Plus bulk reservation and unloaded mean window, radio | 1.185 / 65.222 Mbit/s | Both gates failed; `20260929-005342-adaptive` |
| Same candidate, radio continuity | TCP progress passed in both outage windows; loss 1.785%/0.831%, p99 RTT 209.5/214.5 ms | Voice gates failed; `20260929-005224-continuity` |
| Same candidate, fast | 92.938 / 97.414 Mbit/s | Upload failed; download passed; `20260929-005615-adaptive` |
| Same candidate, basic continuity | Zero loss; p99 RTT 114.6/108.8 ms; gaps 79.7/74.9 ms | All gates passed; `20260929-005738-continuity` |
| Plus current-window ACK coalescing, radio | 1.182 / 69.591 Mbit/s | Both gates failed; `20260929-010037-adaptive` |
| Same candidate, radio continuity | TCP progress and receive gaps passed; hub/edge loss 1.108%/0.923%, p99 RTT 187.9/182.5 ms | Hub loss and both latency gates failed; `20260929-005920-continuity` |
| Same candidate, fast | 94.390 / 89.182 Mbit/s | Both gates failed; `20260929-010311-adaptive` |
| Same candidate, basic continuity | Zero loss; p99 RTT 109.8/105.9 ms; gaps 86.7/86.9 ms | All gates passed; `20260929-010434-continuity` |

Radio benchmarks use 60 seconds idle, 10 seconds warmup and 30 seconds
measurement per direction. Fast benchmarks in this table use 15 seconds warmup
and 20 seconds measurement. Continuity always combines both TCP directions and
two echo streams. Passing the basic profile or an isolated regression test does
not satisfy the radio latency, loss or saturation requirements.

In the last radio continuity run, all 36 missing hub echoes occurred during the
five-second LTE outage. The edge missed 29 during that outage and one just before
it. Small-packet queue drops continued beyond the initial outage transition.
Thus improved failover timing alone does not explain or resolve the remaining
loss: the single standby link must also carry feedback, both echo streams and
the surviving TCP traffic. The full frontend/Go/vendor gate, focused race tests,
ARM64 build and Nix build passed for this intermediate code; these are correctness
and packaging gates, not a substitute for the failed radio tests.

The retained code is commit `4d3d0da`. Production services and the Nix configuration pin were
not changed by these experiments.

## Repair lifetime and rejected discovery experiments — 2026-09-29

The next retained correction separates queue residence from bulk repair lifetime.
A failing reproduction queued a bulk datagram for 90 ms, then observed it expire
after only 195 ms in flight, before a 190 ms repair timeout had been polled.
Bulk now gets 250 ms from its first transmission. Small packets retain the
enqueue-relative deadline, and retransmission never extends either deadline.
The same reproduction verifies the alternate-path retry and fixed expiry.

| Candidate/scenario | Measured outcome | Verdict / artifact |
| --- | --- | --- |
| Repair lifetime correction, radio | 1.183128 / 71.897991 Mbit/s upload/download | Both gates failed; `20260929-011714-adaptive` |
| Same candidate, radio continuity | TCP progress and gaps passed; hub/edge loss 0.985%/1.323%, p99 RTT 198.2/197.2 ms | Edge loss and latency failed; `20260929-011557-continuity` |
| Same candidate, fast | 97.356924 / 95.997262 Mbit/s | Upload passed; download was below the 96 Mbit/s threshold; `20260929-011948-adaptive` |
| Same candidate, basic continuity | Zero loss; hub/edge p99 RTT 117.1/113.6 ms, gaps 77.9/80.7 ms | All gates passed; `20260929-014420-continuity` |
| Rejected faster discovery, radio | 1.185465 / 59.000602 Mbit/s | Both gates failed; `20260929-013411-adaptive` |
| Same rejected candidate, radio continuity | Loss 0.677%/1.415%, p99 RTT 200.5/199.6 ms | TCP progress, gaps and voice gates failed; `20260929-013253-continuity` |
| Same rejected candidate, fast | 94.849307 / 96.940982 Mbit/s | Upload failed; download passed; `20260929-013644-adaptive` |

Benchmark timings match the preceding table. The repair correction addresses
a reproduced deadline defect; these runs do not prove a throughput improvement.
The fast download failure must not be rounded into a pass.
The repair correction is commit `ee61de2`. The default frontend/Go/vendor gate,
focused race tests, ARM64 cross-build and Nix packaging passed. Production
deployment and the `nix-config` pin remain operator-owned and unchanged.

The rejected discovery experiment reached 94–97 Mbit/s within five seconds in
a deterministic 100 Mbit/s, 80 ms RTT model, including a 2-to-100 Mbit/s capacity
increase. The preceding controller reached 16–25 Mbit/s. The radio regression
above is why the faster growth is not retained. Its patch is saved in
investigation scratch as `discovery-v22-rejected.patch`.

A separate loss-free, symmetric 0.4 Mbit/s model offered two 50 Hz voice-sized
flows, a continuous pure-ACK flow, and bulk in each direction. In its final
second the unchanged queue delivered 142/200 voice datagrams, with 158 ms p99
one-way delay. Prepending new flows did not fix it. Preferring non-ACK flow
heads restored all 200 datagrams but still measured 91 ms, above the model's
75 ms bound. Doubling the bulk reservation interval failed the existing
memory/UDP starvation contract. Doubling the feedback interval worsened startup
voice delivery. None of these queue or interval experiments is retained;
the `sparse-flow-*.log` scratch files retain their results. A passing isolated loss check would not establish radio latency
or continuing TCP progress under overload.

Both deterministic reproductions are committed under the `progression` build
tag. They intentionally fail on the retained controller and are excluded from
the default correctness gate. Run them explicitly from the repository root:

```sh
nix develop --command go test -tags progression ./internal/bond \
  -run 'TestCapacityDiscoveryAtCellularRTT|TestSparseSmallFlowsSurviveSustainedACKBacklog' \
  -count=1 -v
```

They run in virtual time without VMs, root, or known-capacity configuration in
the controller. Preserve their assertions while evaluating candidates; passing
them does not replace the full VM radio gates. Move a reproduction into the
default suite only when the corresponding defect is corrected and the broader
radio checks show no regression.

### CPU and warmed throughput measurements

A temporary profiling build of the preceding candidate recorded 25 seconds of
CPU samples during fast-profile reverse TCP. The transfer measured 93.85 Mbit/s;
the daemon used 7.19 CPU-seconds on the hub and 10.13 on the edge. Aggregate guest
CPU remained 89.7%/86.2% idle. These measurements do not support CPU saturation
as the limiting factor in that run. Profiling used a build overlay; it added no
production listener or permanent profiling code.

A subsequent warmed transfer measured 96.50 Mbit/s TCP with 237 retransmissions.
UDP received 99.39 Mbit/s from a 110 Mbit/s offer, with 8.71% loss. The receiver's
rate is used here: iperf's UDP sender summary would incorrectly imply 110 Mbit/s
was delivered. TCP receive windows were about 9 MB, and the outer resequencer
reported no skips. Bulk queue drops and repair expiry still increased. These
observations narrow the investigation; they do not establish one cause of all
remaining losses. Raw profiles, TCP state samples, metrics and transfer reports
are under investigation scratch `profile-v20/`.

## AmneziaWG v3 engine migration — 2026-09-29, `541aacc`

The engine moved from local v1.0.4 to v3.1.20260828 with unchanged wire
settings; the adaptive controller is identical to `e19751c`. Binaries:
A = `e19751c` (`3174b9f2…`), B = `541aacc`/`6de11e0` (`6de11e0b…`).

Mixed versions (`20260929-100235-mixed-versions-awg3`, basic profile): old hub
with new edge, old edge with new hub, and new/new each re-established in
about 0.15 s, moved 6.26–6.42 Mbit/s in each direction with both WAN counters
advancing, and lost 0/270 pings across a 135-second window containing the
120-second rekey (last handshake 40–45 s old at the end).

Matched runs in ABBA order (A, B, B, A) on one lab session:

| Scenario | A run 1 | B run 1 | B run 2 | A run 2 |
| --- | --- | --- | --- | --- |
| Fast up/down Mbit/s | 95.20 / 96.78 | 96.43 / 98.67 | 97.00 / 95.58 | 94.97 / 98.20 |
| Radio up/down Mbit/s | 1.186 / 72.67 | 1.186 / 73.16 | 1.186 / 72.04 | 1.185 / 73.58 |
| Radio continuity loss hub/edge % | 0.769 / 0.554 | 0.954 / 1.446 | 0.892 / 0.708 | 1.231 / 1.262 |
| Radio continuity p99 RTT hub/edge ms | 199.4 / 188.4 | 207.9 / 206.9 | 208.1 / 201.1 | 208.5 / 206.9 |
| Radio continuity max gap hub/edge ms | 104.8 / 134.2 | 116.4 / 189.5 | 141.7 / 147.8 | 172.7 / 206.1 |
| Basic continuity | pass | pass | pass | pass |

TCP outage progress passed in every continuity run. Radio throughput and radio
voice latency fail their gates for both binaries, as before the migration.
The B values lie within the A spread for radio throughput and continuity; the
fast-profile differences are about 2 Mbit/s with two runs per binary. These
runs therefore show no measurable engine regression and no engine-attributable
improvement.

Radio calibration (`20260929-101244-calibration`) failed its gate on one leg:
plain TCP on the 100 Mbit/s, 40±30 ms LTE downlink reached 16.9 Mbit/s,
while UDP on that leg delivered 96.8 Mbit/s and every other TCP/UDP leg reached
at least 91% of its rate. Per-packet netem jitter reorders that link; UDP
calibration is the capacity reference for radio results. Earlier radio
results were not calibrated.

## Hold-and-pulse control, traffic classes and path MTU — 2026-09-29

Binaries: A = `25aba42` (`e453ff5f…`, deployed that day), B = the candidate
(`d431571a…`). Matched runs in ABBA order on one lab session
(`ab-series4.log`):

| Scenario | A run 1 | B run 1 | B run 2 | A run 2 | Gate |
| --- | --- | --- | --- | --- | --- |
| Fast TCP up/down Mbit/s | 93.49 / 101.30 | 100.43 / 100.19 | 103.43 / 101.92 | 90.19 / 91.49 | 96 |
| Radio TCP up/down Mbit/s | 1.149 / 60.12 | 1.288 / 77.67 | 1.254 / 83.54 | 1.117 / 73.19 | 1.2375 / 75.375 |
| Four-flow radio download from idle, 20 s mean Mbit/s | 60.8 | 72.1 | 74.9 | 76.8 | — |
| Radio continuity voice loss hub/edge % | 2.185 / 2.369 | 0.185 / 0.185 | 0.031 / 0.215 | 2.154 / 0.862 | 1 |
| Radio continuity max gap hub/edge ms | 201.4 / 294.6 | 114.7 / 120.7 | 128.1 / 109.4 | 156.9 / 111.1 | 150 |
| Radio continuity p99 RTT hub/edge ms | 198.1 / 198.6 | 184.6 / 186.0 | 206.5 / 198.7 | 211.1 / 203.9 | 150 |
| Radio continuity TCP outage progress | pass | pass | fail (both receivers, LTE outage) | pass | pass |
| Basic continuity p99 RTT hub/edge ms | 99.4 / 103.8 | 82.8 / 75.3 | 87.9 / 72.8 | 135.3 / 126.6 | 150 |

B passes every TCP throughput gate in both runs, and voice loss and gap. It
fails the voice p99 gate, as A does, and TCP outage progress in one run of two.
Three further runs of B before the matched series gave radio downloads of
80.4, 70.2 and 82.9 Mbit/s: one of five below the gate.

Constant-rate UDP (`udp.py`, B only, one run per profile): fast 104.7 / 105.4
Mbit/s up/down (gate 102.4), radio 1.359 / 81.6 (gates 1.32 / 80.4).

What the traces showed, and what remains:

- Voice median RTT in the radio continuity scenario fell from 90-100 ms to
  42-60 ms: voice now keeps the low-latency WAN. The p99 over the run stays
  above 150 ms. Per five-second window it is 95-150 ms in most windows; the
  first five seconds of a cold start reach 135-300 ms, and the window in which
  only the LTE WAN is up is bounded by that WAN's own jitter (two directions of
  40±30 ms give about 135 ms before any queueing).
- During the LTE outage two voice streams need 56-74% of the remaining WAN
  (0.4 Mbit/s up, 0.5 down). Bulk keeps 5% of it, about two datagrams per
  second, and TCP also backs off after losing 100 Mbit/s of capacity, so some
  one-second intervals deliver nothing. More for bulk cost voice in the
  deterministic model (`TestVoiceSurvivesOnSingleSlowLane` with an equal split:
  490/500 delivered, 129 ms one-way p99).
- A voice datagram in flight on a WAN that fails is recovered about 250 ms
  later unless a copy was already on the other WAN. The copy allowance of 10%
  of capacity covers about half of two voice streams on these WANs; the
  longest gap at the Starlink outage was 135-175 ms in two of five runs.
- Path MTU discovery converged below the links' 1500 bytes in about half of all
  runs before `9e9f7fb`, across every binary, shrinking the tunnel MTU by up to
  170 bytes. That added noise to every earlier measurement in this file. After
  it, 8 of 8 runs converged to 1500.
- `iperf3 --omit` makes its UDP totals inconsistent (received bytes and packet
  counts disagree); `udp.py` measures from the receiver's interval reports.

### Final tree

The `nix build` binary of the final tree (`d178eff6…`), two rounds, not matched
against A: fast 102.5 / 102.4 and 103.1 / 103.3 Mbit/s up/down; radio upload
1.289 and 1.291; radio download 73.4 (below the gate) and 84.2; voice loss
0.09-0.15%; longest voice gap at a link failure 109 and 128 ms (158 ms once at
0.5 s into a cold start); voice p99 178-204 ms; TCP outage progress pass and
fail. UDP: fast 104.3 / 105.6, radio 1.333 / 83.2. Over all runs of B, radio
download was below the gate in two of seven.

### Mixed versions and daemon restarts

Basic profile, candidate B against `25aba42` (the build deployed at the time):

| Hub | Edge | Traffic both ways | Rekey, ping loss |
|---|---|---|---|
| B | `25aba42` | yes | 0 of 270 |
| `25aba42` | B | yes | 0 of 270 |
| B | B | yes | 0 of 270 |

The adaptive wire format did not change, so either end can be upgraded first.

Time from restarting one daemon until a ping from the edge succeeds again:

| Restarted | Build | Seconds |
|---|---|---|
| edge | B | 0.70 |
| hub | B | 16.1 |
| hub | `25aba42` | 16.1 |
| hub | `6de11e0` | 16.1 |
| hub | pre-v3 `e19751c` | 16.1 |

The hub does not initiate: after its restart the edge keeps sending under the
session the hub has lost, and starts a new handshake only when the engine's
new-handshake timer fires (`KeepaliveTimeout` 10 s + `RekeyTimeout` 5 s without
a reply). The figure is the same on every build, including the one before the
engine migration. The cause is inferred from the timer constants, not traced.

With the edge initiating on a concentrator restart (edge build `r1`, basic
profile; the hub build does not matter, the edge build does):

| Restarted | Edge build | Hub build | Seconds |
|---|---|---|---|
| hub | `r1` | `r1` | 1.58, 1.58, 1.58 |
| hub | `r1` | `05ed8bd` | 1.59, 1.59 |
| hub | `05ed8bd` | `r1` | 16.1, 16.1 |
| edge | `r1` | `r1` | 0.70, 0.70 |

### Copy allowance of 10% against 20% — 2026-09-29

A = `hub-restart` with the allowance at 10% (`11a6b020…`), B = the same with
20% (`8df04d31…`); order A, B, B, A, B, A; radio profile, two continuity runs
per step. "p99" is over datagrams sent after the first five seconds while the
Starlink WAN was up, hub / edge.

| Binary | Voice p99, ms | Voice loss, % | Radio up / down, Mbit/s | Continuity gates |
|---|---|---|---|---|
| A | 212/220, 158/162, 155/150, 144/149, 178/176, 136/139 | 0.03-0.43 | 1.257/82.8, 1.290/80.6, 1.290/70.8 | 2 of 6 pass |
| B | 212/207, 146/133, 134/134, 175/174, 158/156, 173/160 | 0.00-0.58 | 1.290/74.0, 1.289/75.8, 1.289/81.2 | 0 of 6 pass |

The two are indistinguishable; the allowance stays at 10%. Between 0.7% and
3.5% of voice datagrams took over 150 ms with Starlink up, more than its
emulated loss accounts for, so late recovery of lost datagrams is not the main
source of the tail. Its source has not been identified. Two of the failures of
B were gaps of 205 and 206 ms in the first two seconds. All six basic-profile
continuity runs passed. Radio download was below its gate in one run of each
binary.

### Voice beside bulk: copies, growth by evidence, lane ranking — 2026-09-29/30

The comparison above changed the allowance alone and found nothing because the
allowance was not the limit. Each cause below was reproduced in the
deterministic model or by a trace before it was changed:

| Cause | Observation | Change |
|---|---|---|
| Copies had no room on the second WAN | Bulk filled its window and pacing slots; four in ten voice datagrams went uncopied at 10% and at 20% | The other lanes reserve capacity for copies; allowance 20% |
| The standby WAN's target drifted above its capacity | Pulses on a lane sending below its target always "succeeded"; 81-97 kB/s on the 62.5 kB/s WAN, then 300-390 ms voice round trips when LTE failed | No pulse on a lane sending below its target; on the voice lane discovery is bound by recent delivery |
| A voice backlog formed at a failure drained over seconds | Remaining capacity barely exceeds two voice streams | Real-time datagrams that waited give way to those behind them |
| One unloaded sample replaced a lane's round trip | The 80 ms LTE lane ranked at 23 ms ahead of the 46 ms lane; voice moved to it (about one model run in a hundred) | The sample moves the estimate halfway |

A = `main` at `b2a72d7` (`11a6b020…`); E3 = the first three changes
(`9ddc6f56…`); E5 = all four, the candidate (`34921a4d…`). Interleaved within
each series. Voice p99 is over datagrams sent after the first five seconds
while the Starlink WAN was up, hub / edge, radio profile.

| Series | Binary | Voice p99, ms | Round trips over 200 ms per stream | Radio continuity | Basic continuity | Radio up / down, Mbit/s |
|---|---|---|---|---|---|---|
| 10 | A | 184/156, 152/150, 169/172, 127/141 | 4-24 | 1 of 4 | 1 of 2 | 1.290/81.2, 1.288/78.2 |
| 10 | E3 | 115/111, 118/122, 127/130, 115/106 | 0-4 | 3 of 4 | 2 of 2 | 1.289/73.5, 1.257/81.8 |
| 12 | A | 202/183, 183/163, 139/142, 254/242 | 11-63 | 1 of 4 | 4 of 4 | 1.255/79.7, 1.291/83.8 |
| 12 | E5 | 104/109, 108/107, 114/111, 126/129 | 0-2 | 1 of 4 | 4 of 4 | 1.291/74.5, 1.254/81.4 |

Every voice p99 of E3 and E5 is below 150 ms; 11 of 16 of A are not. What
still fails on the candidates is not the voice latency with Starlink up:

| Failure | E3 | E5 | A |
|---|---|---|---|
| Voice p99 while only LTE is up (gate 181.6 ms) | 1 of 4 (200.8) | 1 of 4 (207.9) | 5 of 8 |
| TCP without delivery for 3 s while only Starlink is up | 0 of 4 | 2 of 4 | 0 of 8 |
| Voice p99 with Starlink up | 0 of 4 | 0 of 4 | 6 of 8 |

Costs and limits:

| Measurement | A | E3 / E5 |
|---|---|---|
| TCP upload beside two voice streams, whole scenario, Mbit/s | 0.47-0.58 | 0.31-0.36 |
| TCP download beside two voice streams, whole scenario, Mbit/s | 32.0-52.1 | 39.0-53.6 |
| TCP delivered in the five seconds with only Starlink up, series 12, kB, down / up | 3-34 / 7-18 | 0-7 / 0-11 |
| Throughput without voice (`benchmark.py`) | unchanged | unchanged |

The upload beside voice carries about a third less TCP: the copies take up to
20% of a 1.65 Mbit/s uplink. With only the 0.4/0.5 Mbit/s Starlink WAN up, two
voice streams take 56-70% of it and the target no longer exceeds its capacity,
so TCP gets less than before and sometimes nothing for the five seconds. A
model of a backed-off sender's retransmissions on that lane delivered 75-90%
of them, so the lab's TCP stall is not explained by the tunnel dropping them;
its cause is open.

Rejected on the way, each measured in a series of its own:

| Variant | Result |
|---|---|
| Discovery bound on every lane, by current delivery (`e2`) | One run without any TCP delivery in 65 s; slow TCP start in the others |
| Unloaded sample also counted towards the variation (`e4`) | Both basic-profile runs failed: voice moved to the 25 ms lane, 246 ms gap at its failure |
| Catch-up rule alone decides nothing in the lab | E0 (without) and E1 (with) within run-to-run variation; kept on the model's evidence (287 late of 5600 without, 21 with) |

Series 6-9 and 11 (`C20`, `E0`, `E1`, `e2`, `e4`) are in the lab state
directory under the binaries' hashes.

### 300+300 Mbit/s links — 2026-09-30

`profiles/gigaradio.json`: the radio profile's delay, jitter and loss at
300 Mbit/s per WAN. Plain TCP over the emulated Starlink WAN alone reaches
8 Mbit/s (random 0.4% loss at 40 ms round trip, Mathis's bound), so the
calibration's TCP check does not apply to this profile; UDP calibrates at
290 Mbit/s per WAN. The tunnel's own CPU on the 4-vCPU guests stayed below
1.5 cores at 600 Mbit/s and no thread above a third of a core.

First runs of `main` at `e14db98`, before the lab corrections described under
"Scenarios and gates": TCP 31 / 93 Mbit/s, UDP 229 / 250 (up / down, gates
450 / 480).

Changes kept, each reproduced first:

| Cause | Reproduction | Change |
|---|---|---|
| Any timed-out datagram was a congestion signal; at 300 Mbit/s a 0.4% random loss times something out in every control interval | model lane collapsed from 28 to 3 MB/s within 12 s | loss is congestion only when a round lost 3+ datagrams and 2% (the startup rule, in every state) |
| Before eight delay differences, one delayed sample ended discovery | 30 ms jitter lane held at 16 kB/s for 6 s of a cold start | the probes' unloaded variation stands in for the spread |
| The resequencer's 2048-frame window abandoned a gap once 2048 later frames arrived | unit test: 12500 frames arrive during a 250 ms repair at 600 Mbit/s; in the lab every abandoned sequence arrived afterwards as a stale frame | window of 32768 frames; the engine's anti-replay window raised from 8128 to 131008 messages to stay above it |

Candidate W (`wanbond-w1`, branch `fast-links`) against `main` (A,
`11a6b020…`), W A A W:

| Measurement | A | W |
|---|---|---|
| Radio TCP up / down, Mbit/s | 1.256/67.6, 1.292/83.6 | 1.291/82.6, 1.254/79.9 |
| Radio continuity (corrected 2026-09-30, see below) | 1 of 2 pass: voice p99 197/203 ms with the low-latency WAN up (gate 150), a 201 ms gap | 2 of 2 pass |
| 300+300 UDP up / down, Mbit/s | 254/240, 227/219 | 319/346, 353/382 |
| 300+300 TCP up / down, Mbit/s (20 s after a 15 s warm-up) | 134/103, 151/70 | 87/98, 114/161 |

W carries about half as much more UDP at 300+300 Mbit/s, the radio profile
does not move, and TCP at 300+300 Mbit/s is within run-to-run variation on
both (50-205 Mbit/s over all runs of all builds today). Neither reaches the
gates at 300+300 Mbit/s.

Why TCP stays near 100 Mbit/s, from traces of W-equivalent builds:

- TCP's window sits at 1-3 MB with a 100-230 ms round trip: in-order delivery
  across the two WANs costs the slower one's latency and jitter, and the
  resequencer is holding for some gap during the whole transfer on every
  build. A loss reaches TCP every 10-15 s; at that round trip CUBIC needs
  longer than that to grow.
- The lanes' targets stay at 6-30 MB/s of 37.5 during a TCP transfer. Of 32
  rate cuts in a 30 s transfer, 27 were loss cuts, 10 of them on the lossless
  5G WAN: jitter carries round trips past the repair timer (100-125 ms against
  a 140 ms tail), those timeouts count as loss, and TCP's small rounds make
  one or two of them material. Discovery then ends at whatever TCP happened
  to be sending.
- The losses TCP sees are gaps the resequencer abandons (2-15 a minute) and
  the tunnel's own CoDel drops.

Tried and rejected, each measured:

| Attempt | Result |
|---|---|
| Timeout bounded by the recent peak confirmation time | TCP 12 / 37 Mbit/s in the lab: repairs approach the 250 ms lifetime. In the model it also removed the controller's loss signal, and a 75 ms queue stood in the path |
| Timeout including the recently measured queue delay | within variation |
| Window sized from confirmation time | no gain; two model tests fail |
| Repair from acknowledgement gaps (a lost datagram is one that later acknowledged sends have passed) | no lab gain. At 27000 datagrams a second the acknowledgement's 256-entry receipt bitmap spans 10 ms, less than any usable reordering allowance, so nothing inside it is old enough to judge; judging what has left the bitmap produced false verdicts. It needs a receipt bitmap that scales with the rate, a wire format change |
| Gap-detected repairs on the earliest-arrival lane | abandoned gaps 3-15 a minute against 2-4 |

Open, in the order I would take them: an acknowledgement format whose receipt
bitmap scales with the rate, then repair from gaps; a loss signal that does
not count timeouts later shown spurious; a capacity estimate that is not
taken from an application-limited transfer.

### Loss window and further repair experiments — 2026-09-30

Branch `tcp-fast` on top of `main` at `1802c9e`.

Kept: material loss is judged over the last second instead of one delivery
round (`6199555`). In the model a bursty sender at a third of a lossy
300 Mbit/s lane's capacity saw the target cut below its own rate (11.7 of
12.0 MB/s offered, 8 cuts in 20 s); with the change the target stays at 19.8
and is cut twice. Acknowledgements now leave in lane creation order
(`668f40a`), which makes two-lane model runs repeatable: six executions of
`TestLightlyLoadedLaneTargetStaysWithinCapacity` gave six targets before and
one after.

T = `6199555` (`wanbond-t1`), W = `main` (`wanbond-w1`), order T W W T:

| Measurement | W | T |
|---|---|---|
| Radio TCP up / down, Mbit/s | 1.222/82.4, 1.289/83.7 | 1.290/73.0, 1.291/80.6 |
| Radio continuity (corrected 2026-09-30, see below) | 2 of 2 pass | 1 of 2 pass: voice p99 192/183 ms with only the 40±30 ms WAN up (gate 181.6) |
| 300+300 UDP up / down, Mbit/s | 291/321, 314/244 | 291/367, 407/405 |
| 300+300 TCP up / down, Mbit/s | 107/98, 122/82 | 103/81, 121/73 |

No measurable difference on either profile; `668f40a` was not in the binary
measured.

What limits TCP at 300+300 Mbit/s, measured on T-equivalent builds:

- In four 60 s transfers, TCP retransmitted 5-10 times and the resequencer
  abandoned 1-10 gaps; the two counts track each other. Every abandoned
  sequence arrived afterwards as a stale frame: the repair was late, not lost.
- 922 repairs in 40 s left more than 150 ms after the first transmission. On
  the lossy WAN the timer that triggers them was 150-175 ms (round trip
  40 ms), so a repair through the 5G WAN arrives 190-245 ms after the
  original, at the edge of the receiver's 250 ms hold. On the lossless 5G WAN
  they came in bursts of 9-68: datagrams unacknowledged after 150-175 ms
  while acknowledgements kept arriving.

Not kept, each tried in the deterministic model on a saturated lossy lane:

| Attempt | Result |
|---|---|
| Verdict from receipt reports: unreported while a send this much later is confirmed | verdicts fire 25-100 ms after the first transmission, but the repair then waits for a window slot behind the datagram it replaces; repaired datagrams still arrive at 180-210 ms |
| The same, with the repair taking the lost datagram's place in the window | the target then fell to 22 of 37.5 MB/s for ten seconds in one model, and runs were not repeatable |
| Verdicts limited to sequences the lane bitmaps covered without a hole, acknowledgements every 32 datagrams | acknowledgements arrive out of order through the reverse path's jitter, so the coverage always has holes; the more frequent acknowledgements also left a 24 ms queue in the steady-state model |
| A shorter timer for repairs than for declaring loss | repairs still arrived at 226 ms: on a saturated lane they wait for the window, not the timer; two other models regressed |
| Ignoring delay signals on a lane that sends below its target | no effect on the reproduced case; broke `TestLateACKStillMeasuresCongestion` |

The saved work is in the stash on `tcp-fast` ("gap repair v5 WIP").

Open: on a saturated lane a repair waits for the window, and the saturated
lossy lane of the model carries a 35 ms standing queue on `main` as well
(transit p50 58 ms against 20 ms of path). A model of a TCP sender through the
real resequencer would let these be judged in seconds instead of half-hour lab
series whose TCP results vary between 50 and 200 Mbit/s.

### Latency with memory — 2026-09-30

A deterministic model of one TCP transfer (CUBIC-like, selective
acknowledgements) through two bonded endpoints and the real resequencer
(`internal/bond/tcp_model_test.go`; the report is `-tags model`) showed what
the netem profiles cannot: with latency that wanders as the measured links'
does, a lane can be held at its minimum rate. Queue delay is measured from
the lowest transit time seen; a wandering path sits above that floor most of
the time, and when the wander is slow the samples of a control interval move
together, so their minimum is no nearer the floor than any of them.

Model, 300+300 Mbit/s unless stated, last 30 of 60 s, Mbit/s (T =
`668f40a`, X = the candidate):

| Scenario | T | X |
|---|---|---|
| Measured latency on both lanes, no loss | 496 | 517 |
| Measured latency, 0.1% loss on the satellite lane | 217 | 451 |
| Steady latency, no loss | 525 | 525 |
| Satellite lane alone, measured latency | 234 | 203 |
| Satellite lane alone, scatter without level shifts | 263 | 258 |
| Mobile lane alone, measured latency | 0 | 265 |
| Today's downlink (0.5+100 Mbit/s), measured latency | 86 | 88 |

X changes two things, each with a failing model test first:

| Cause | Change |
|---|---|
| A fixed 10 ms threshold above the transit floor | the threshold also stands above the delay seen while the lane sent less than half of what it can carry (`TestWanderingLatencyIsNotAQueue`) |
| A timeout counted as loss at once; a confirmation delayed by jitter or the acknowledgement cadence arrives after it as often as a loss does (77 timeouts a second on a model lane losing 37) | the datagram is sent again at the timeout but counts as lost only if still unconfirmed after a second one, or confirmed no sooner than its repair could have been |

Lab, X (`wanbond-x1`) against `main` (W, `wanbond-w1`), order X W W X:

| Measurement | W | X |
|---|---|---|
| Radio TCP up / down, Mbit/s | 1.290/49.7, 1.290/73.2 | 1.291/83.0, 1.323/82.1 |
| Radio continuity (corrected 2026-09-30, see below) | 2 of 2 pass | 0 of 2 pass: voice p99 203/205 and 209/183 ms with only the 40±30 ms WAN up (gate 181.6); TCP not resumed after the LTE recovery once |
| 300+300 netem jitter, TCP up / down | 115/125, 73/86 | 101/125, 67/139 |
| 300+300 netem jitter, UDP up / down | 351/306, 308/321 | 361/316, 286/279 |

With `wander.py` on both WANs and no loss, order W X X W:

| Measurement | W | X |
|---|---|---|
| 300+300 wandering latency, TCP up / down, 60 s | 83/45, 116/58 | 102/132, 147/122 |
| 300+300 wandering latency, UDP up / down | 121/129, 137/96 | 187/162, 184/182 |

X is better wherever latency has memory, in the model and in the lab. It is
worse in one respect the series did not catch at the time (its continuity
row above is corrected): voice with only the 40±30 ms WAN up got 25-30 ms
slower, the wander allowance taken by per-datagram jitter (`001b5cf` corrects
it; see the next section). With wandering latency the lab stays far below the model
(UDP 162-187 of 600 Mbit/s, against 451-517 for TCP in the model) and below
its own netem-jitter figures; the delay changes reorder datagrams within a
WAN there, and how much of the gap that accounts for is not known. The
satellite lane alone loses 13% in the model with X: a level shift upwards
still reads as queue until the ten-second baseline refresh.

### Corrected continuity readout and two corrections on `main` — 2026-09-30

`regress_series.sh` reported `rc=0` for every continuity run regardless of
the result (the exit code it echoed was `basename`'s), so the "2 of 2 pass"
rows of the three preceding sections were wrong; they are corrected in
place from the recordings (`continuity_gates.py` on each run directory).
Every recorded radio-profile continuity run, re-evaluated per build; voice
round-trip p99 over both guests in ms, with only the 40±30 ms WAN up (gate
181.6) and with the 20±10 ms WAN up (gate 150):

| Build | Runs | Pass | Only 40±30 ms WAN up | 20±10 ms WAN up | Other failures |
|---|---|---|---|---|---|
| `1802c9e` (fast-links) | 16 | 12 | 148-194 (mean 167), over the gate in 3 runs, all after 19:00 | 110-137 (123) | TCP gates in the LTE outage once |
| `4aa5948` | 13 | 5 | 172-212 (mean 188), over the gate in 8 runs | 114-149 (129) | TCP gates in the LTE outage twice |
| `4aa5948` + ledger + floor test + scatter discount (branch `level-shift`) | 15 | 8 | 157-195 (mean 171), over the gate in 2 runs | 108-143 (121) | TCP gates in the LTE outage five times; a 305 ms voice gap once |

The voice regression of `4aa5948` is the wander allowance taken by
per-datagram jitter: the lowest of a control interval's few samples lies
above the floor by the spread over one more than their number, and that
raised the 40±30 ms WAN's threshold by 20 ms and the queue voice shares with
bulk there by as much. `001b5cf` discounts it
(`TestVoiceOnJitteryLaneAloneKeepsItsLatency`: one-way p99 142 → 108 ms; the
model's ten-seed averages move by 1-3%, within their spread). `94b7783`
bounds how long a probe's queue explains delay: a lane that lost half its
capacity while a probe drained held its target at 75% of the old capacity
with the path's buffer full for as long as the loss lasted
(`TestCapacityDropIsNotALevelShift`, failing on `4aa5948` for 50% and 25%
remaining when the loss fell on a probe). Both are cherry-picked from
`level-shift`; the ledger and the floor test stay on the branch until
production measurements show the defects they address on real links.

The TCP gates in the LTE outage are marginal by construction: bulk keeps 5%
of the 0.4 Mbit/s WAN while voice takes 70%, about two datagrams a second,
and every build delivered 7-11 bulk frames to the hub in those five seconds
(polled once a second, the hub's slow lane held a 55-62 kB/s target and gave
bulk 1.5-3 kB/s with every build). Whether TCP's retransmissions land in the
windows decides the gate; the evening runs were the noisier ones for every
build. Under `wander.py` a quarter of the wire bytes are repairs of datagrams
that arrived (an instrumented build showed the hub's duplicate count growing
by the sender's repair count): netem reorders datagrams within a WAN
whenever it lowers the delay, the acknowledgement's lane bitmap covers the
64 newest lane datagrams and its global bitmap 256 sequences shared by both
lanes, and the late confirmations time out. Production shows no such
reordering (lane acknowledgements confirmed 98% of the bytes the edge sent
and 88% of the bytes sent back, read-only metrics of 2026-09-30), so the lab
figures under `wander.py` are a lower bound.
### Loss from byte counts, level shifts, and a policed WAN — 2026-09-30

Branch `level-shift` (rebuilt on `main` `aef36c9` as `level-shift2`), each
change with a failing test first; the scatter discount and the probe-drain
expiry of the previous section came from it:

| Cause | Change |
|---|---|
| A timeout cannot tell a lost datagram from a late one; under `wander.py` 284 loss cuts in one transfer over links that dropped nothing | loss is measured from the receiver's cumulative byte count per lane against the bytes sent through the acknowledged sequence, settled over 200 ms and judged over a second (`lossLedger`); timeouts still trigger the repair (`TestLossLedgerTellsLateFromLost`) |
| A latency level that moves up reads as a queue until the ten-second baseline refresh; a 15 ms shift on a 300 Mbit/s lane cut delivery by up to half for two to three seconds, and one that fell on a probe held the target at 75% for as long as the level lasted | on the second consecutive delay signal of a holding lane, when neither a target above the estimate nor a probe or cut still draining explains it, bulk pauses for twice the delay and the samples sent afterwards give the verdict: floor where it was, the delay was a queue and the estimate drops 3%; still late, the floor moved and the target stands (`TestLatencyLevelShiftDoesNotCutTheTarget`) |
| The production satellite link polices rather than queues: on 2026-09-30 at 20:21 the concentrator sent 107 kB/s into it while it delivered 65, for fifteen seconds, with queue delay under 7 ms and the loss repaired over the other lane. In the model of such a path (a two-datagram token bucket beside a 100 Mbit/s lane) the target stood at 1.04-1.10 of capacity with 4-10% loss | the loss cut goes below delivery (95%, not 105%) and lowers the estimate to it when the lane was sending its target; material loss is judged over 150 datagrams, up to 4 s, and is its own confirmation; a probe is won only after 30 datagrams and a settling period without loss, and loss in that time returns the estimate and doubles the wait before the next probe, up to 16 s (`TestPolicedLaneIsNotOverdriven`: 1.3% loss, all in the seconds a probe ran) |

The reordering measurement introduced with the ledger compared the
datagrams one acknowledgement confirms with each other in map order, so an
in-order path measured 37 ms of reordering and model runs were not
repeatable (`TestInOrderPathShowsNoReordering`; the datagrams one
acknowledgement confirms arrive in an order it does not tell).

#### Model

Deterministic TCP model, 300+300 Mbit/s unless stated, last 30 of 60 s,
Mbit/s. The model's loss now draws from its own random stream, so every
version meets the same path; the figures are not comparable with the
previous section's. Four seeds, X against Z (all four changes):

| Scenario | X | Z |
|---|---|---|
| Measured latency on both lanes, no loss | 511, 530, 522, 533 | 519, 522, 523, 529 |
| Measured latency, 0.1% loss on the satellite lane | 503, 486, 486, 474 (2423 and 3464 TCP retransmits in two of them) | 447, 494, 373, 511 (462 in one) |
| Satellite lane alone | 263, 262, 262, 244 | 250, 250, 267, 266 |
| Today's uplink, 0.4+1.25 Mbit/s | 1.40, 1.34, 1.37, 1.40 | 1.39, 1.35, 1.38, 1.36 |
| Today's downlink, 0.5+100 Mbit/s | 86.5, 87.6, 86.9, 89.4 | 86.8, 88.2, 86.0, 89.0 |

Over ten seeds the lossy scenario averages 473 Mbit/s without the scatter
discount and 456 with it (paired difference −16 ± 44); the satellite lane
alone 259 and 257. The report (`-tags model`) now also prints the wait for
the link that 95% of the sender's datagrams stayed below, per lane: with the
first version of the floor test, which swallowed the congestion signal when
the delay turned out to be a queue, the lossy scenario ran with targets a
quarter above capacity and 40 ms standing in both paths; the verdict that
lowers the estimate brought that back to 17-31 ms (X: 15-24 ms).

#### Throughput

Both profiles, interleaved runs through the day (Z4 and Z6 differ only in
the scatter discount and the floor-test verdict), Mbit/s:

| Measurement | X | Z4 / Z6 |
|---|---|---|
| Radio TCP up / down | 1.322/83.0, 1.290/83.1, 1.324/79.9 | 1.290/73.4, 1.322/81.1, 1.326/75.1 |
| 300+300 netem jitter, TCP up / down | 102/144, 167/67, 111/75 | 206/62, 89/115, 124/61 |
| 300+300 netem jitter, UDP up / down | 382/363, 377/396, 405/418 | 391/379, 397/362, 397/408 |
| 300+300 `wander.py`, TCP up / down, 60 s | 96/117, 148/140, 112/132 | Z3 204/181, 200/198, 234/197; Z6 224/196, 236/201 |
| 300+300 `wander.py`, UDP up / down | 160/106, 202/160, 220/178 | Z3 246/191, 222/167, 246/222; Z6 242/181, 198/125 |

The ledger alone (Y, before the floor test) gave 179-239/184-202 TCP under
`wander.py` in seven runs; the floor test adds nothing measurable there, and
the model says why: the lab's latency steps reorder datagrams, its level
shifts are what the floor test is for, and the random path of the model's
report has small ones. Radio download is 5-10 Mbit/s lower with Z in three
of three pairs; the netem-jitter TCP figures scatter too much for three
pairs to say anything.

#### The policed WAN

`radio-policed.json` is the radio profile with the 0.4/0.5 Mbit/s WAN
policed (a token bucket of two datagrams, `"police": true`, `lab.py`).
Before the loss responses above were limited to a lane sending its target
(Z7), the lane that took over voice when the other WAN failed sent 30 kB/s
of a 42 kB/s target, lost to its bursts, was cut to 25 kB/s and dropped
96 voice datagrams in its own queue: voice loss 1.35-1.57% in two of three
runs against a gate of 1%. With the limit (Z8), three continuity runs lost
0.06-0.49% (X: 0.06-0.28%); TCP up/down 1.255-1.290 / 60-82 Mbit/s against
X's 1.288-1.291 / 68-79 in three pairs.

## A call beside bulk: one lane, a flood, and the lane a call rides — 2026-10-01

Three defects found while modelling a lane that queues below its mean rate.
Each has a failing-first model test in `internal/bond` and, where the lab can
show it, a scenario above. `main` here is `09b5d01`.

**Bulk beside a call on the only lane.** `single_lane.py`, `profiles/basic.json`
with WAN1 dead (one 6 Mbit/s WAN):

| Build | TCP upload, Mbit/s | Voice round trip p99, ms | Voice loss |
| --- | --- | --- | --- |
| `main` | 0.0, 0.0, 0.0, 0.0 | 60-68 | 0% |
| `2a13a88` and later | 4.02, 3.97, 4.03, 3.97 | 70-75 | 0% |

The lane stayed in discovery with a target of 64-65 kB/s for the whole run.
A lane that carries a real-time stream before its capacity is known has its
target held near recent delivery; the call was more than half of that target,
which made the lane a real-time lane and left bulk its minimum share.

**The lane a call rides.** With that fixed, bulk discovers a lane that carries
a call, and one call on a 0.5 Mbit/s lane is under the half that made a lane
a real-time lane. Radio continuity, voice round trip with only the 40±30 ms
WAN up, gate 181.6 ms: `2a13a88` alone failed 4 of 5 runs, `main` 3 of 5 in
the same session. `48e8f19` keeps bulk off a lane below 1.2 Mbit/s that
carries real-time originals while another lane can take it: 5 of 8 runs pass
for it and for `main`, interleaved; radio TCP 63.8 and 74.4 Mbit/s down
against 66.5 and 82.7; 300+300 TCP 94-119 up and 110-120 down against 107-174
and 67-116; UDP 283-391 up and 207-383 down against 374-385 and 395-403 (every
build of the day produced one such low UDP sample).

**A call behind a flood.** `flood.py`, 400 Mbit/s of UDP offered to 32+96
Mbit/s, each direction twice:

| Build | Voice loss, hub / edge | Longest voice gap, ms | Flood delivered, Mbit/s |
| --- | --- | --- | --- |
| `main` | 61 / 61, 39 / 38, 36 / 35, 37 / 37 % | 810-990 | 40-95 |
| with the admission headroom | 0.08 / 0, 0 / 0, 0 / 0, 0 / 0 % | 40-60 | 92-99 |

The bound on queued and outstanding datagrams was one count for all classes;
`wanbond_adaptive_interactive_queue_drops_total` counted 927-1591 refused
small datagrams per run on `main` and 0-1 with the headroom.

The continuity gates stay marginal for every build: over the day `db465b3`
passed 10 of 15 runs and `main` 18 of 28, in interleaved series 10 of 14
against 11 of 14.

## A link that stalls — 2026-10-01, not run here

Branch `field-stalls` answers what the production mobile link showed on
2026-10-01: stalls of 0.2-0.4 s about once a second, repairs that all arrive
as duplicates, and estimates taken from delivery a stall depressed. The
measurements and the model figures are in
[the improvement plan](../../docs/drafts/20261001-0820-wanbond-improvement-plan.md#field-run-of-2026-10-01-main--b46f1c2).

Nothing of it was measured in this lab. No profile has a WAN that stalls:
netem delays, loses and limits, but serves continuously. A profile that
holds a WAN's rate near zero for a drawn time at drawn intervals is the
missing piece; the rate schedule of `profiles/cellular.json` on branch
`queue-allowance` is the place to add it. The usual series on `radio` and
`gigaradio` with the continuity gates, interleaved with `b46f1c2`, was also
not run: the host's load average was 18.

## Adaptive-policy stage 0 — 2026-10-02, in progress

The [adaptive-policy plan](../../docs/drafts/20261002-1730-adaptive-policy-plan.md)
is being measured against the unmodified transport at `f75668e`. The baseline
lab executable has SHA-256
`59df1fbe9eed338b122ceab3cf4e512208631d7feaf849290b01135e78537987`.
Its source is a detached checkout; model infrastructure and progression tests
are added there separately from production sources.

Observed: the original debug runner completed a full `latency-voice` run,
including collection and summary, in
`20261002-184746-adapt-latency-voice-f75668e-runner-check`. Its interface byte
counters include voice and headers, so they do not establish TCP payload
goodput. The added-delay phase had an edge voice arrival gap of 188 ms; the
restore phase had a hub gap of 300 ms. Neither meets a 150 ms continuity gate.
These are preliminary baseline observations, not a three-run acceptance series.

Observed in the deterministic model: the preliminary voice-only 3a latency
test passed on both profile families at `f75668e`. Code inspection explains
why a frozen primary rank does not imply a frozen receive latency: copies on
the other lane can arrive first. Primary-lane movement and delivery latency
must therefore be measured separately. The remaining preliminary model
matrix has failures in every row, but its radio directions and wire framing
required correction before treating its throughput results as gate evidence.

The stage 0 diagnostic change exposes the legacy policy's floor, floor age,
path-delay input, rank, live/dead state and rejected-frame counts. It enforces
the documented monotonic-time precondition. It does not replace the control
policy. Observed validation: all 42 frontend tests, the root Go build/vet/tests,
patched device vet/tests and formatting passed the non-privileged gate.
Progression scenario tests remain behind `-tags adaptivepolicy` until their
implementation stages; they are not claimed as passing that gate.

`test/vm/adapt.py` now collects complete runs and preserves the binary digest,
initial conditions, actual event times, guest clock offsets, raw voice/TCP,
10 Hz metrics/interface samples and daemon logs. `--profile` selects `radio`
or `gigaradio`; rate changes are proportional to that family's rates.
Collection scenarios cover one-way outages, deep/shallow rate falls, rises,
plan changes, cellular grants, latency changes and a cold transfer. The
display still uses interface bytes; the independent available-goodput
reference and complete section 4 gate evaluator are unfinished. It must not
be used to claim acceptance from a printed throughput figure.

The corrected model matrix on `f75668e` has the following case counts. It
includes first-second deadline measurements and primary voice-route checks;
these are progression results, not candidate passes:

| Row | Passing / failing cases |
| --- | --- |
| 1a | 3 / 5 |
| 1b | 0 / 4 |
| 1c | 4 / 12 |
| 2a | 0 / 4 |
| 2b | 0 / 2 |
| 2c | 0 / 2 |
| 2d | 0 / 2 |
| 3a | 2 / 2; both voice-only latency cases pass |
| 3b | 0 / 4 |
| 3c | 0 / 4 |
| 0 | 0 / 2 |

**Initial stage 0 stop: lab and field disagree.** Observed with two 50 Hz
160-byte echo streams and no TCP: a 15-second outage of mobile egress on
the edge gave these results:

| Place | Edge / hub echoes lost during outage | Edge / hub longest arrival gap |
| --- | --- | --- |
| Production | 0 / 0 of 750 each | 62.3 / 69.8 ms |
| Lab, field profile | 10 / 40 of 750 each | 177.2 / 880.3 ms |

The field daemons are identified as `f75668e` by the plan; their observed
restart times agree, but their source commit was not independently verified
from the binaries. The lab digest above is from the verified `f75668e`
checkout. This compares the same egress intervention and traffic, not equal
initial lane state or identical physical links: the field daemons had been
running for hours and the lab daemons were freshly started. The cause of the
gate disagreement is unknown; no policy change was made to explain it away.

The lab run is
`20261002-193514-adapt-field-baseline-voice-f75668e-field-match` under the VM
state directory. Field artifacts and exact commands are under
`/srv/nvme/tmp/wanbond-adaptive-evidence/field-voice-20261002-193300` and
`field_voice.py`. Management replies were observed routing over untagged
`end0` via `192.168.222.1`, separately from the impaired VLANs. Each qdisc
change had a verified active 15-second removal timer. Both VLANs subsequently
reported `noqueue`; both services remained active. No candidate binary or
deployment configuration was changed.

The successful 60-second field measurement advanced the mobile interface's
RX+TX counters by 2.396 MB. The counter increase from the first attempted run
through the successful run was 7.931 MB, including failed echo setup and
intervening background traffic. Initial UDP/5202 attempts were inconclusive:
the host firewall did not admit that port. UDP/60099 was already permitted;
both preflights then received 100/100 echoes without changing firewall rules.

Two radio direct-link calibrations failed their existing capacity gate.
After correcting correlation, TCP downlink on WAN2 measured 11.325 Mbit/s
against the required 85 Mbit/s, while UDP measured 96.567 Mbit/s. The cause
of the TCP shortfall was not established, so throughput references remain
inconclusive. Three-run series, existing benchmark/UDP/continuity baselines,
stopgap selection and stages 1–3 remain unfinished. This is a measurement
stop, not an adaptive-policy handover.


### Resumed measurement: host contention and field reference

Operator evidence: the host repeatedly reached 100% CPU during earlier lab
measurements; the later reported load was 9%. The operator directed that field
measurements be the behavioral reference. No historical CPU trace was collected
in those earlier runs, so CPU contention is a hypothesis, not an observed cause.
Their timing verdicts are inconclusive. The numeric gates and the three-run
series remain required.

`adapt.py` now records 10 ms observer wake deadlines, summarized at 10 Hz,
aggregate CPU counters (including steal), load average and per-thread Linux
scheduler counters for each QEMU process and each guest daemon. It retains
qdisc/filter statistics too. Recorder failures or insufficient duration fail
collection. These observations can identify scheduling interference; a small
observer delay cannot exclude a Go runtime stall or establish the cause of an
unrecorded earlier gap. The observer test deliberately stops its own process
for 200 ms and requires that pause to be visible; a missing observed process
must fail, rather than produce valid evidence.

Observed three fresh-daemon lab replays of the same field-profile voice-only
interventions on verified `f75668e`:

| Replay | Mobile outage lost, edge / hub | Outage maximum gap, edge / hub | Maximum guest observer wake delay |
| --- | --- | --- | --- |
| `20261002-201445-…-f75668e-timing-r1` | 4 / 4 of 750 each | 40 / 33 ms | 4.0 ms |
| `20261002-202039-…-f75668e-timing-r2` | 0 / 0 | 21 / 21 ms | 2.1 ms |
| `20261002-202327-…-f75668e-timing-r3` | 0 / 0 | 29 / 32 ms | 35.1 ms |

All three completed collection. Replay 1 still recorded a brief aggregate host
CPU sample near 100%; its maximum observer wake delay was 2.8 ms and maximum
recorded guest-daemon thread runqueue-wait increment was 3.5 ms. Aggregate CPU
utilization alone therefore does not identify a scheduling-induced gate failure.
These replays cover voice-only mobile egress loss and satellite egress delay;
they are not the full two-family section 4 series.

Observed temporary field diagnostic candidate `adapt-s0-10609b9`, built from
`10609b9`, SHA-256
`4a5cf20aa8b3f14fd3d0489ca0808c927211aa99425790e8561c2cb36308a140`:

| Round | Received, edge / hub | Whole-run maximum gap, edge / hub | Mobile RX+TX counter increase |
| --- | --- | --- | --- |
| After restart | 3000 / 3000 | 98.3 / 111.2 ms | 2.871 MB |
| Warmed up | 3000 / 3000 | 126.0 / 130.3 ms | 2.367 MB |

The first scheduler samples were approximately 29–34 seconds after daemon
startup; the warm samples were 314–320 seconds after startup. This was not a
row 0 cold-transfer measurement. Both rounds used two 50 Hz 160-byte echo
streams, no TCP, a 15-second edge mobile-egress blackout and 15 seconds of
100 ms added satellite-egress delay. Artifacts and exact commands are under
`/srv/nvme/tmp/wanbond-adaptive-evidence/field-voice-{fresh,warm}-20261002-*`
and `field_voice_timing.py`. Each impairment's removal timer was verified
before adding its qdisc. Candidate restore timers were verified on both hosts;
both hosts subsequently returned to the deployed store binary with empty
runtime drop-ins, inactive restore timers, no echo listener/helper processes,
and `noqueue` on both edge WAN VLANs. Deployment configuration was not edited.

The diagnostic candidate's `control.go` digest equals that of `f75668e`;
unchanged policy decisions are inferred from code, while the echo results and
running executable digests were observed. Field scheduler observers had
maximum wake delays of 1.4 ms on the edge and 4.0 ms on the concentrator.
Nine epoch rejections were already present in the concentrator's first sample
and did not increase during either round. The warm edge round recorded one
path rejection; its cause beyond that validation category is not established.
The 5.238 MB total is the mobile interface counter increase during the two
measurement windows, including background traffic and excluding preflights
and artifact transfer outside those windows. These continuity observations do
not establish the latency-deadline, bulk, calibration or full acceptance gates.


Operator evidence also identifies the field links as 5G and Starlink and warns
that their metrics vary widely. Their nominal rates and individual idle RTT
samples are not stationary reference values. Field comparisons must retain
both lanes' time series before, during and after each intervention, pair
baseline/candidate rounds closely, and describe `tc` as an added impairment
on top of the observed native conditions. A zero-loss round establishes that
round's continuity, rather than a general property of either link.


Observed calibration follow-up: two isolated 60-second WAN2 downlink tests
measured 95.420 Mbit/s with the radio jitter and 95.489 Mbit/s with jitter
removed; both reached approximately that rate in their early measured
intervals, after the usual three-second omission. The three-run original
calibration procedure still failed its gate, including a 13.002 Mbit/s WAN2
TCP downlink in the first repetition while UDP delivered 95.900 Mbit/s.
Thus neither low present load nor a successful isolated flow establishes that
the original calibration is valid. Artifacts are
`calibration-diagnostic-20261002-203106` and
`calibration-repeats-20261002-203541` in the evidence directory. The cause of
the procedure-dependent TCP result remains under investigation.

The model's passing outage cases on `f75668e` must also be retained as
findings: voice-only 1a passes for radio lane 0 and both gigaradio lanes;
voice-only 1c passes for radio/gigaradio lane 0 direction 1 and gigaradio lane
1 in both directions. The plan's section 2 does not establish a voice failure
in these cases from its liveness mechanism alone. Those observed passes do
not establish bulk progress or the other outage variants.


### Calibration and cold-run collection corrections

Observed reproduction: the original calibration repeatedly counted slow TCP
receipts in 128 KiB chunks; the receiver's one-second reports alternated zero
bytes and 128 KiB. At 0.4 Mbit/s a chunk spans multiple seconds, so that is not
a precise ten-second goodput sample. Matched 1 KiB TCP reads brought the radio
satellite uplink above its 85% gate. A longer convergence interval brought the
mobile downlink from approximately 13–15 Mbit/s to approximately 95 Mbit/s.
Longer measured intervals were still needed for the satellite downlink. The
successful combined diagnostic (`20261002-211224-calibration`) measured
0.356/1.170 Mbit/s TCP uplink and 0.455/95.587 Mbit/s TCP downlink; all TCP and
UDP capacity gates passed. This is one run, not a repeated reference series.

`calibrate.py` now uses 1 KiB TCP reads, 30 seconds of TCP convergence (UDP
retains three seconds), and 30 measured seconds per flow. The fixed-size UDP
capacity gate remains 85% on each WAN and direction. Both calibration and
adaptive collection verify that the
owned iperf process is listening before starting clients, rather than treating
daemon startup as readiness; server output is retained in a guest log. A
captured calibration attempt had failed with `Connection refused`. Adaptive
collection retains TCP error output and the server log on a failed client.
These are harness changes, not changes to wanbond's policy or its adaptation
deadlines.

Observed on the lab WAN: eight idle wanbond UDP packets were captured within
approximately 140 ms while no test flow was active. The previous calibration
did not stop an existing daemon, despite intending to measure without it.
Calibration now stops only the processes recorded in the lab's wanbond PID
files, verifies their executable before signaling them, and verifies exit.
Calibration must therefore precede provisioning a baseline/candidate scenario.
This removes policy-dependent background traffic from the reference.

Observed cold-clock reproduction after a complete TCP run: the old driver
dispatched traffic 39.739/41.570 seconds after edge/hub startup. The corrected
driver records both process start times, anchors dispatch to 30 seconds after
the later startup, and extends telemetry to cover the intervening idle time.
The verifying run dispatched at 30.000/31.872 seconds after edge/hub startup.
TCP's own timestamp and clock-offset uncertainty remain part of the raw
record; dispatch is not an assertion that TCP negotiation takes zero time.
The cold throughput itself is baseline evidence, not a candidate pass.

The scheduler observer additionally records the kernel's
`sched_schedstats` enablement when available (`null` if unavailable). Disabled
scheduler collection must not be interpreted as zero waiting; observer wake
delay and CPU counters remain separate observations. Neither these harness
corrections nor the later successful measurements establish the cause of the
original unrecorded host-contention failure.

The isolated six-run series (`calibration-corrected-repeats-20261002-214636`
under `/srv/nvme/tmp/wanbond-adaptive-evidence`) passed every UDP capacity
check: three radio and three gigaradio runs. Radio TCP failed once at
0.336 Mbit/s against a 0.340 Mbit/s threshold, then passed twice. All three
gigaradio TCP runs failed on the WAN configured with 0.4% loss, delivering
6.61–8.39 Mbit/s while its UDP delivered 287.11–290.25 Mbit/s. TCP on the
loss-free WAN delivered 285.34–286.96 Mbit/s. These are observations, not
adaptive-policy scenario passes.

Controlled reproduction (`calibration-loss-repro-20261002-221212`): removing
loss on the affected gigaradio WAN, keeping the rate and delay settings,
raised uplink TCP from 7.556 to 286.840 Mbit/s; retransmissions fell from 156
to zero. The inference is that the TCP capacity assertion rejected the
intended congestion response to loss rather than establishing insufficient
emulator capacity. `calibrate.py` therefore retains TCP diagnostics and gates
capacity on fixed-size UDP. This changes a defective calibration criterion,
not the section 4 throughput percentages or adaptation deadlines.

In the six-run trace, maximum observer wake delays were 10.80/20.07/10.32 ms
on host/edge/hub. Host aggregate CPU peaked at 99.17%; guest steal peaked at
32.69%/26.92%. Scheduler statistics were disabled. These observations retain
evidence of contention even though the UDP capacity checks passed; they do
not establish that contention caused each voice or TCP failure.

The 2d deterministic model now matches `cellular.json`'s mobile delay of
25±5 ms as well as its ±60% grants. On `f75668e`, the corrected radio test
fails p99 at 151 ms against 150 ms, with no voice loss; its bulk checks pass.
The gigaradio test passes voice and fails both bulk directions. Raw output
is `f75668e-model-cellular-v3.txt` in the evidence directory. The earlier
matrix used the radio mobile delay for this row and is superseded for 2d.

Post-correction gigaradio calibration (`20261002-221603-calibration`) exited
successfully: UDP delivered 290.29–290.33 Mbit/s on each 300 Mbit/s direction,
while the lossy TCP diagnostic still delivered approximately 6.7 Mbit/s.
This verifies the corrected separation of capacity and TCP response.

Stage 0 also exports `wanbond_adaptive_realtime_original_packets_total` per
lane. It counts first real-time submissions, excluding copies,
repairs and small TCP datagrams. A deterministic transport-to-metrics
reproduction failed before the counter existed and passed after it, with
both repair and small-TCP transmission exercised. At 10 Hz its increments
provide aggregate primary-route observations; sub-100 ms changes remain
unresolved by that sampling. A low RTT alone does not establish that the
primary moved, as the passing voice-only 3a model demonstrates.

Observed on 2026-10-03: the corresponding route-move reproduction failed
because no counter existed, then passed after
`wanbond_adaptive_realtime_original_path_moves_total` was added. Copies,
repairs and small TCP packets do not increment it. A sampled increase of two
or more reveals otherwise hidden moves; their exact spacing still needs
sufficient time resolution. Both counters observe the transport's selected
route, not successful socket writes or delivery. Transport and metrics tests
passed (`realtime-moves-green.txt`).

`wanbond_adaptive_bulk_original_packets_total` also counts first bulk
submissions per lane. Its transport-to-metrics reproduction failed with the
counter absent, then passed with one original despite multiple bulk
transmissions. Small TCP packets and repairs are exercised and excluded;
the counter observes route selection, not delivery. The metrics suite passed
(`bulk-originals-full-green.txt`). This is stage 0 instrumentation for 1b;
the original `f75668e` binary does not export it.

The original `f75668e` binary completed all 84 scenario collections: fourteen
scenarios, three runs on each family. The manifest is
`f75668e-lab-matrix-20261002-223350/manifest.json` in the evidence directory;
all collector exits were zero. Section 4 verdicts still require matched
references and gate evaluation. Collection success alone is not a policy
pass. The original binary has no primary-route counters, so latency cannot
prove its primary moved.

The initial receiver evaluator's deadline calculations are superseded.
**Inferred from inspected upstream source:** iperf 3.20 creates its JSON
timestamp in `iperf_on_connect`, before initializing the stream measurement
clocks. See [the pinned source](https://github.com/esnet/iperf/blob/3.20/src/iperf_api.c).
Millisecond precision does not make that timestamp a test-start timestamp.
A reproduction supplied a connection timestamp one second before test start;
the evaluator assigned the receiver interval to the wrong second
(`iperf-time-origin-red.txt`). It now requires a separately captured start.
The original 84 collections lack that observation, so their absolute TCP
deadlines and phase-specific TCP gates are inconclusive. Their raw receiver
intervals, voice reports and scheduler traces remain evidence.

`tcp_capture.py` records the guest time when iperf emits its test-start event
and reconstructs the same receiver JSON from streamed events. The collector
retains both endpoints' reports and event times. This timestamps receipt of
the event, not the kernel's first data packet; guest recorder scheduling
remains an uncertainty. Voice reports now also retain actual send times,
including packets without an echo. The shared parser contract passed with
an in-memory event source and a real subprocess pipe. An actual iperf 3.21
loopback test also passed: both endpoints retained two receiver intervals;
their captured starts were approximately 1.8 ms after the connection
timestamp (`tcp-capture-loopback.txt`). Guest iperf 3.20 validation is pending.

`adapt.py check DIRECTORY --references REFERENCES.json` writes `gates.json`:
exit 0 means pass, 1 means at least one failed check, and 2 means inconclusive.
`run --references REFERENCES.json` evaluates after collection. The reference
file has a `phases` object keyed by phase number, each with `payload_bps`
(downlink, uplink), `idle_p99_ms` (edge echo, hub echo), and, for 3a,
`idle_p50_ms`. Record the independent calibration evidence alongside it.
TCP measurements use complete receiver intervals; no within-interval arrival
times are invented. Qualitative unchanged/burst definitions and observations
of restored-lane bulk and physical post-change loss remain unfinished, and
are reported as inconclusive. A collector exit of zero without references
continues to mean collection only.

The model's under-150-ms gate also accepted exactly 150 ms. Its deterministic
boundary reproduction failed for that reason, then passed after the strict
comparison was corrected (`model-latency-gate-{red,green}.txt`). This corrects
the acceptance test; it does not change transport behavior.

Matched UDP calibration of the 26 distinct healthy profile states is in
progress. The first sweep stopped on an iperf error during its second
profile and lacked guest timing records because their required PID argument
was omitted. That incomplete sweep is not used as a reference. The resumed
sweep records host/guest timing and retains client errors; its completed
states are indexed under `matched-phase-calibration-20261003-005314`.

Observed guest iperf 3.20 validation subsequently completed on the original
`f75668e` binary (`20261003-011808-adapt-cold-f75668e-tcp-start-smoke`). Its
captured measurement starts were 167.8 ms (edge) and 130.8 ms (hub) after
the connection timestamps. The evaluator now bounds a final second using
all intersecting receiver intervals and retains connection-to-capture and
clock-exchange uncertainty. A partial-interval reproduction failed because
it produced a false failure; after correction it reports inconclusive
(`partial-deadline-{red,green}.txt`). Eighteen Python checks passed.
Event-application timing and all qualitative gates still require completion.

The event collector previously collapsed two changes in the same guest into
one timestamp. Its four-change reproduction retained only two records
(`applied-times-red.txt`). It now retains each guest/lane's host submission
and guest completion separately; all nineteen Python checks pass
(`stage0-python-current.txt`). These bound application time rather than
asserting that completion was its exact instant. Integration of those bounds
into the gate evaluator remains pending.

The resumed calibration sweep completed fifteen profile states, each passing
its independent UDP capacity gate, then stopped during stream setup on the
gigaradio standby profile. The retained client error was `unable to read from
stream socket: No such file or directory`, with no received intervals. This
is an incomplete measurement, not a failed tunnel throughput verdict. A
controlled port-reuse experiment is investigating residual traffic; its cause
has not been established. The full phase reference set remains unfinished.

The attempted 2026-10-03 voice-only field measurement of added delay on both
edge WAN VLANs did not start: the first SSH connection reported `No route to
host`. No remote command, qdisc change, helper or candidate ran. The
concentrator was reachable and its ping to `192.168.222.15` received no reply.
No voice/throughput traffic was generated by this attempt; current mobile
interface counters could not be read. Exact commands and errors are retained
in `field-both-delay.txt` and `field-access-20261003-0116.txt`.

**Observed calibration protocol defect:** the same UDP setup error occurred
on a fresh unique port in the controlled port experiment, so port reuse is
not required. A subsequent iperf 3.20 debug reproduction on the unchanged
gigaradio standby profile consumed 651 data datagrams while waiting for its
acceptance reply, hit its startup byte limit and aborted. A successful trial
consumed 598 data datagrams before receiving the reply. The inspected
[UDP startup source](https://github.com/esnet/iperf/blob/3.20/src/iperf_udp.c)
matches that failure path. Evidence is under
`udp-handshake-repro-20261003-012822`.

UDP downlink calibration now starts a hub client against the edge's per-WAN
server, using the same directional qdiscs and offered rate. The server helper
takes an explicit guest; TCP diagnostics retain their previous direction.
Three full 30-second calibrated pairs passed on the formerly failing standby
profile, with reused ports: WAN1 measured approximately 0.4845 Mbit/s against
0.5, and WAN2 290.21–290.36 against 300, in both directions. Each exceeds the
unchanged 85% capacity gate. Results are under
`calibration-direct-udp-20261003-013025`; the Python checks passed too.
The remaining matched reference states still need collection.

The outage model gates now use measured baseline idle p99, rather than the
previous fixed 110/182 ms limits. These are deterministic measurements on
`f75668e` with the same two echo streams, one lane enabled or both, measured
in `[5,19)` seconds (`f75668e-model-idle-reference.txt`):

| Family | Enabled lanes | Direction 0 idle p99 | Direction 1 idle p99 |
|---|---|---:|---:|
| radio | lane 0 | 191 ms | 77 ms |
| radio | lane 1 | 154 ms | 154 ms |
| radio | both | 76 ms | 90 ms |
| gigaradio | lane 0 | 60 ms | 60 ms |
| gigaradio | lane 1 | 134 ms | 135 ms |
| gigaradio | both | 60 ms | 60 ms |

Outage thresholds add the specified 50 ms; recovery uses the restored pair's
idle reference. Baseline and candidate use these same fixed measurements.
The under-1% loss check remains run-wide, rather than being imposed again on
each outage phase. These references describe this model, not stationary
5G/Starlink field conditions.

Corrected baseline output (`f75668e-model-outages-v3.txt`) records another
pass: radio lane 1 voice-only 1a. All four voice-only 1a variants now pass;
all four bulk variants fail. All four 1b cases fail. The four voice-only 1c
passes previously recorded remain; its other twelve variants fail. This
supersedes the earlier outage matrix and corrects section 2's claimed voice
consequence, without inventing a failure to fit the plan.

The one-way bulk reference was subsequently corrected to the survivor
criterion of 1a, which 1c explicitly inherits. It previously counted the
failed link's usable reverse direction and changed both flow allocations.
A reference invariant reproduced that mismatch before correction. Re-running
1c on `f75668e` (`f75668e-model-oneway-v4.txt`) retains the same four passes
and twelve failures, now against the specified survivor budget.

`adaptive_reference.py` supplies the independent lab wire budget. Observed
inputs (`lab-tcp-budget-inputs.txt` and raw iperf JSON) are TUN MTU 1339,
TCP MSS 1287, enabled TCP timestamps and iperf 3.20. From those inputs and the
framing/padding code, a full TCP segment occupies 1514 link bytes, a pure ACK
239, a voice datagram 367, feedback 207 and a keepalive 143. These costs are
inferences; the reference assumes full-MSS TCP segments and one reverse ACK
per segment, plus feedback every 25 ms and every 64 receipts. It reserves
the required 100 voice datagrams per direction per second and 200 ms
keepalives. Copies and repairs do not lower the reference.

Successful 1300-byte UDP calibration receipts are converted to link service
with their 42-byte IP/UDP/Ethernet overhead, capped at the configured rate.
The helper rejects a failed 85% UDP calibration or insufficient service for
required traffic. Five tests prove the directional conservation constraints,
the voice cost, survivor equivalence and calibration rejection. This is a
budget helper; matched phase calibration and the complete section 4 evaluator
are still pending. Cold and existing benchmark gates retain their specified
wire-rate percentages; the voice amendment does not change those gates.

The complete baseline collector series is running with the original
`f75668e` binary, not the diagnostic build. Its manifest and per-run logs are
under `f75668e-lab-matrix-20261002-223350` in the evidence directory. The first
six voice-only blackout collections completed: five lost no echoes, radio
run 3 lost three/two, and whole-run receive gaps stayed below 97 ms. Maximum
guest observer wake delay was 25.68 ms. Those are observations of delivery
and scheduling, not complete latency gate verdicts; independent idle
references are still required.
