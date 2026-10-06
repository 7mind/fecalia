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
| `profiles/field-standby.json` | Starlink 0.5 Mbit/s symmetric, 5G 100 down/10 up; fixed 14/28 ms one-way delays, satellite policing and 300 ms mobile buffer | Additional field-cap fixture for adaptive comparisons; timing, loss and queue behavior are assumptions, not an RF trace |
| `profiles/gigaradio.json` | 300+300 Mbit/s in each direction with the radio profile's delay, jitter and loss; delay correlation 95% | Same throughput gates; a model of an upgraded Starlink beside 5G |
| `profiles/gigasteady.json` with `wander.py` | 300+300 Mbit/s with steady netem delays that `wander.py`, run in each guest, moves as measured links' latency moves | Same throughput gates; the only scenario in which latency has memory |
| `single_lane.py` | One WAN dead from the start; on a cold tunnel a call in both directions, then one TCP flow three seconds later | TCP reaches 50% of the live WAN's rate; each voice stream has <1% loss |
| `flood.py` | A call in both directions while one side offers 400 Mbit/s of UDP to `profiles/fast.json` | Each voice stream has <1% loss |
| `continuity.py` | Simultaneous TCP in both directions plus two 50 Hz, 160-byte UDP echo streams | TCP completes 65 seconds and meets the progress rule below; each UDP stream has <1% loss and meets the gap and latency rules below (`continuity_gates.py`) |

**Current field caps, operator evidence 2026-10-04:** Starlink standby is
0.5 Mbit/s symmetric; 5G is at most 100 Mbit/s down and 10 Mbit/s up.
These are ceilings, not measured goodput. The retained radio family uses
0.5/0.4 Mbit/s Starlink and 100/1.25 Mbit/s mobile; `adapt.py`'s built-in
`FIELD` fixture uses 0.5/0.5 and 50/10. Neither reproduces those current
caps. Gigaradio's 300+300 Mbit/s conditions are a separate stress requirement.
Field comparisons require current direct uplink measurements before the
tunnel and independent references after voice and protocol overhead. Prior
fixture results retain their original conditions and are not relabelled as
measurements of this field configuration.

The separate `field-standby.json` fixture matches the operator's rate caps.
Its fixed delays, zero random loss, satellite policing and mobile buffer
are inherited modeling assumptions from the earlier built-in field fixture.
It does not replace the radio/gigaradio acceptance families. The deterministic
model reads this same file; its policer admits two full datagrams, whereas
the lab uses a 3 kB burst. Matching rate caps alone does not establish
equivalence to the field.

Observed on the production controller at `f75668e` with corrected TCP-model
inputs: the field fixture's idle voice p99 is 41/41 ms on satellite,
59/59 ms on mobile, and 41/41 ms on the pair in `[5,19)` seconds. All 700
echoes per direction arrive in each topology, identically across three runs
(`field-standby-f75668e-idle-three.txt`). These independent references supply
the unchanged idle + 50 ms outage/recovery latency limits.

Run the additional model cases explicitly:

```sh
nix develop --command go test -tags adaptivepolicy ./internal/bond \
  -run '^TestAdaptiveFieldStandby' -count=3 -v
```

Observed with the earlier receipt-timing-corrected model input: baseline
and `c8` both pass all six voice-only outage variants and fail all six
combined bulk/outage/recovery cases, identically across three
runs. The latter test combines the existing 1a/1c and 1b gates; a combined
failure need not mean its outage phase failed. On the bidirectional mobile
blackout, baseline TCP delivery in `[23,35)` is 1,200/3,500 B/s and `c8`
delivers 8,700/8,100 B/s against an independent 12,110 B/s reference. The
75% bound is 9,082.5 B/s, so this is a measured model increase, not a gate pass
or a field gain. Evidence: `field-standby-{f75668e,c8}-outage-three.jsonl`.

A later failing reproduction corrects sequence distance being mistaken for
three selectively received segments. With that correction, the same
voice-only cases still pass and all six combined bulk cases still fail
identically across three runs. Mobile-blackout TCP delivery in `[23,35)`
is now 700/1,800 B/s on baseline and 1,600/3,600 B/s on `c8`, against the
same 12,110 B/s independent reference. These supersede the older-input
numbers for current model verdicts (`field-standby-{f75668e,c8}-sack-loss-three.jsonl`).

Observed: the new profile passes its independent lab UDP capacity gate in
both directions: satellite 0.4843/0.4845 Mbit/s and mobile 9.6777 up/96.8394
down, all above 85% of configured wire rate. Plain TCP is 0.3816/0.3823 on
satellite and 9.5566 up/95.3654 down; it is reported separately. The host
trace records a 99% busy 100 ms sample and maximum wake delay 2.6 ms;
guest maxima are 5.8/10.3 ms with observed steal. This is one calibration,
not the required scenario or regression three-run series.

One `c8` lab blackout diagnostic completed on this additional profile
(`20261004-150613-adapt-blackout-field-caps-c8-static-diagnostic`): voice
loss/gap checks pass, but receiver delivery after mobile recovery is only
68,587 B/s down and 989,290 B/s up after the five-second deadline. The
run lacks independent phase latency/goodput references for a full gate
verdict. Its startup state differs from the warm field voice series. An
earlier dynamically linked diagnostic binary could not start in Alpine;
it contributes no policy evidence. Static build SHA is
`928afcde34837cecbcd31fb20d9928081ccf1c8f3626f6b12feeff15c43445ec`.
See the [trial record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md)
for the matching field rounds, input-model corrections and limitations.

Observed after the office was parked: three matched field series, each
deployed → `c8` → deployed, complete with 24 kB/s TCP offered each direction
and a 15-second 5G-egress blackout. Candidate uplink outage delivery is
15.3–15.9 kB/s versus deployed rounds' 1.4–9.3. All loss/gap checks pass,
but candidate voice p99 reaches 131–144 ms in two rounds and downlink results
are mixed. These bounded field observations do not supply the unmeasured
independent survivor idle/residual-goodput references or replace the required
radio/gigaradio lab gates. Restoration and cleanup were verified; the full
test interval's mobile VLAN increase is 100.24 MB including background.
The [parked field record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md#parked-field-resumption--2026-10-04)
contains the three repeats, exact sources, receiver data and outstanding gates.

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

The latest model matrix on `f75668e` has the following case counts. It
includes first-second deadline measurements and primary voice-route checks;
these are progression results, not candidate passes:

| Row | Passing / failing cases |
| --- | --- |
| 1a | 4 / 4; all voice-only cases pass |
| 1b | 0 / 4 |
| 1c | 4 / 12 |
| 2a | 0 / 4 |
| 2b | 0 / 2 |
| 2c | 0 / 2 |
| 2d | 1 / 1; radio passes |
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
asserting that completion was its exact instant.

A second clock-bound reproduction found a false pass: an interval stamped
inside the deadline could belong wholly before the impairment under its
recorded start uncertainty (`uncertain-origin-red.txt`). The evaluator now
uses only provably contained intervals for passes and includes every possible
overlap in failure bounds. That case is inconclusive after correction; all
twenty Python checks pass (`uncertain-origin-green.txt`).

The event bounds are now integrated: passes use earliest submission plus the
deadline, failure bounds extend through latest guest completion plus clock
uncertainty, and TCP phase samples must be certainly contained. A reproduction
showed that delayed host collection could otherwise postpone the deadline by
twenty seconds. Another showed that excluding an uncertain boundary sample
could falsely pass a latency gate (`event-bounds-red.txt`). Voice quantiles
now bound certain/possible membership; unresolved results are inconclusive.
Older collections lacking submission bounds remain inconclusive for those
phase checks.

A separate 3a reproduction recovered 2.5 seconds after the impairment but
passed a median over the rest of the phase (`latency-deadline-red.txt`). The
collector now checks the final second before the two-second deadline as well
as the later phase. All twenty-three Python checks pass
(`latency-deadline-green.txt`). Other gate observations, qualitative
definitions and the model deadline audit remain unfinished.

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
The remaining eleven states subsequently passed. The complete 26-state set
is indexed under `matched-phase-calibration-20261003-005314` (fifteen states)
and `matched-phase-calibration-direct-20261003-014040` (eleven). Each state
has raw UDP receipts in both directions on both WANs and host/guest timing
records. These successful capacity references do not establish policy gates;
phase reference assembly and independent idle-latency measurements remain.

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
budget helper; matched phase calibration is complete while the complete section 4 evaluator
remains unfinished. Cold and existing benchmark gates retain their specified
wire-rate percentages; the voice amendment does not change those gates.

The original baseline collector series completed with the original
`f75668e` binary, not the diagnostic build. Its manifest and per-run logs are
under `f75668e-lab-matrix-20261002-223350` in the evidence directory. The first
six voice-only blackout collections completed: five lost no echoes, radio
run 3 lost three/two, and whole-run receive gaps stayed below 97 ms. Maximum
guest observer wake delay was 25.68 ms. Those are observations of delivery
and scheduling, not complete latency gate verdicts; independent idle
references are still required.

Observed model-fidelity correction: the new adaptive model marked every TCP
ACK eligible for coalescing, including its SACK reports. The production
classifier already excludes SACK. A three-ACK reproduction retained only
two reports (`model-sack-red-three.txt`); after correcting the model metadata
it retained all three (`model-sack-green.txt`). A preliminary two-ACK fixture
passed because coalescing requires three advancing ACKs and did not exercise
that precondition. This changes the simulated TCP input, not wanbond policy.
The corrected `f75668e` matrix completed three repetitions of all 52 cases.
Measurements and verdicts were identical, including all failure messages
(`f75668e-model-sack-matrix-v5.jsonl` and its summary JSON). Radio 2d now
passes: p99 is 136/141 ms; bulk delivers 3,717,120/233,220 B/s against
references of 2,030,443/288,205 B/s. Gigaradio 2d still fails. The previous
151 ms radio verdict is superseded; this passing finding is retained and
the plan's section 2 corrected. Qualitative gate definitions and the
remaining gate-fidelity audit are still unfinished.
The 2d voice window is `[10,40)` seconds and its mean bulk window `[20,40)`;
the finding is confined to those measurements.

Independent phase references have also been assembled for all 106 phases
of the 28 family/scenario combinations from the successful calibrations.
`phase-goodput-references-v1` retains each calibration path, directional wire
service and protocol budget; no candidate estimate or delivery is an input.
A one-way-dark WAN is excluded entirely, as required by 1c. Independent
idle-latency collection subsequently completed all eighteen runs in
`f75668e-idle-references-20261003-022301`: each WAN and the pair, three runs
on both families, with voice only on the original binary. Quantiles use
actual send times in `[10,29)` seconds. Fixed lab references use the maximum
of the three p99 measurements and median of the three p50 measurements;
every raw run remains available. These are lab references, not stationary
5G/Starlink values.

| Family | Enabled WANs | Idle p99 edge / hub (ms) | Idle p50 edge / hub (ms) |
|---|---|---:|---:|
| radio | WAN1 | 59.114 / 58.833 | 40.695 / 40.428 |
| radio | WAN2 | 133.065 / 133.686 | 81.617 / 80.698 |
| radio | pair | 72.179 / 74.122 | 39.041 / 39.048 |
| gigaradio | WAN1 | 59.187 / 145.580 | 40.967 / 40.435 |
| gigaradio | WAN2 | 134.708 / 134.655 | 81.988 / 80.241 |
| gigaradio | pair | 78.599 / 73.422 | 38.621 / 38.701 |

Observed maximum recorder wake delays across these runs were 7.519 ms on
the host, 15.196 ms on edge and 17.911 ms on hub. The WAN1 gigaradio outlier
round recorded maxima of 0.925/0.500/0.204 ms; these recorder observations do
not establish the cause of its 145.580 ms p99. Scheduler observations
are retained in `scheduler-summary.json` beside the reference index.

The assembled phase files now include the idle references and their raw
evidence paths. No new TCP scenario series has been claimed as acceptance:
the original 84 runs lack the new timing evidence, and several gate
observations and definitions remain unfinished. Edge SSH still reported
`No route to host` on the subsequent read-only recheck
(`field-access-recheck.txt`); no field candidate or impairment ran.

The assembled references are preserved with SHA-256 digests in
`phase-references-with-idle-20261003-030336` (28 family/scenario files and an
index). Baseline and candidate evaluations must use that same reference set.

Further read-only contact checks found the concentrator reachable, its tunnel
interface present, and its WAN probe status down (`field-hub-contact-state.txt`).
One ping to the edge's tunnel address `10.77.0.2` received no reply; SSH via
the concentrator to that address timed out during banner exchange
(`field-hub-tunnel-contact.txt`, `field-tunnel-ssh-contact.txt`). These
observations establish that neither tested SSH route worked; they do not
establish whether the edge is powered off. No candidate or qdisc change ran.
At that point, the physical WAN carrying management `192.168.222.15` remained
unestablished; the Linux reply-route observation alone was insufficient for
another blackout. The operator subsequently confirmed ZeroTier safety for
either single wanbond VLAN, as recorded below.

The current non-privileged AGENTS gate and `nix build` passed after the ACK
model, diagnostic-counter and evaluator changes (`stage0-current-nonprivileged-v3.txt`,
`stage0-current-nix-build-v3.txt`). The policy's `control.go`, `schedule.go`
and `queue.go` remain byte-identical to `f75668e` (observed git comparison).
Stages 1–3 and their validation remain unstarted.

### Field contact restored; delay measurement and model deadline

The operator identified a discharged battery and reported it fixed. Fresh
SSH checks observed both hosts reachable, both deployed executables at
`/nix/store/zkkqlgm2jpg2s0klyiq5z6ixkcyfwxg5-wanbond-0.0.0/bin/wanbond`
with SHA-256 `f0cb62b2e221b413436c76a428e58dc177110deace3d70d87ee7db08eeec1375`,
and no runtime drop-ins. Both edge WAN metrics reported up. The physical
management WAN was unknown at that point; no blackout was performed.

Observed deployed-baseline voice-only measurements, 60 seconds each:

| Conditions | Lost echoes, edge / hub, of 3000 each | Maximum receive gap, edge / hub | Mobile interface counter delta |
| --- | --- | --- | --- |
| Native links | 0 / 0 | 60 / 80 ms | 3.915 MB |
| Both WAN VLANs gain 100 ms egress delay; timer removes during collection | 5 / 4 | 95 / 120 ms | 4.022 MB |
| Same added delay held until collection finishes | 66 / 68 | 124 / 76 ms | 4.315 MB |

Native p99 RTT was 67/76 ms. The first delay run lost no echoes during
the rise; its nine losses coincide with qdisc removal. Observed timer
`AccuracyUSec=1min` and journal times establish that the nominal 15-second
removal ran about 40 seconds after application. That run does not isolate
controller loss from removal of queued packets. The repeat arms 75-second
removal timers with 100 ms accuracy before either change, and removes the
qdiscs after voice collection. Its losses occur in the post-rise window
`[17,28)` seconds while both netem qdiscs are still present; neither qdisc
reports drops before cleanup. That is an observed failure of the 3c zero-loss
gate on the deployed build. It does not establish the loss's internal cause.
Both final qdisc checks report `noqueue`. The 12.253 MB sum includes
background traffic in these windows; helper transfers and preflights outside
them are excluded, so this is not a provider billing total.

Evidence is under `/srv/nvme/tmp/wanbond-adaptive-evidence/`:
`field-voice-restored-native-20261003-165940`,
`field-voice-restored-both-delay-20261003-170151`,
`field-voice-restored-both-delay-held-20261003-170835`,
`field_resumed_voice.py`, and `field-resumed-delay-timer-precision.txt`.
Field and lab interventions differ: the field adds 100 ms only on edge
egress; the standard lab adds 50 ms in both directions. Equal added RTT does
not establish equal queueing, lane state or physical-link conditions.

The model's 3a deadline reproduction accepted recovery at 2.5 seconds before
correction (`model-shift-deadline-red.txt`); it now rejects that fixture.
Independent voice-only better-lane median references on `f75668e`, measured
in `[5,19)` seconds, are 90/91 ms on radio and 83/82 ms on gigaradio
(`f75668e-model-idle-median.txt`). The model checks `[21,22)` seconds for a
change at 20 seconds, then checks the later phase separately. All four
baseline 3a variants pass latency in three repetitions; both bulk variants
still fail their delivery-cut gate (`f75668e-model-3a-deadline-v6.txt`). This
supersedes the arbitrary 80 ms reference and the old `[22,24)` deadline
claim. Baseline caller-latency passes are retained as findings, not converted
into failures.

Observed subsequent diagnostic candidate `adapt-s0-d2d831f` had executable
SHA-256 `7ebe1d705a9ed354a5c5f6cda65f747b974447b913d4a7790ed3b1b94cc8d1fb`.
Its policy source remained unchanged. The fresh held-delay round lost 66/80
echoes, with maximum gaps 218/217 ms; the warm round lost 63/73, with gaps
166/150 ms. These are not clean 3c comparisons: retained edge samples show
Starlink down throughout fresh voice collection, and down in warm windows
`[9.257,13.360)` and `[16.562,64.696)` relative to voice start. Logs record
liveness transitions but do not establish their cause. Both deployed
executables were restored, runtime drop-ins removed, and both final VLAN
qdiscs were `noqueue`. No permanent deployment occurred.

Before the deployed held-delay rise, both WANs remained up throughout the
retained samples. Its edge targets fell to 16 kB/s on both lanes and its
interactive queue-drop counter increased by 134, equal to the 66+68 lost
echoes. That supports the delay-model diagnosis by inference; aggregate
counters do not identify individual datagrams. The later diagnostic rounds
retain their differing lane states rather than treating their larger gaps
as an implementation regression.

Across the interval from the native run's initial counters through final
candidate restoration, the mobile VLAN's RX+TX counters advanced by
39.538 MB, including background traffic and gaps between measurements.
The gzip artifact sent to the edge was 4.693 MB on the management path,
whose physical WAN is still unknown; it cannot be assumed included in that
VLAN total. Evidence includes `field_diagnostic_voice.py`,
`field-voice-s0-d2d831f-held-{fresh,warm}-20261003-*`,
`field-diagnostic-{edge,hub}-lane-check.txt`, and
`field-candidate-mobile-{before.json,after-and-restoration.txt}`.

The candidate runner's simulated timer-failure reproduction observed both
runtime override attempts despite failed timer commands. Its local
`/srv/nvme/tmp/wanbond-field/candidate.sh` now stops on an arm failure and
verifies timer activity before writing an override. The corrected fake-SSH
reproduction stops before any override (`candidate-arm-repro/{red,green}.txt`).
Real starts verified the timers and executable hashes; artifacts were gzip
compressed by the same runner.

The observed one-minute default timer accuracy also leaves the exact duration
of earlier nominal 15-second field impairments unproved. Their whole-run
echo receipts remain observations; nominal timer delays do not establish
phase boundaries or a matched 15-second lab replay.

Stage 0 now exports `received_bulk_packets_total` per destination lane. The
complete reproduction failed for the missing counter before implementation
(`received-bulk-red-complete-fixture.txt`). It passes with actual transport
receipts, repeated physical attempts, frame replays, small datagrams and
keepalives exercised (`received-bulk-green.txt`). This count establishes
physical bulk receipt, including a duplicate datagram in a new attempt;
it does not establish inner or TCP delivery. It changes no control rule or
wire field. The full non-privileged gate passed with this diagnostic
(`stage0-current-nonprivileged-v5.txt`); lab gate integration remains pending.

The returning-lane gate now checks physical bulk receipts within two seconds,
using the correct destination lane IDs (`0`/`256` on edge, `0`/`1` on hub).
The evaluator reproduction initially failed to recognize a timely receipt
(`return-lane-gate-red.txt`). Its corrected checks also reject a substitution
of submissions for receipts, preserve uncertainty for a read spanning the
deadline, and detect counter resets. All 24 Python checks pass
(`return-lane-gate-green.txt`). A real one-second sampler check recorded ten
bounded reads on each guest; maximum durations were 94/126 ms
(`metric-read-completion-lab.txt`). This implements the observation, not a
claim that scenario 1b passes. The original `f75668e` binary lacks the new
counter, so this component remains inconclusive on its metrics alone.

### Existing gates with contemporaneous scheduler observations

Observed baseline binary SHA-256 was
`59df1fbe9eed338b122ceab3cf4e512208631d7feaf849290b01135e78537987`
in both guests. The six unchanged existing gate scripts produced these
results; TCP measurements are the initial run and its observer rerun:

| Gate | Family | Result | Uplink / downlink Mbit/s |
| --- | --- | --- | --- |
| continuity | radio | pass | — |
| continuity | gigaradio | pass | — |
| benchmark | radio | pass twice | 1.308 / 83.362; 1.306 / 83.729 |
| benchmark | gigaradio | fail twice | 195.126 / 54.944; 45.558 / 33.764 |
| UDP | radio | pass | 1.352 / 80.568 |
| UDP | gigaradio | fail | 285.505 / 405.868 |

Gigaradio TCP requires 450 Mbit/s and UDP requires 480 Mbit/s in each
direction. The failures are those throughput assertions, not collection
errors. Baseline failures do not establish a candidate regression or prove
these gates impossible. No candidate comparison has run.

The initial benchmark observer hook did not wrap the separately loaded
module's own provision function. Its CPU traces are missing; the correction
was followed by both reruns above with complete observer logs. In the
gigaradio rerun, maximum recorder wake delays were 1.077/2.529/2.274 ms for
host/edge/hub. Host aggregate CPU peaked at 83.898%; guest aggregate CPU
peaked at 25.641/27.907%, and guest steal peaked at 13.953%. Scheduler
statistics were disabled on all three, so thread wait-time deltas are not
established. Aggregate CPU and recorder wake times do not exclude saturation
of an individual execution thread or establish the cause of low throughput.

Evidence: `f75668e-existing-gates-20261003-183828/{index,scheduler-summary}.json`,
`f75668e-benchmark-{radio,gigaradio}-with-observer-v2` and their logs, and
`f75668e-existing-benchmark-observer-v2-summary.json`, under the evidence
directory above. The lane controller remains unchanged. Stage 0 and stages
1–3 remain incomplete.

The older one-flow model in `tcp_model_test.go` also marked SACK reports
eligible for coalescing. Its public transport reproduction delivered reports
1 and 3, discarding report 2 (`tcp-model-sack-red.txt`). The model now excludes
reports carrying SACK ranges, matching the production classifier. All bond
tests pass with the corrected metadata (`tcp-model-sack-bond-tests.txt`,
79.267 seconds). This changes test inputs, not the deployed controller.

The plan-change collector now records qdisc and filter statistics at each
bounded sample. Observed in a real policer accounting check: root drops
increased by 888, including 887 policer drops and one child-netem drop
(`lab-police-root-drop-accounting.txt`). Summing root, child and policer drops
would count losses repeatedly. The loss evaluator instead balances offered
policer bytes against root-dequeued bytes and the backlog change. It observes
all WAN egress frames, including feedback and probes; it does not count TCP
delivery. The whole window from three seconds after the downshift through
phase end is bracketed conservatively. A pass requires its upper byte-loss
bound below 5%; exactly 5% fails, and unresolved boundaries or counter resets
remain inconclusive.

The missing-gate reproduction failed before implementation
(`plan-loss-gate-red.txt`); all 26 Python checks now pass, including byte-loss
boundaries, strict 5%, counter identity changes and draining an old backlog
(`plan-loss-gate-green.txt`). A real two-second collector check recorded 20
samples with both root and policer counters; its maximum read duration was
99 ms (`egress-sampler-lab.txt`). This validates collection, not a scenario
2c pass. Collections lacking these counters retain an inconclusive loss
component. Sampler failures now invalidate collection rather than silently
leaving missing egress evidence.

A Go AST audit observes 32 declared constant names in `control.go` on both
`f75668e` and the stage 0 branch, with 24 in the first block
(`count-control-constants.go`, `control-constant-baseline.jsonl`). The plan's
recorded count of 25 was incorrect. Use the full-file count of 32 as the
baseline for policy removal; moving declarations between blocks is not a
reduction. The `window-bound` and `cold-start` branches remain unmerged:
retained field evidence does not justify the former as an urgent stopgap,
and no field benefit of the latter has been measured. Their replacement
remains in stages 2 and 3.

### Corrected hello inputs and single-WAN field access basis

Observed: the model renewed both hello leases every tick even when the
incoming direction was dark. Its failing reproduction kept that lease up
without incoming evidence (`model-hello-dark-red.txt`). The correction stops
only that incoming renewal, preserving the live direction and other lane
(`model-hello-dark-green.txt`). It changes model inputs, not transport policy.

The remeasured 28 outage cases produced identical outcomes and measurements
in three repetitions on both the original `f75668e` production code and the
current stage 0 code (`{f75668e,current}-model-hello-matrix-v7.jsonl` and
`model-hello-matrix-v7-summary.json`). This supersedes their earlier matrix:

| Row | Pass / fail cases | Passing variants |
| --- | --- | --- |
| 1a | 5 / 3 | all four voice-only; radio lane 0 with bulk |
| 1b | 0 / 4 | none |
| 1c | 5 / 11 | voice-only: radio lane 0 direction 1, radio lane 1 either direction, gigaradio either lane direction 0 |

The previous inference of universal bulk failure for 1a is wrong. The radio
lane 0 bulk pass is retained; several earlier 1c verdicts also change with
the corrected hello evidence. Baseline production files are unchanged;
its test adapter only accommodates the original void-returning `Path` API.
The other 24 scenario cases retain their earlier three-run evidence.

Operator evidence: edge management uses ZeroTier and survives either single
wanbond VLAN blackout. The authorized test scope is one of `end0.231`
(Starlink) or `end0.232` (5G) at a time, with verified automatic removal.
Fresh observed prechecks found both deployed daemons without runtime
replacement drop-ins, both adaptive lanes up, noqueue on both VLANs, and the
edge's management replies routed via untagged `end0` and `192.168.222.1`.
That Linux route alone does not identify the upstream provider; management
safety for this scope comes from the operator's network explanation.

The operator also directed immediate direct-uplink measurements before the
tunnel, rather than treating an RF rate or latency as stationary. Observed
`/home/pavel/wbtest` runs Starlink speedtest, 5G speedtest, then tunnel
speedtest. Bounded field comparisons use that ordering and retain spread;
unbounded speedtests and its exit-peer selection are not needed for the
voice baseline. Fixed numerical definitions for the qualitative gates
remain unresolved where a measured comparison cannot establish them.

### Three single-VLAN field blackout repetitions

Observed on the deployed executables with SHA-256
`f0cb62b2e221b413436c76a428e58dc177110deace3d70d87ee7db08eeec1375`:
three interleaved voice-only egress blackouts of each VLAN, 60 seconds per
round, 3000 echo requests per source. Both adaptive lanes were live
immediately before each run. Each 100% loss qdisc had a verified 15-second
removal timer with 100 ms accuracy. Recorded application and removal
completion bounds give minimum outage durations of 15.026–15.141 seconds;
these are measured bounds, not exact 15.000-second impairments.

| Blacked-out egress | Run | Lost echoes edge / hub | Largest loss run edge / hub | Maximum receive gap edge / hub, ms | Direct ICMP p99 Starlink / 5G, ms | Mobile MB |
| --- | --- | --- | --- | --- | --- | --- |
| 5G | 1 | 53 / 81 | 11 / 13 | 156 / 280 | 82.6 / 49.9 | 3.283 |
| Starlink | 1 | 6 / 6 | 6 / 6 | 254 / 244 | 53.7 / 48.9 | 5.723 |
| 5G | 2 | 0 / 1 | 0 / 1 | 103 / 77 | 44.7 / 55.1 | 2.931 |
| Starlink | 2 | 4 / 4 | 2 / 2 | 97 / 138 | 38.0 / 197.0 | 4.644 |
| 5G | 3 | 4 / 12 | 1 / 2 | 98 / 93 | 46.1 / 41.9 | 2.731 |
| Starlink | 3 | 37 / 40 | 16 / 16 | 154 / 174 | 45.9 / 55.7 | 5.280 |

The first 5G round and first/third Starlink rounds fail the specified voice
continuity limits. The other rounds satisfy their loss, consecutive-loss
and gap components; this does not establish their full latency gate.
The 5G survivor briefly lost adaptive eligibility during Starlink run 1
(one of 151 samples on each peer). In every other round, every sampled
surviving adaptive lane remained eligible throughout the certain blackout
window. Adaptive eligibility is a controller observation, not an independent
proof of uninterrupted physical service or the cause of the losses.

Each direct reference used 100 source-bound ICMP requests to the concentrator
immediately before tunnel collection, after recording its source-policy
route. ICMP p99 describes a separate protocol and packet size; it does not
substitute for equivalent-size direct UDP voice calibration or prove the
survivor's voice latency gate. The spread is retained rather than treating
one reference as a constant property of either RF link. These are original
baseline rounds, not a candidate comparison or a matched lab replay.

All six removal services reported success and exit status zero. Both VLANs
returned to noqueue; a final read observed edge PID 2798, no runtime drop-ins
and no remaining test timers. The mobile counter advanced 24.592 MB across
the six individual measurement intervals and 27.470 MB from the first setup
through the final receipt, including intervening background traffic.
Evidence: `field-voice-baseline-{mobile,satellite}-r{1,2,3}-20261003-*`,
`field-single-wan-three-run-summary.json` and
`field-single-wan-final-restoration.txt` under the evidence directory.

Before removing stage 1 mechanisms, two existing tests were restated in a
separate code commit. `TestInteractiveFailoverDelivery` drives two public
transports over timed physical lanes, drops one outgoing direction, and
checks exactly-once unchanged payload delivery, its deadline, or bounded
expiry without an alternate. It replaces private feedback-timer and
below-minimum pacing-rate setup. Its slow alternate carries 8000 wire B/s;
that one-datagram fixture checks the 250 ms lifetime, not a claim that it
can carry both 50 Hz streams. The initial 150 ms expectation for that
fixture observed delivery at 150 ms and is retained in
`stage1-outcome-restatement-first.txt`; the continuous voice gates were
not changed. `TestLostIdleKeepalivePreservesRateAndBulkUsesTheHealthyLane`
checks healthy-lane bulk receipt and unchanged idle pacing rather than a
stall flag. Its receiver is polled through the delivery window instead of
assuming delivery from a single scheduling call. Both restatements pass on
the unchanged controller (`stage1-outcome-restatement-idle-and-delivery-v2.txt`).

The complete non-privileged gate and `nix build` passed after the hello-input
correction (`stage0-current-nonprivileged-v7.txt`,
`stage0-current-nix-build-v9.txt`). All 26 Python checks passed
(`stage0-python-tests-v8.txt`). Those runs preceded the test restatements;
their full verification is still pending. The refreshed 84-collection lab
series started with the exact original executable and the current timing
and egress collector (`f75668e-lab-matrix-20261003-202919/manifest.json`).
A collection's exit status is not its section 4 gate verdict.

The resequencer's cost regression now counts `Stats.HoldSlotVisits`, the
ring cells inspected while locating the oldest buffered observation.
Observed: 100,000 frames, every hundredth arriving early, require 99,999
visits with the existing indexed lookup; in-order arrivals require zero.
An isolated reproduction replacing only that lookup with `7b84ca5^`'s
full scan requires 3,244,032,000 visits and fails the 200,000-visit bound
(`reseq-slot-work-{green,red-scan}.txt`). All resequencer tests pass
(`reseq-slot-work-all-tests.txt`). This replaces the wall-clock ratio
assertion and adds a diagnostic count; it changes no resequencing decision.
The reproduction is separate from the untouched `f75668e` production code.

A cold-gate reproduction dispatched at zero but started the transfer at
two seconds. It reached the required goodput in its seventh transfer
second, yet the previous evaluator failed both directions by measuring
from dispatch (`cold-deadline-origin-red.txt`). The gate now brackets its
origin between connection setup and captured test start on both peers,
retaining clock uncertainty. Missing start evidence remains inconclusive;
a zero-delivery fixture still fails. All 27 Python checks pass
(`cold-deadline-origin-green.txt`). The seven-second and 60% requirements
are unchanged; this corrects observation timing, not transport behavior.

The liveness outcome restatements also pass against the original `f75668e`
production code (`stage1-outcome-restatement-original.txt`), with test-only
adapters for its void `Path` API. The full non-privileged gate and Nix build
passed after those restatements (`stage0-current-nonprivileged-v8.txt`,
`stage0-current-nix-build-v10.txt`); those checks preceded the subsequent
slot-count and cold-evaluator changes.

### Completed baseline collection and bounded field comparisons — 2026-10-03

Observed: the refreshed original-`f75668e` series completed all 84 collections
with exit status zero, three runs per family/scenario, interleaving radio and
gigaradio (`f75668e-lab-matrix-20261003-202919`). Its executable SHA-256 is
`59df1fbe9eed338b122ceab3cf4e512208631d7feaf849290b01135e78537987`.
`gate-summary.json` retains the individual checks against the same fixed
phase references. The evaluated results are:

| Scenario | Radio runs 1/2/3 | Gigaradio runs 1/2/3 |
|---|---|---|
| blackout, voice only | pass/pass/pass | pass/pass/pass |
| blackout, with TCP | fail/fail/fail | fail/fail/fail |
| one-way, voice only | fail/fail/fail | fail/fail/fail |
| one-way, with TCP | fail/fail/fail | fail/fail/fail |
| shallow rate fall | inconclusive/inconclusive/inconclusive | fail/fail/fail |
| deep rate fall | fail/inconclusive/fail | fail/fail/fail |
| rate rise | inconclusive/inconclusive/inconclusive | fail/fail/fail |
| plan change | fail/fail/fail | fail/inconclusive/fail |
| cellular grants | pass/pass/pass | fail/fail/fail |
| call-lane delay, voice only | inconclusive/inconclusive/inconclusive | inconclusive/inconclusive/inconclusive |
| call-lane delay, with TCP | fail/fail/fail | fail/fail/fail |
| both-lane delay, voice only | fail/fail/fail | fail/fail/fail |
| both-lane delay, with TCP | fail/fail/fail | fail/fail/fail |
| cold transfer | inconclusive/fail/inconclusive | fail/fail/fail |

These are evaluator results, not established controller causes. Observed in
this series: host aggregate CPU reached 100%; maximum observer wake delays
were 24.5 ms on the host, 136.8 ms on edge and 118.1 ms on hub; guest steal
reached 67.6% and 64.4% over sampled intervals. Per-thread scheduler accounting
was disabled. The complete `scheduler-summary.json` and raw observations are
retained. They establish contention, not attribution of an individual failed
gate. Original binaries also lack the new primary-route and physical-receipt
counters, and all 60 TCP collections used one-second receiver reports
(`receiver-resolution-audit.json`). Those missing observations remain
inconclusive. The three qualitative definitions remain unresolved; this table
cannot establish the full goal or completion of stages 1–3.

Two evaluator reproductions wrongly passed a three-second hole in receiver
coverage, and a missing outage second (`receiver-coverage-red.txt`). They now
remain inconclusive. Progress needs certain positive receiver bytes in every
required second, retaining phase and TCP-origin uncertainty. A four-second
aggregate with positive bytes cannot prove that; a covered zero-byte interval
still fails. Future TCP collection uses 100 ms reports on both peers. Lab SSH
sessions reuse a task-local control socket with a 60-second idle lifetime;
this reduces the clock and change bounds without assuming synchronized clocks.
Observed radio smoke: clock uncertainty fell from 66/70 ms to 13/16 ms,
both receiver intervals were 100 ms, one outage direction proved delivery in
all 14 required seconds and another proved a second with no delivery
(`stage0-receiver-resolution-mux-radio.txt`). The scenario still fails other
gates. All 30 Python checks pass (`stage0-python-tests-v10.txt`).

The operator's direct-before-tunnel method was exercised with bounded SSH
TCP payloads to the same OCI worker, in Starlink/5G/tunnel order. Source and
output-interface routing were observed separately. Local host policy routes
an unbound `10.77.0.2` source through `end0`; the benchmark therefore binds its
socket to `wanbond0`. The first attempt stopped on that route precondition,
restored the exit policy and sent no tunnel bulk. No WAN qdisc was changed.
Temporary restricted worker keys and the runtime `raspi5l` exit override had
removal timers; each cleanup removed its exact key and restored `auto`.

Three rounds then completed at a 1 Mbit/s offered ceiling, 750,000 bytes per
direction and path. Their upload receiver spans excluded payload buffered
during SSH startup: some implied more than the offered rate. That reproduced
measurement defect is retained (`field-reference-startup-red.txt`), and those
spans are not capacity evidence. A corrected round waits for receiver readiness
before pacing. Observed payload rates were Starlink 0.522/0.535, 5G
0.997/1.002 and tunnel 1.004/1.001 Mbit/s (upload/download). A 1 Mbit/s ceiling
establishes a bounded workload and a capacity lower bound, not full RF capacity
or an aggregation percentage. All per-transfer times and immediate idle
measurements remain in `field-direct-before-tunnel-20261003-223105`.
The three helper intervals used 19.63 MB on the mobile VLAN; the complete
first-to-last interval used 23.26 MB, including setup gaps and background
traffic (`field-direct-before-tunnel-summary.json`). The deployment was not
changed. Final observation: both WANs have `noqueue`, the exit policy is
`auto`, and no benchmark removal timer remains active.

Two additional public-transport reproductions prepare stage 1. With fresh
hello leases but no peer progress, the unchanged controller stays live at
two sparse ACK intervals and silently cuts 125,000 B/s to 87,500 B/s. Its
uncopied real-time datagram also receives no alternate delivery by that
cadence plus alternate transit (`stage1-liveness-outcomes-red-v2.txt`). The
receiver is polled after its delayed-ACK interval and a physical receipt is
required before asserting recovery. The same rate and copy outcomes also fail on the exact original production
source (`stage1-liveness-original-red-v2.txt`), with only test adapters for
its void `Path` API. The rate proof omits liveness diagnostics absent from
that original API; its 125,000-to-87,500 cut is still observed. An earlier
adapter compile failure is retained as no behavioral proof. These progression
tests remain tagged `adaptivepolicy`; they do not implement or prove stage 1.

The full non-privileged gate passed after the slot-count and cold-origin
changes (`stage0-current-nonprivileged-v9.txt`), and `nix build` subsequently
passed (`stage0-current-nix-build-v12.txt`). Those earlier runs preceded the receiver-coverage,
SSH-session and progression-test additions. The full non-privileged gate and
`nix build --cores 2 --max-jobs 1` now also pass after those additions
(`stage0-current-nonprivileged-v10.txt`, `stage0-current-nix-build-v13.txt`).
The instrumented executable is retained separately as
`stage0-policy-baseline-diagnostic-v13`, with source revision and hashes;
it is not relabelled as the untouched original executable.
The gigaradio smoke also used 100 ms receiver reports and 9.6 ms clock
uncertainty; all four outage progress checks proved delivery in all 14
required seconds. Its overall gate still fails
(`stage0-receiver-resolution-mux-gigaradio.txt`).

### Rejected stage 1 model attempt — 2026-10-03

Observed on the attempted ACK-progress liveness replacement: the two new
rate-preservation and suspect-copy reproductions pass. After correcting a
copy pacing-slot regression, the existing two-lane voice test delivers
500/500 at one-way p99 59 ms against its unchanged 70 ms gate. The complete
bond suite still fails the bursty-link bulk outcome: schedule 2 delivers
3,031,500 B/s, below 75% of 4,562,500 B/s mean service.

One full stage 1 model run gives 1a four passes/four failures, 1b four
failures, and 1c eight passes/eight failures. All twelve voice-only cases
pass; all sixteen bulk cases fail. These are not three-run results. The
radio lane 0 bulk case regresses relative to its corrected original baseline
pass. No gate or reference was changed to obtain these outcomes.

The radio survivor trace and scheduler code explain one restriction: while
voice activates the single-lane bulk cap, direction 0 allows only 5% of its
70,514.7 B/s target (3,525.7 wire B/s), below the required 6,385.5 TCP payload
B/s at the retained 8,514 B/s reference. Inferred: removing that restriction
is necessary at that held target. It is insufficient as a fix: the removal
still fails TCP progress and regresses single-lane voice to 494/500 delivered
and p99 116 ms against 99% / 75 ms. The earlier 2,181 B/s reference belongs
to direction 1 and must not be paired with direction 0's target.

These deterministic failures cannot be attributed to lab-host CPU spikes.
No attempt was installed in either VM or production, and no field data was
used. Under the operator's stop rule, the failed code was retained outside
the repository and the stage 0 policy restored. The
[checkpoint](../../docs/drafts/20261003-2245-adaptive-stage1-checkpoint.md)
lists exact source, artifacts, observations and limits. Stages 2–3 remain
unstarted; the fixed numeric gates and direct-before-tunnel field method
remain unchanged.

Observed after restoration: the full AGENTS.md non-privileged gate and
`nix build --cores 2 --max-jobs 1` pass
(`stage1-restored-nonprivileged-v1.txt`,
`stage1-restored-nix-build-v1.txt`). These results validate the restored
stage 0 source, not the rejected attempt.

### Field trial eligibility and policy research — 2026-10-04

Operator direction: an unsatisfactory lab result does not veto a bounded
temporary field trial. The field remains the behavioral reference. Record
the candidate's outstanding model and lab failures alongside its revision
and results; a field result does not establish the required three-run lab
gates. This changes trial eligibility, not the acceptance gates or stage
order. The previous stage 1 rejection is deterministic model evidence, not
a lab-host timing result, and its code remains unmerged and untested in the
field.

Use `candidate.sh` with verified automatic restoration, gzip binaries for
the edge, begin with voice, and cap subsequent bulk traffic. Preserve
Starlink/5G/tunnel direct-before-tunnel ordering and report actual mobile
MB. Each single-WAN impairment still needs its own verified removal timer
and the current management-path check. Permanent deployment remains outside
the goal.

The [research follow-up](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#8-research-follow-up--2026-10-04)
compares BBRv3 delivery sampling, completion-aware scheduling, QUIC recovery
principles and SCReAMv2 with the retained wanbond failures. Its implementation
experiments are proposals, not measured improvements. No policy code or field
state changed during this research.

### Sole-survivor pacing reproduction — 2026-10-04

Observed on the restored controller and on exact `f75668e` production source:
`TestVoiceAndBulkShareSingleSlowLane` delivers 500/500 voice datagrams at
one-way p99 55 ms, but only 2,400–3,600 bulk payload B/s in each measured
second. The independent conservative payload reference is 6,302 B/s after
two 50 Hz voice streams and forty full wire ACKs per second; its 75% gate
is 4,727 B/s. The test uses public transport deliveries and simulated time.
It remains a progression test under `-tags adaptivepolicy` until corrected.
The original source required no production adapter for this reproduction.

Evidence: `stage1-survivor-budget-{red,original-red}.txt` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. The current liveness reproductions
also still fail for the expected missing suspect-copy and silent rate-cut
reasons (`stage1-resume-liveness-red-v2.txt`). An earlier mistyped test filter
ran zero tests and establishes no behavior (`stage1-resume-liveness-red.txt`).

### Stage 1 shared-pacing trial draft — 2026-10-04

The ARM64 draft `c4-s1-share` is identified by executable SHA256
`45b28c75b6e66bc2a65c566e7e50c2bbb12d07d5da1ae9df52ff7dd41999c5b4`
and source patch SHA256
`5799be7e16dc6279d3101512a419d6980d34289b5062d0921cad2162012c880f`
on `04a745d`. It has not passed the acceptance gates. See the
[trial record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md).

Observed: one targeted model run restores sole-survivor bulk from
2,400–3,600 to 9,600–10,800 B/s, with all 500 voice datagrams delivered
and one-way p99 65 ms instead of 55 ms. The existing targeted cold,
jitter, isolation, takeover and bursty catch-up checks pass in that run.
A separate full bond run still fails the held-target assertion. One complete
outage model run before the progress-bounded stall change gives 1a five
passes/three failures, 1b four failures, and 1c nine passes/seven failures.
These are one-run results and do not establish three-run proof or a field gain.

Before replacing recovery's timeout, the stall/slowdown tests were restated
as received-bulk outcomes in commit `04a745d`, retaining the existing 70%
bulk gates. They pass on unchanged stage 0 and on the draft. The cold-stall
case checks 60% received service over its fixture's measured half; it does
not establish row 0's seven-second deadline. Commit `46f98fe` separately
restates isolated loss and counter fixtures with authenticated, responsive
alternates. The bond fixture passes on `f75668e` after a test-only adapter
for `Path`'s changed return type; an initial compile failure is not behavior
evidence. No production adapter was applied.

Field trials remain bounded, temporary and individually identified. A router
update interrupted preflight: 5G was observed down, later 5G was up while
Starlink was down, one edge SSH attempt timed out, and both uplinks were
subsequently observed up. No WAN impairment or candidate had been installed
at those checks. The corrected TCP collector verifies connectivity before
offering 8,000 payload B/s per direction for 30 seconds beside voice, at
most 240,000 bytes per direction. Its TUN-only, exact-peer TCP input rule
expires after 180 seconds and is removed in cleanup. Candidate builds still
use `candidate.sh` and its independently verified restoration timers.

Subsequent observed `c8` checks pass the full non-privileged gate and Nix
build before a later TCP-model correction. Its source and executable hashes,
remaining scenario failures and reverted isolation experiment are in the
[trial record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md).
Candidates delivered all voice echoes in three individually recorded mobile
egress blackouts across two different binaries. The deployed baseline lost
34/37 echoes in one round and zero in its repeat; this is not consistent
evidence of a field gain or three repetitions of one candidate. Native TCP
on both builds reaches the same 8,000 B/s offered ceiling. All runtime
overrides, task firewall rules and WAN qdiscs were restored and verified.
The recorded mobile VLAN deltas total 45.135906 MB including collection;
management binary transfers are separately reported, with unknown provider.

A reproduction subsequently found the TCP models forgetting Karn history
when their retransmission scoreboard resets at an RTO. The exact test fails
on `f75668e`, delaying a required retry after an ambiguous RTT sample; it
passes when retransmission removes the original's RTT sample timestamp.
Both models are corrected without changing policy or scenario thresholds.
The Karn-corrected original-controller series completed all 52 cases three times,
with identical measurements and verdicts. Outage passes/failures remain
1a 5/3, 1b 0/4, and 1c 5/11; radio 2d and the 3a latency findings remain
passes. Evidence: `adaptive-policy-f75668e-karn-three.jsonl` and its summary
under the evidence root. Earlier measurements retain their previous model
provenance. No candidate lab series or
existing continuity/benchmark/UDP acceptance is established by these field
observations; stages 1–3 remain unaccepted.

Subsequent RTT-model reproductions fail on original-controller source:
previously SACKed data is timed again on cumulative progress (1.15-second
RTO), and fresh SACK timing is ignored (initial one-second RTO). Both models
now share receipt-timing selection, excluding already SACKed data and
ambiguous retransmission feedback. The new reproductions pass; no policy or
gate threshold changes. Evidence: `model-sack-f75668e-red.txt` and
`model-sack-timing-green.txt`.

The `c8` draft still fails all bulk 1a cases and three recovery cases under
those inputs. A gigaradio trace records its live survivor's 676,154 wire B/s
target at the deadline requiring 20,242,721 TCP payload B/s. The low estimate
and repeated delay cuts already precede the blackout. The explained failure
stops this attempt; its code is retained on `adaptive-stage1-c8-final`
(`94b15c4`), and `main` is restored to the stage 0 controller with test-model
corrections. This model diagnosis does not establish a field root cause.
The [trial record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md)
retains the phase bounds, source hashes and restoration evidence.

The fresh-SACK-corrected baseline completes all 52 cases three times with
identical measurements and verdicts. Outage passes/failures remain 1a 5/3,
1b 0/4, and 1c 5/11. Radio 2d now fails direction-0 voice p99 at 151 ms
against the unchanged under-150-ms gate, while bulk passes both bounds.
This supersedes its older-input pass for current acceptance. Evidence:
`adaptive-policy-f75668e-sack-three.jsonl` and its summary. The restored
main branch passes the full non-privileged gate and `nix build`
(`stage1-c8-final-restored-{nonprivileged-gate,nix-build}.txt`). These checks
establish neither candidate lab acceptance nor completion of stages 1–3.

### Operator-approved C8 release — 2026-10-04

The operator subsequently approves committing and tagging the tested C8
candidate for their installation. Production code `4a1cd54` matches the
tested `94b15c4`; later model corrections remain. Release `v0.0.2` contains
ACK-progress live/suspect/dead eligibility, shared DATA/ACK pacing and
residual bulk service on a sole slow voice-bearing survivor. No profile,
wire encoding, configuration or acceptance threshold changes.

**Operator evidence:** a sequential Starlink → 5G → tunnel Speedtest at
20:18–20:19 UTC reports 0.49/0.46, 44.58/2.42 and 66.19/0.33 Mbit/s down/up.
Tunnel loaded download latency is 105.70 ms, with a maximum of 831.68 ms.
Different servers and changing RF service prevent a matched aggregation
claim. Upload and latency spread remain open. This supplements the bounded
interleaved blackout comparisons; it does not prove the failed model or
incomplete lab/regression gates.

**Observed:** the full non-privileged gate and three liveness outcome tests
pass with C8 and current model inputs. Both hosts are restored and verified
at 20:22 UTC: deployed hashes, empty overrides, inactive restoration timers,
adaptive lanes UP and edge WAN qdiscs `noqueue`. The operator window advances
mobile RX+TX counters by 213.36 MB, including tests, management and background
traffic. It includes the activation interval. See the
[release record](../../docs/drafts/20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04)
for source identity, supplied measurements, retained failures and check logs.
Stages 1–3 are not accepted by their gates.

### Build identity in measurements — 2026-10-04

`wanbond monitor` and the dashboard now display the daemon's source commit
and UTC commit time; `wanbond version` reports the invoked executable.
Go builds use embedded VCS metadata, and Nix builds explicitly stamp the
flake revision/source time. Modified source appends `-dirty`; unavailable
metadata displays `unknown`. The time is not compilation time or uptime.
Retain the executable hash and any dirty patch as well: a base revision with
uncommitted edits does not establish their contents.

Observed CLI/UI reproductions fail before this feature because the build
fields are omitted; both pass afterwards. The full non-privileged gate
(44 frontend tests), e2e/realhosts source vet checks and three binary smoke
cases pass: Go VCS identity, unavailable VCS identity and explicit stamps.
No adaptive transport policy, lab profile or gate changed. Evidence:
`/srv/nvme/tmp/wanbond-adaptive-evidence/build-identity-20261004-205649/`.
Nix build and packaged identity verification are required before handover.

A subsequent boundary reproduction found numeric times outside RFC3339's
year range being accepted. Validating UTC text encoding rejects those inputs;
the reproduction now passes. `timestamp-range-{red,green}.txt` retains both
results alongside the build-identity evidence.

### Installed C8 baseline and directional investigation — 2026-10-04

**Operator decision:** use `b444920`, including build identity, as the new
operational baseline. **Observed:** both hosts run that exact revision with
source time `2026-10-04T21:13:41Z` and executable SHA-256
`dce5c7e4dae9a13565e04c69aa2ac36a6ab388a51418282a027e14e18f4e5dd9`.
The investigation does not replace binaries, restart daemons or impair WANs.
The selected exit remains `raspi5l` after collection and cleanup.

Voice-only preflights precede controlled same-destination TCP. Direct
Starlink/5G references precede tunnel measurements; receiver timestamps and
exact payload checks are retained. Offered rates bound usage: 1 Mbit/s
(1.25 MB/transfer), then 3 Mbit/s uploads (3.75 MB) and a 10 Mbit/s download
(12.5 MB). **Observed:** tunnel up/down reaches 1.001/1.001 Mbit/s in the
first sequence; later uploads reach 3.001 and 2.999 Mbit/s, the latter after
a 9.996 Mbit/s download. Direct 5G uploads bracket both later measurements
at 2.989–3.000 Mbit/s. These are service lower bounds, not capacity or
aggregation claims. They do not reproduce the operator's 0.33 Mbit/s upload
after an uncapped Speedtest. No field policy gain or loaded-voice gate is
asserted from these baseline-only rounds.

`TestAdaptiveFieldStandbyDirectionalService` measures each bulk direction,
with and without voice, while retaining real reverse TCP ACK demand. All
four healthy fixed standby cases pass three identical repetitions on both
original `f75668e` and C8. This is a finding, not a newly fixed defect or a
replay of RF conditions. The model change is in `2395c92`; production is
unchanged. All 52 section 4 model cases have three identical completed
measurements/verdicts on C8, but many fail. The first full invocation times
out at ten minutes during repetition three; a separate twenty-minute-limit
run completes missing subtests. No timed-out case counts as completed.

A narrower gigaradio deadline reproduction observes a live survivor with
ACK-progress age 33 ms and zero queue delay, paced at 653,342 B/s against
required payload 20,242,721 B/s. **Inference:** stage 1 cannot prove that
gate with the legacy estimator retained; the later delay/capacity model is
a dependency. This is not the field upload cause. Stage acceptance and
both-family lab/regression completion remain outstanding; none is newly
claimed here. Full scenario counts, provenance, reproduction commands and
field tables are in the
[installed-baseline checkpoint](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#10-installed-baseline-and-upload-investigation--2026-10-04).

Use disk-backed `/var/tmp` for field logs and preserve the observed exit
policy on failed setup as well as success. This investigation corrects
collector cleanup and `/run` exhaustion separately from policy. The operator
requests removal of obsolete edge `/run` artifacts: 453.339 MB is archived,
verified and removed, with unchanged daemon/config hashes and PID. Completed
collection intervals account for 53.504 mobile RX+TX MB (first setup and one
incomplete failure excluded). The enclosing 21:21:40–21:59:46 UTC interval is
217.118 MB including deployment, management and background; nested counts
must not be added. Cleanup accounts for another 0.058 MB including background.
These are VLAN counters, not provider billing. Evidence is retained under
`/srv/nvme/tmp/wanbond-adaptive-evidence/`; see the checkpoint for exact paths.

### Outcome restatements and replacement experiment — 2026-10-05

The main controller remains the installed C8 policy. Before replacing delay
and capacity mechanisms, deterministic model tests now check lane selection,
voice latency/loss, useful bulk delivery and physical traffic budgets. Target
peaks and controller consequence counters do not establish those outcomes.
Three identical unchanged-C8 runs pass the directional delay, queued sparse
and resumed service, batched-receipt, restart-budget and lightly loaded voice
outcomes. The stronger bidirectional-noise, policed-service, stale-floor and
slow-lane-upgrade cases retain their baseline failures under `adaptivepolicy`.

Observed in the isolated replacement: a 90 kB/s lane upgraded to 625 kB/s
carries 335,640 B/s against a 417,458 B/s independent payload reference after
removing the one-bulk-datagram flight restriction. The earlier trace drops no
physical WAN frames while TCP times out and tunnel queues discard traffic;
that fixture does not support a physical-loss explanation. Policed service
and slow steady-path utilization still fail. No new lab or field trial, host
binary change or WAN impairment accompanies this model checkpoint. The
[execution record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#11-revised-execution-goal--operator-2026-10-04)
records the metric targets and prototype provenance. These measurements are
not a field improvement claim.

### Isolated flight allowance in the field — 2026-10-05

Observed C8 experiment `7860b97` removes the one-bulk-datagram flight cap
without replacing estimators. Its non-privileged gate fails existing voice
outcomes for a slow survivor, ACK backlog and takeover. Temporary paired-host
testing uses 15-minute restore timers, matched restarts, voice preflights,
direct uplink references, then voice during capped tunnel transfers.

Baseline → experiment → baseline downlink is 8.23 / 8.15 / 10.01 Mbit/s;
uplink is 2.99 / 2.99 / 2.86 Mbit/s. Measured loaded voice has zero loss,
with latency within the baseline spread. Raw downlink capacity is unmeasured;
an upload reaching its 3 Mbit/s offer proves only that lower bound. There is
no established field improvement and no new lab acceptance claim.

Both hosts are restored to `b444920`, runtime overrides/timers are cleared,
and edge WAN qdiscs remain `noqueue`. Experiment-owned edge `/run` reference
directories are archived and removed; active configuration is retained.
The corrected comparison uses 99.479 mobile RX+TX MB; the enclosing interval
including setup attempts, management and background is 157.720 MB. Do not
add nested intervals. The [execution record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#isolated-flight-allowance-field-comparison--2026-10-05)
records exact identity, measurements, failed setup provenance, clock/window
limits and artifacts. No production controller changes accompany this record.

### Controlled field rate upgrade and model reproduction — 2026-10-05

A second temporary comparison of the same flight-cap removal shapes only
mobile wanbond UDP, 400 kbit/s → 2 Mbit/s, with voice and a thirty-second
capped uplink TCP flow. Fresh direct uplinks precede every phase, and network
cleanup timers precede each change. Baseline → experiment → baseline later
payload rates are approximately 1.36 / 0.24 / 1.55 Mbit/s. Whole-report bounds
are 1.10–1.64 / 0.19–0.31 / 1.27–1.85; interpolated partial reports do not
establish exact deadline verdicts. Guarded TCP-active voice windows have zero
loss; p99 edge/hub RTT is 62.4/57.2, 61.0/55.3 and 72.6/71.2 ms. No repeatable
gain is established. Both baseline binaries, empty overrides, cleared test
timers/firewall rules and unchanged WAN qdiscs are verified after restoration.
The enclosing mobile VLAN interval through cleanup advances by 42.996 MB.

Collector corrections use 1,200-byte iperf writes and gzip log archives. An
earlier baseline attempt with default 128 KiB writes yields coarse reports
and no candidate result. Full-stream trailing hub voice misses result from
stopping its echo server before its sender; local guarded TCP-active windows
exclude that cleanup interval. Raw and corrected analyses both remain.

The new tagged `TestAdaptiveVoiceLaneUplinkUpgrade` uses fixed two-lane inputs:
a voice-bearing 50 kB/s uplink increases fivefold beside a 62.5 kB/s policer.
Original `f75668e`, C8 and the isolated flight-cap removal each deliver
27,600 B/s at the ten-second deadline against a 217,408 B/s reference,
identically three times. Voice loses none at 24 ms p99. This is a model
failure, not an exact replay: the field baselines recover. In the unaccepted
replacement, `Poll`-triggered pushes of queued real traffic improve model
deadline/sustained payload to 180,000/165,120 B/s. Other voice outcomes still
fail. No three-run lab series or production estimator replacement is claimed.
The [execution record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#controlled-uplink-upgrade-and-two-lane-reproduction--2026-10-05)
links source, full measurement provenance, rejected hypotheses and accounting.

```sh
nix develop --command go test -tags adaptivepolicy ./internal/bond \
  -run '^TestAdaptiveVoiceLaneUplinkUpgrade$' -count=3 -timeout=15m -v
```

### Estimator replacement field tradeoff — 2026-10-05

Unmerged source `b5948c5` replaces delay then capacity estimation and moves
bounded real-traffic pushes into `Poll`. The deterministic two-lane upgrade
case passes identically three times at 192,000/175,680 B/s deadline/sustained
payload, versus a 217,408 B/s independent reference. Voice loses none at
36 ms p99, compared with baseline 24 ms. A full scenario 2a–2d, 3a–3c, 0
run still fails every top-level case: unloaded delay-change variants pass,
all bulk-loaded delay-change variants fail, and only gigaradio passes 2d.
Selected default slow-survivor voice, ACK backlog and small-flow discovery
also fail. No three-run lab series or accepted policy is established.

A bounded field baseline → candidate → baseline controlled rate upgrade
delivers approximately 0.28 / 1.41 / 0.29 Mbit/s in the late receiver window;
report bounds preserve the increase. Edge voice p99 is 50 / 168 / 49 ms with
two candidate losses; hub p99 is 48 / 139 / 48 ms. The low-rate window also
regresses. This paired set establishes a tradeoff and does not explain why
the earlier field baselines recovered faster. Mobile RX+TX is 21.805 MB for
the comparison, 26.516 MB through cleanup; those intervals overlap.
Both baseline binaries, empty overrides, cleared test timers/firewall rules,
original qdiscs and exit policy are independently verified after restoration.
Owned `/run` reference artifacts and temporary binaries are removed after
archive verification. The
[execution record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#estimator-replacement-field-tradeoff--2026-10-05)
retains source, uncertainty, reproduction and the next experiment.

The post-push qualifier follow-up (`ef36799`) also remains unaccepted. In a
second bounded field set, low-rate payload rises from about 0.15 to 0.24
Mbit/s with separated report bounds; later recovery is 1.41 / 1.56 / 1.39
Mbit/s with overlapping bounds. Voice has zero loss but candidate edge/hub
p99 is 100/118 ms, above the baseline. Both hosts are restored and verified;
the comparison uses 27.367 mobile RX+TX MB, 32.391 MB through cleanup.

Later isolated byte-clock and ACK-round-aging corrections pass small-backlog,
batched-receipt, directional-noise and upgrade outcomes three times, including
186,000/168,960 B/s upgrade delivery and zero voice loss at 36 ms p99. Slow
voice, ACK backlog and steady utilization still fail. These later sources
have no field result and no accepted lab series. The
[follow-up record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#post-push-correction-and-sampling-follow-up--2026-10-05)
retains the fail-first reproductions, source versions, all paired baselines
and rejected probe trials.

The bursty underused-lane regression now checks delivered payload rather than
the controller's target. The unchanged 300 Mbit/s link, 0.4% random loss and
10 ms correlated jitter carry 92 datagrams every 10 ms. Measurement counts
unique payload created from 10 to 30 s, allows 500 ms to drain, requires at
least 99% delivery, and retains zero AQM drops. C8 passes identically three
times at 99.98%; this establishes the pre-replacement service baseline.

```sh
nix develop --command go test ./internal/bond \
  -run '^TestUnderusedLossyLaneDeliversBurstyBulk$' -count=3 -v
```


### Holding-source model and field follow-up — 2026-10-05

Unmerged source `1d272f9` preserves current/pre-push pace until matching
physical delay feedback arrives and removes plateau-dependent long pushes.
Selected outcomes pass three times: batched payload 4,982,720 B/s;
ACK-backlog voice 200/200 at 80 ms one-way p99; steady utilization
98.2%/97.4%; fivefold upgrade payload 189,600/172,560 B/s with zero voice
loss at 36/40 ms RTT p99. Its full default bond run retains eighteen
failures, including cold discovery, policer loss and slow voice. Build/vet
and Nix build pass; no accepted three-run lab series is established.

A temporary field baseline → candidate → baseline rate-upgrade set completes
without cleanup errors. Late upload is about 1.41/1.51/0.30 Mbit/s, with
candidate report bounds overlapping the initial baseline. Voice loses none;
candidate p99 falls between the baselines and receive gaps worsen to
131/134 ms. Raw peer AQM drops decline while repairs and expirations do not
both decline. This does not establish improvement across all metrics or a
download gain. Baseline binaries, network state and absence of owned
timers/overrides are verified; owned `/run` references/binaries are archived,
compared and removed. Evidence is `estimator-feedback-field-upgrade-20261005/`,
with 22.601 mobile RX+TX MB for the comparison and 24.534 MB through cleanup
(nested intervals). The
[execution record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#holding-source-model-and-field-follow-up--2026-10-05)
retains source, failure provenance and further work.

### Receiver-cohort field rejection — 2026-10-05

One paired baseline → `a8fe4a9` → baseline set completes with clean restoration.
Late upload is estimated at 1.432/1.561/1.395 Mbit/s, with overlapping
whole-report bounds. Candidate voice RTT p99 is 186/192 ms against baseline
47/47 and 50/51 ms; gaps reach 178/180 ms and candidate voice loses 4/1424
and 9/1411 datagrams while both baselines lose none. Reject promotion: the
accounting corrections do not preserve voice. Direct Starlink references are
about 0.52 Mbit/s; 5G reaches the 3 Mbit/s offered cap in every phase. No
three-run field pass, download gain or lab series is established.

Both baseline executable hashes, empty overrides, timer/firewall removal,
`noqueue` WAN qdiscs and the operator's `raspi5l` policy are independently
verified. Owned edge `/run` references are archived, compared and removed;
inactive candidate binaries are removed from both hosts. The evidence folder
is `receiver-cohort-field-upgrade-20261005/`. Mobile RX+TX advances 24.679 MB
for the comparison and 26.459 MB through cleanup (nested, including background).
The full source has 20 default bond failures, although Nix and other
non-privileged checks pass. The next failing reproduction isolates reuse of a
pre-congestion capacity sample; its role in this field regression remains a
hypothesis. See the plan's execution record for raw-counter limitations and
exact source identity.

### Congestion-history field checkpoint — 2026-10-05

One paired set for `5540733` has late upload 0.232/1.511/1.388 Mbit/s. Candidate
bounds overlap the final baseline, while voice RTT p99 is 156/123 ms versus
baselines 55/54 and 65/63 ms. It loses 2/1410 edge-origin voice datagrams;
both baselines lose none. Reject promotion despite improved tails against the
preceding experiment. Direct Starlink references are about 0.52 Mbit/s and
5G reaches the 3 Mbit/s offer. No download or three-run acceptance is claimed.
Both baseline binaries, policy and cleared network state are independently
verified; owned edge runtime references and inactive binaries are removed.
`congestion-history-field-upgrade-20261005/` records 23.517 mobile RX+TX MB
for comparison, 28.902 through cleanup. Both new sets' enclosing interval is
63.778 MB including background/gap; these intervals overlap.

The next separate sampler reproduction fails three times: bulk queued for
another lane certifies sparse protected voice service as capacity (14,450 B/s),
though this lane has unused service. The initial fixture-age failure is retained
separately and is not evidence for the defect. This is a model finding, not a
proven explanation of the field result. Replace global sampling qualification
with existing per-lane class demand before another capped trial.

### Eligible-backlog field checkpoint — 2026-10-05

Source `fd088cc` passes the new sampling outcomes, tagged upgrade and fast
shared-lane service three times; 16 default bond outcomes remain failed. Its
new field comparison is rejected: estimated late upload 1.425 Mbit/s overlaps
before/after bounds, while edge/hub voice RTT p99 reaches 188/175 ms and
reply loss rises from zero to 4/1412 and 1/1402. No download or three-run
acceptance is claimed. Evidence is `lane-demand-field-upgrade-20261005/`.
Both baseline binaries and cleared temporary state are independently verified;
owned runtime references are archived/content-compared and removed. Mobile
RX+TX is 26.179 MB for comparison and 28.218 MB through cleanup; the enclosing
three-set interval is 108.600 MB including gaps/background, not an additive sum.

Two new reproductions show congestion handling revising fresh capacity from
application-limited voice or expired delivery (`stage23-congestion-sample-quality-red.txt`).
The initial correction passes them but regresses sole slow-lane voice to
497/500 at 97 ms one-way p99; it is withheld from field testing. Numerical
gates remain unchanged and installed `v0.0.3` remains the reference.

### Fresh-evidence pacing and queue-attribution checkpoint — 2026-10-05

Experimental `897004c` passes targeted accounting, physical-feedback aging,
noise and upgrade outcomes, but the complete non-privileged gate has 20 bond
failures. Frontend's 44 tests, build/vet, patched-engine tests, formatting and
all other Go packages pass; Nix build passes. Its timer-backed field comparison
raises low-rate upload bounds to 0.230–0.282 Mbit/s above both baseline upper
bounds (0.181/0.188), but loses 1.77%/1.13% of voice datagrams and is rejected.
The voice-before-bulk model passes three times on both sources; it is not a
reproduction of the field loss. No new complete VM/profile gate is claimed.

Both deployed baseline hashes, network state and operator exit policy are
verified restored. Owned runtime references are archived/verified/removed;
mobile use is 25.767 MB during comparison, 34.153 through cleanup including
background. The intervals overlap. Evidence is
`fresh-pacing-field-upgrade-20261005/` and
`stage23-voice-before-bulk-{reproduction,b444920-common-driver}.txt`.

Class/cause counters now distinguish small non-TCP and TCP queue drops at
admission, residence deadline and persistent real-time backlog shedding.
The public transport/collector test first fails three times on unchanged
baseline behavior, then passes with counting added; policies and wire fields
are unchanged. `small_queue_drops_total` emits six series whose sum matches
the aggregate. These counters were not present during the paired field set.
The subsequent counter-only diagnostic observes three guarded real-time
backlog-shedding drops at the edge, zero small TCP/admission/deadline drops,
and none at the hub; four startup drops fall outside the guarded window.
Local loss timing correlates with the later edge drop interval, but no
per-packet cause or all-metric gain is established. Voice p99 is 75.4/161.3 ms.
Both hosts and temporary network state are independently verified restored;
mobile RX+TX is 9.665 MB during the run / 10.678 through cleanup, overlapping
intervals. Evidence is `queue-cause-field-diagnostic-20261005/`. The detailed
numerical gates remain fixed. The default standby startup test separately
reproduces 12 stale real-time drops and 88/100 delivery three times; it does
not reproduce this field trace.


The recovery-cohort prototype passes new actual-receipt reproductions for a
qualified sample followed by sparse traffic and a late pre-response cohort.
Observation age and physical send time serve different checks. Its existing
steady/noise outcomes pass three times, but upgrade service is 280,320 B/s
against a 75% gate on 417,458 B/s; voice-first constant-delay p99 is 83/175 ms.
Startup and sole slow-lane failures remain. This is an experimental model
checkpoint, not completed VM gates or a field improvement. Evidence is
`stage23-post-response-qualified-recovery-repeat.txt`.


The subsequent `0e5de63` field comparison retains both baselines: candidate
late upload bounds 1.091–1.651 Mbit/s overlap the returning baseline's
1.114–1.678. Hub voice p99 is 172.9 ms versus 63.5/57.4 ms baseline, with
one lost echo and no local small-queue drops. Promotion is rejected; the
component causing the tail is unknown. The completed default gate has 19
bond failures; all other non-privileged parts and Nix build pass. Both exact
deployed baselines and temporary network state are independently verified
restored; owned runtime references are archived/verified/removed. Mobile
RX+TX is 25.009 MB during comparison / 26.618 through cleanup, overlapping
intervals. Evidence is `qualified-cohort-field-upgrade-20261005/`; no new
completed VM/profile gate or repeatable all-metric gain is claimed.


Passive `small_queue_residence_seconds` histograms distinguish admission-to-first
transport-submission waits from later socket/kernel/path time. The public
transport/collector test first fails three times with 50/25 ms observed waits
but no export, then passes with counting added. Repairs and queued expiry do
not add residence samples. This changes no scheduler or wire field and has
now been measured in a candidate-only field diagnostic. Evidence is
`stage23-small-queue-residence-{red,green}.txt`.

Main's full non-privileged gate and Nix build pass with passive residence
telemetry. Experimental `dacf50a` observes local real-time queue-wait p99
bounded by 5/1 ms alongside voice RTT p99 141/163 ms. Echo handling stays
below 0.32 ms, while the kernel shaper has up to 17.9 KB queued and the
congestion allowance exceeds 200 ms. This separates measured components;
it does not attribute individual echoes or establish a matched baseline gain.
The larger sampler archive exceeds a harness extraction limit; captured logs
are recovered locally with exact member-name/size validation and the failure
flags preserved. Both baseline services and temporary network state are
independently verified restored. Mobile RX+TX is 9.506 MB during the run /
10.802 through cleanup, overlapping intervals. Evidence is
`queue-residence-field-diagnostic-20261005/`; no completed VM/profile gate is
claimed. Late pre-control-period receipts separately reproduce contamination
of unloaded RTT and the congestion threshold three times. The first
application-limited sampling replacement fixes those cases but regresses
noisy-link bulk and is rejected. Current-receipt qualification additionally
exposes release-order dependence; sampling after all physical releases fixes
that reproduction, but noisy bulk, steady queueing and voice-loss outcomes
still fail. Those sampling choices remain rejected. See the plan's
receipt-period reproduction; no field gain is claimed for them.

The accepted-policy component control `38a58f7` has zero guarded voice loss,
64/59 ms voice RTT p99, local real-time wait p99 bounded by 1/1 ms and 4.3 KB
maximum shaper backlog. The fresh direct 5G reference drops to 1.117 Mbit/s;
raw throughput is not a matched comparison with C20. The runner completes and
both baseline services/network state are independently verified restored.
Mobile use is 8.375 MB during the run / 11.701 through cleanup, overlapping
intervals. Evidence is `baseline-residence-field-diagnostic-20261005/`.

An overdue-original recovery reproduction fails three times on C20 and
`b444920` while the original's lane remains live. Replacing that first-copy
trigger passes the reproduction and three selected model runs, preserving
noisy bulk while changing constant-delay voice p99 from 83/175 to 120/133 ms.
The opposite-direction and later-tail tradeoffs are retained. The completed
B/C/B field comparison rejects source `24e0318`: guarded voice loss is
17/1413 and 15/1395, with p99 206/205 ms, versus zero loss and 50–57 ms p99
in the surrounding baseline phases. The edge records 32 real-time/stale
queue drops and local wait p99 bounded by 50 ms. Candidate/returning-baseline
late upload bounds overlap, and direct 5G references vary from 1.154 to
2.014 Mbit/s. No throughput gain is established. The full non-privileged gate
has 20 bond failures; other components and Nix build pass. Both deployed
baseline hashes, exit, timers and network restoration are independently
verified. Mobile RX+TX is 21.783 MB during comparison / 37.282 through cleanup,
overlapping intervals. Evidence is `late-original-field-upgrade-20261005/`.
Installed `v0.0.3` remains the reference; no completed VM/profile gate is claimed.

A separate public model reproduces fresh real-time wait behind 19 recovery
copies after a lane failure: 26 ms on unchanged C8 and `b444920`, three times
each, against the documented 20 ms local queue target. The isolated C8
scheduler choice `388a6d6` sends fresh small datagrams before pending repairs
and fresh bulk. Fresh scheduler wait becomes zero three times, all 19 older
originals still recover, and the full bond suite passes. The existing buffered
rate/outage, standby startup and voice-lane failure guards pass three times.
No estimator, copy trigger, constant or wire field changes. Its full
non-privileged gate and Nix build pass, including frontend's 44 tests and the
patched engine. End-to-end field verification remains pending; no VM/profile
gate is inferred from the model. Evidence is
`stage23-{c8,b444920}-original-priority-red.txt` and
`stage23-c8-original-priority-{green,green-measured,bond-gate,full-nonprivileged-gate,nix-build}.txt`.

After restarting the exited lab guests, the authentication rejection probe
initially reuses an authenticated SSH control socket and returns zero without
performing authentication. The harness now disables connection sharing for
that negative probe. Fresh connections reject password/keyboard-interactive
authentication on both guests with exit 255 after advertising publickey only;
the server policy is unchanged. Captured authentication traces are
`stage23-lab-{hub,edge}-auth-{mux-red,fresh-green}.txt`. This setup correction
does not establish any transport gate. Stale metadata is archived only after
verifying both recorded guest processes have exited and acquiring the lab lock;
persistent disks and previous measurements are retained.
The full normal shutdown/start cycle then completes with the corrected
negative probe and both WAN reachability checks; evidence is
`stage23-lab-auth-fix-{shutdown,full-up}.txt`.

The fresh-first field source `d7f9a01` completes B/C/B collection and cleanup.
Late upload bounds overlap across phases. Guarded voice p99 is 55/50 ms;
one edge-client echo is missing, versus zero loss in both surrounding
baselines. Candidate local real-time wait p99 is bounded by 1 ms on both
hosts and all small-queue drop counters are zero. This does not establish
the modeled WAN-failure benefit or a repeatable field gain. Exact deployed
baseline hashes, exit and temporary network/runtime restoration are
independently verified. Mobile-interface RX+TX is 24.512 MB during comparison
/ 25.356 through cleanup, overlapping intervals. Evidence is
`c8-original-priority-field-upgrade-20261005/`; no new completed VM/profile
gate is claimed from this field trial.
The rejected receipt-time delay choice separately has 22 default bond failures
and a passing Nix build. No new complete VM/profile gate is claimed.

Three fresh-first radio blackout collections subsequently complete, but their
section 4 checks fail all three runs: whole-run arrival gaps exceed the limit
each time, and one run also misses the returning satellite lane's two-second
physical bulk-receipt deadline at the edge. Each has two failed checks and
19–20 inconclusive checks because independent references are absent. The
first paired baseline has 85.1% median host CPU busy and guest observer delays
up to 1.3 s; the candidate and return baseline still have 83.0/84.3% median
host CPU busy. CPU interference is an inference, not a per-packet attribution.
Evidence is `stage23-c8-original-priority-lab-radio-{comparison,outage-counters}.json`
and the retained candidate gate reports. No three-out-of-three pass is claimed.

The voice-only field WAN-failure comparison is incomplete: its baseline
phase delivers 2748/2750 echoes in each direction across two ten-second
single-WAN egress blocks, with maximum arrival gaps 62/95 ms. The edge reboots
during the subsequent compressed binary transfer, before candidate activation.
The returned edge has the deployed baseline hash and unshaped WAN queues;
the reboot cause is unknown. Counter reset prevents a total mobile-byte claim;
5.468 MB is observed only through the last pre-reboot sample. Evidence is
`c8-original-priority-field-blackout-20261005/`. This is a baseline diagnostic,
not a candidate field result.

The next fresh-first field set captures all three B/C/B voice phases, each
with 2750 echoes per direction and two ten-second single-WAN egress blocks.
Candidate edge/hub losses are 6/6, against 17/19 before and 10/14 after.
Hub RTT p99 is 69 ms against 103/101 ms; edge tails and arrival gaps are mixed.
Four consecutive missing candidate echoes exceed the unchanged limit of
three. The immediate candidate direct 5G reference is higher than both
baselines, so the observed lower loss is not yet a repeatable policy gain.
All phase network cleanup completes. A reboot interrupts final SSH
verification; `comparison.json` retains that failure and
`recovered-comparison-state.json` records the separately recovered third
phase and independent restoration checks. Both deployed hashes, empty
overrides, unshaped WAN queues, `auto` operator policy and removal of owned
runtime artifacts are verified afterwards. The operator reports maintenance
events and stable power. Total mobile RX+TX is unknown after counter reset;
17.884 MB covers only the last pre-reboot sample. Evidence is
`c8-original-priority-field-blackout-locked-20261005/`.

A discarded retry attempted to launch a second runner while the first still
owned a removal timer. The timer guard rejects the second before mutations;
the first is stopped and owned cleanup verified. Subsequent runs acquire a
local exclusive lock before SSH; a second acquisition is tested to fail.
The discarded retry supplies no comparison result.

Three gigaradio candidate collections report respectively 5 failed / 20
inconclusive, 0 failed / 16 inconclusive, and 3 failed / 16 inconclusive
checks. The second is not a pass. Arrival gaps reach 1413/1309 ms in the first
and 640/612 ms in the third. Independent references are absent. Evidence is
`stage23-c8-original-priority-lab-gigaradio-gate-summary.json`; no
three-out-of-three gate is established on either profile family.

Combining the fresh-first scheduler with age-based recovery (`54053eb`)
fixes the missing-original model but fails the buffered rate-reduction guard
three times: target 170161 B/s after five seconds at 62500 B/s. Unchanged
fresh-first passes that guard three times. This combination is rejected
before field activation; its Nix build passes. Model traces are retained as
`stage23-c8-{priority-recovery,original-priority}-rate-fall-*.txt`.

The next fresh-first C8 field B/C/B repeat completes without reboot or cleanup
errors. Candidate losses are 22/21 of 2750 echoes, against baseline 12/4
before and 31/42 after; candidate RTT p99 is 181/167 ms against 149/139 and
198/197 ms. Candidate consecutive losses are 7/8. The immediate 5G references
are 3.007/2.207/1.933 Mbit/s; the first reaches the 3 Mbit/s offer and is only
a lower bound. This drifting comparison does not reproduce the earlier gain.
Both deployed hashes, `auto` policy, timers, network and owned-artifact removal
are independently verified. Mobile RX+TX is 0.946 MB during staging, 16.517 MB
during comparison and 17.640 MB from comparison start through runtime cleanup;
the latter two intervals overlap. Evidence is
`c8-original-priority-field-blackout-repeat2-20261005/`.

`TestAdaptivePolicy2aBufferedLowLatencyLaneFalls` adds a quieter buffered
rate-fall reproduction under the existing `adaptivepolicy` progression tag.
It checks the unchanged 150 ms voice limit, 75% available-payload requirement
in the last second before the five-second deadline, and the existing
three-per-100-ms expired-burst limit. Main fails three identical runs at
226800 B/s against a 546092 B/s reference, with a four-datagram expiration
burst; voice p99 is 65 ms with no missing echoes. The unmerged estimator plus
fresh-first scheduling (`c2d9976`) reaches 511200 B/s with 90 ms voice p99 and
no missing echoes, but still expires four together. It is not a complete 2a
pass. Earlier diagnostics measured the following second and are retained as
diagnostics, not deadline gates. Evidence is
`stage23-c8-buffered-low-latency-rate-fall-red.txt` and
`stage23-estimator-fresh-priority-buffered-rate-fall.txt`.

The prototype's fresh-priority reproduction separately fails three times at
26 ms, then passes three times at zero while all 19 older originals recover.
Its full default gate retains 22 bond failures; frontend's 44 tests, build/vet,
patched engine, formatting and other packages pass. Its Nix build passes.
Main's complete non-privileged gate passes. Evidence is
`stage23-estimator-fresh-priority-{red,green,full-nonprivileged-gate,nix-build}.txt`
and `stage23-buffered-rate-fall-model-main-full-nonprivileged-gate.txt`.

### Estimator plus fresh-priority field repeats — 2026-10-05

Unaccepted source `72fcc9d`, executable SHA-256
`b8e4e971dc51ca9c0689a751ba2b7ba824fa2e03ddfdfa4a0b55610faf9baa73`,
completes three voice-only B/C/B sets. Each phase sends 2750 echoes per
direction at 50 Hz, with separate ten-second edge-egress UDP blocks for
5G and Starlink. Immediate capped direct references precede each tunnel
phase; both deployed hashes, boots, restore timers, `auto` exit policy and
owned artifact removal are independently verified afterwards.

| Set | Baseline-before losses edge/hub | Candidate losses edge/hub | Baseline-after losses edge/hub | Candidate RTT p99 edge/hub, ms | Candidate arrival gaps edge/hub, ms |
|---|---|---|---|---|---|
| 1 | 19/18 | 0/0 | 30/56 | 106.4/98.5 | 118.1/151.1 |
| 2 | 10/5 | 0/0 | 7/7 | 154.2/154.9 | 236.1/232.2 |
| 3 | 52/71 | 0/0 | 8/8 | 138.1/138.2 | 160.1/158.4 |

Zero candidate loss repeats; whole-run gaps fail the unchanged 150 ms limit
in all sets, and the second set worsens RTT p99. Direct 5G service varies
across phases, so these observations do not establish a controlled all-metric
policy improvement. No tunnel TCP goodput is measured. Monotonic arrival
reconstruction confirms the gap failures; candidate clock differences are
below 0.1 ms. Guarded small-queue drops are zero, with local real-time
residence p99 bounded by 5 ms. These aggregates do not attribute individual
stalls. Evidence folders are
`estimator-fresh-priority-field-blackout{,-repeat2,-repeat3}-20261005/` and
`stage23-estimator-fresh-priority-monotonic-gap-audit.json`.
Mobile RX+TX including background is 53.631 MB during the three comparisons,
58.193 MB from their respective comparison starts through runtime cleanup;
those intervals overlap. Separate staging adds 2.643 MB.

`TestAdaptivePolicyBulkRecoveryUsesAlternateBeforeRepairExpires` is another
public progression reproduction. One original is lost on the first lane;
the alternate proves physical ACK progress before the original's 250 ms
repair lifetime. Main receives 0/1 and expires one, three identical runs.
The API-adapted fixture on original `f75668e` fails for the same reason three
times. It remains under `adaptivepolicy`; the default gate is preserved.
Evidence is `stage23-{main-repair-deadline,deadline-recovery-original}-red.txt`.

### Capped field upload and model limits — 2026-10-05

Two completed B/C/B field sets shape only wanbond UDP egress on edge 5G to
400 kbit/s, upgrading to 2 Mbit/s at TCP +15 seconds. Two 55-second voice
streams run beside 30-second upload offered at 3 Mbit/s. Each phase has
voice-only preflight and immediate capped physical references; all temporary
changes have restoration timers. Independent final source/hash, boot,
operator policy, timer, firewall, queue and owned-artifact checks pass.

Source `72fcc9d` improves low-phase receiver upload bounds to 0.216–0.268
Mbit/s against 0.144–0.162 before and 0.137–0.167 after, but raises loaded
voice p99 to 158/154 ms against 78/79 and 61/57. No echoes are lost. Qualified
sampler `fcb25f1` gives 0.207–0.243 against 0.137–0.179 and 0.225–0.272;
loaded voice p99 is 135/148 ms, with 2/1 lost echoes against zero in both
baselines. Late-window bounds overlap the preceding baseline. Returning
baseline sends no 5G bulk originals in either guarded upload window.
These are mixed field outcomes; neither source is promoted.

The qualified sampler's physically possible ACK-holding reproduction fails
at 5717 B/s then passes at 10290 B/s, three times. An earlier impossible
fixture and its factor-nine claim are withdrawn; their logs remain retained.
Unqualified receipt time fails the late-receipt accounting guard. Qualified
sampling passes the quiet buffered 2a progression three times, but the full
default gate still fails 23 bond checks. Native Nix and ARM builds pass.

`TestAdaptivePolicyVoicePrimedStandbyStartsUpload` is a separate public
progression model: after ten seconds of voice, upload starts as edge 5G falls
to 400 kbit/s beside standby Starlink. Main receives 28,800 B/s at five seconds
against a 36,563 B/s requirement; both prototypes receive 1200 B/s. Each
repeats three times and passes modeled voice gates. It does not reproduce
the field tail failure. A 100 ms FIFO differs from burst-token TBF; field
backlog/rate estimates are service-time proxies, not packet delays. Lab host
load can distort elapsed-time performance: paired field results decide gains.

Mobile RX+TX including background through cleanup is 28.703 and 27.892 MB;
separate staging uses 0.887 and 0.843 MB. Comparison intervals are subsets,
not extra usage. Evidence, executable identity, bounds and next investigation
are in the [field record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#capped-field-upload-and-qualified-receipt-sampling--2026-10-05).

The receipt-endpoint field set (`1bc29ef`) improves low-upload bounds to
0.186–0.213 Mbit/s against 0.137–0.157 and 0.130–0.166, but worsens loaded
voice p99 to 154/157 ms and whole-run losses to 8/8 of 2750 per direction.
Native/ARM builds pass; 24 default bond checks fail. Untouched f75668e
transport also fails the two newer upload progression cases three times;
its API-adapted current-model fixture is archived. All field boot, identity,
restoration and owned-cleanup checks pass; mobile RX+TX through cleanup is
28.834 MB plus 0.877 MB separate staging. Direct idle ICMP delay now targets
the concentrator public IP; capped TCP references still use the OCI route.
Neither predicts service throughout a later tunnel phase. A further public
reproduction shows bulk suspended before a known 100 ms round trip; the
liveness correction passes that check three times but has no field result.
See the [measurement and timing record](../../docs/drafts/20261002-1730-adaptive-policy-plan.md#receipt-endpoint-field-result-and-feedback-timing--2026-10-05).

### Current-delay field comparison — 2026-10-05

Unaccepted `684b193` passes its targeted ACK/window guards and Nix/ARM builds,
but fails 25 default bond checks. B/C/B field low-upload bounds are
0.135–0.170 / 0.197–0.241 / 0.149–0.188 Mbit/s; late bounds overlap.
Loaded voice p99 is 72/66 / 111/157 / 65/88 ms edge/hub. All 2750 echoes per
direction arrive in every phase. Candidate real-time local residence p99 is
at most 5/1 ms with no stale real-time drops; TBF backlog/rate remains a proxy,
not measured packet latency. The throughput gain does not satisfy the voice
goal. Both boot identities remain stable; deployed source/hash, original
exit policy and network state are independently verified after restoration
and owned inactive files are removed. Mobile RX+TX through cleanup is
27.833 MB, plus 0.874 MB separate staging; the 26.980 MB comparison interval
is nested and must not be added. Evidence is
`estimator-current-delay-field-upload-20261005/`. Field comparisons determine
performance acceptance; host-loaded lab throughput and tails remain diagnostic.

The later unloaded-delay source `97f5509` passes the complete quiet rate-fall
model three times, with no expired-burst failure, but fails 24 default bond
checks; all other non-privileged components and Nix/ARM builds pass. Its
B/C/B field low-upload bounds are 0.144–0.183 / 0.268–0.302 / 0.149–0.184
Mbit/s. Loaded voice p99 is 95/100 / 112/95 / 58/62 ms; whole-phase losses
are 5/5 / 0/0 / 0/0 of 2750 echoes per direction. Late throughput bounds
overlap. Both boot identities, restoration and owned cleanup are verified.
Mobile RX+TX including background is 28.572 MB through cleanup, plus 0.773 MB
separate staging; its 27.715 MB comparison interval is nested. Source
promotion is withheld. Evidence is `estimator-unloaded-delay-field-upload-20261005/`.

### Feedback-round push field result — 2026-10-05

**Observed source:** unaccepted `78127ff3e6d1ffa7c8ed28137aa4cfdb5ac929fc`,
SHA-256 `c6258a390e8d9b58a2857b9dfc7345393d24a3286bcaf97c8beb2f5e10e8a7de`.
One timer-backed 400 kbit/s to 2 Mbit/s B/C/B upload comparison completes:

| Observed metric | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-window upload bounds, Mbit/s | 0.133–0.161 | 0.200–0.252 | 0.149–0.179 |
| Late-window upload bounds, Mbit/s | 1.092–1.603 | 1.133–1.751 | 0.290–0.434 |
| TCP-active voice RTT p99, edge/hub ms | 74.3/81.1 | 127.5/134.6 | 58.2/68.4 |
| Whole-phase voice RTT p99, edge/hub ms | 76.6/80.2 | 119.2/111.8 | 58.6/66.0 |
| Missing echoes of 2750, edge/hub | 0/1 | 0/0 | 0/0 |
| Whole-phase maximum receive gap, edge/hub ms | 80.0/107.3 | 112.2/101.2 | 89.5/95.4 |

Candidate low-upload bounds exceed both baseline bounds. Late bounds overlap
the first baseline and exceed the collapsed return baseline. Loaded and
whole-phase p99 worsen in both directions against both baselines; the source
is not promoted. Bounds use whole receiver reports, not an exact adaptation
deadline. The return baseline still submits 163 bulk originals on 5G in a
guarded late 4.8-second window, compared with 615 before and 628 in the
candidate; its cause is unknown, not a claim of complete lane abandonment.

Immediate direct references on the different OCI route are Starlink
0.493/0.503/0.505 Mbit/s and 5G 2.897/2.077/2.950 under 1/3 Mbit/s offers.
Near-offer samples are service lower bounds; the candidate's 5G reference is
below its offer, so physical conditions are not matched. Idle ICMP to the
concentrator itself has Starlink p99 41.6/45.9/49.0 ms and 5G
137.0/54.6/103.0 ms, with 100 replies per sample. These adjacent samples
do not establish UDP service throughout the loaded phase or isolate the
policy's contribution to its worse field tail.

Candidate local real-time queue residence p99 is at most 1 ms on each host,
with no stale drops; guarded expired-original deltas are 14/0, versus 4/0
before and 1/0 after. Expiry alone does not prove non-delivery. Candidate
low-window TBF backlog/rate p99 is 165 ms, a service-time proxy rather than
measured packet wait, with five drops; the high window has four drops.
Both boot identities stay unchanged and all phase cleanup errors are empty.
Independent checks before and after owned runtime cleanup verify deployed
b444920 source/hashes, original `auto` policy, empty overrides, unshaped
queues and absence of temporary firewall rules/timers. Mobile RX+TX including
background is 25.919 MB during comparison, 26.622 MB through cleanup
(overlapping), plus 0.779 MB separate staging. Evidence is
`estimator-round-push-field-upload-20261005/` under the adaptive evidence root.

Two further local diagnostics are rejected and reverted, never field
activated. Limiting the extended push to its preceding excess-volume budget
regresses cold discovery to 1.85 Mbit/s and leaves primed upload at 2400 B/s.
Removing optional copies with earlier real-time gap repair and class-preserving
repair delivers 5500/5500 but 18 late beyond 150 ms; buffered rate-fall voice
p99 is 369 ms and primed upload 25200 B/s. Each repeats three times.
Evidence is `stage23-round-length-volume-budget-{diagnostic.txt,rejected.patch}`
and `stage23-realtime-gap-no-optional-copy-{diagnostic.txt,rejected.patch}`.

**Intended next check:** derive lower-class flight allowance from the same
queue budget used for voice, replacing the special pre-push class-window
rule, and preserve both delivery and capacity-discovery outcomes before any
new field comparison. This is a hypothesis about the field tail, not its
established cause. `b444920` / `v0.0.3` remains the baseline; all outstanding
model/lab gates stay visible. Field measurements determine performance gains;
local host CPU load cannot establish or refute one.

### Rejected lower-class flight diagnostics — 2026-10-05

**Observed in deterministic virtual time:** the feedback-round source fails
slow-lane voice isolation (38 ms one-way p99 versus the existing 25 ms gate)
and cold standby delivery (90/100 versus at least 97), identically three times.
A queue-only lower-class flight cap fixes buffered rate-fall expiry and gives
52/57 ms primed-upload voice RTT p99, but single-lane bulk discovery falls to
12000 B/s. It fails 24 default bond cases against 18 before; build/vet,
formatting and other Go packages pass. This is rejected, not a field candidate.

Retaining propagation and ACK cadence restores fast two-lane sharing to
802880 B/s but leaves single-lane service at 108000 B/s. Removing the pre-push
holding cap then restores single-lane bulk to 467120/5440800 B/s while raising
voice one-way p99 to 99/94 ms, still failing its unchanged gates. Limiting push
excess while voice reserves capacity gives 5085040 B/s at 34 ms on the fast
single lane, but the slow case stays at 120000 B/s and buffered expiry still
bursts by four. Each diagnostic repeats three times; all are archived and
reverted, with no field activation. None proves an improvement across metrics.

**Inference from code and these counterexamples:** propagation flight cannot
be treated as queue occupancy, while retaining the previous holding flight
allowance can prevent a capacity push from demonstrating more service. Removing
that allowance alone sacrifices voice delay. This does not establish which
mechanism caused the measured field tail or justify another scalar cap.
**Intended next investigation:** reproduce capacity qualification and record
per-lane rejection of fresh real-time submissions, preserving existing outcome
gates, before choosing another policy replacement. Field comparisons remain
the performance reference and installed `b444920` remains the baseline.
Evidence under `/srv/nvme/tmp/wanbond-adaptive-evidence` is
`stage23-realtime-flight-budget-{red,green-attempt,sharing-and-upgrade,go-gate}.txt`,
`stage23-realtime-flight-budget-rejected.patch`,
`stage23-propagation-and-queue-flight-budget-{diagnostic.txt,rejected.patch}`,
`stage23-propagation-flight-without-holding-clamp-{diagnostic.txt,rejected.patch}`,
and `stage23-voice-queue-budget-push-{diagnostic.txt,rejected.patch}`.

### Delivery-qualified delay field rejection — 2026-10-05

**Observed:** source `2d48b75` (code `07368d4`, binary SHA-256
`e06c1d5a9e1a452b847f400472577f27578e643c98d49d3ca12964de03c663dd`)
completed C8/candidate/C8 on stable boots. Low-upload bounds overlap:
0.139–0.171 / 0.140–0.207 / 0.156–0.190 Mbit/s. Late bounds overlap:
1.081–1.597 / 0.914–1.402 / 0.910–1.221 Mbit/s. No clear throughput
improvement is established. Loaded voice p99 is 55.76/55.30 →
119.43/144.91 → 65.66/63.44 ms (edge/hub); all phases receive 2750/2750
echoes per host. Promotion is rejected.

Immediate direct offered-rate tests give Starlink 0.505/0.500/0.512 Mbit/s
and 5G 2.976/2.950/2.973 Mbit/s. Exact-route idle ICMP p99 is
61.0/59.5/42.2 ms on Starlink and 182/67/77 ms on 5G, over 100/101
observed replies; raw packet summaries retain missing replies. These do not
establish equal loaded physical service. Candidate late first-submission
counters place 478 voice originals on 5G versus zero before and three after;
that association does not prove the tail's cause. Local real-time residence
p99 is bounded by 10/1 ms, with no stale drops.

The supplied-rate qualification reproduction passes after the replacement,
and primed upload improves in the model, but the default gate has 20 bond
failures versus 18 before. A separate traced model shows a new ranking
regression: sender and receiver averages differ by 0.0012 B/s at 13 s,
keeping the better lane's delay unchanged for over eight seconds.
**Inferred:** comparing unmatched intervals can preserve a false delivery
deficit after a real delay improvement. Replacing those interval observations
with coherent, aged evidence remains necessary; this trial is not accepted.

Evidence: `estimator-delivery-qualified-field-upload-20261005`,
`stage23-delivery-qualified-delay-full-nonprivileged-gate.txt`, and
`stage23-delivery-qualification-rank-trace.{txt,json}` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. Native Nix and ARM builds pass;
full scenario and VM profile gates are not proved. Both deployed C8 hashes,
original `raspi5l` policy, empty runtime overrides/timers and clean qdiscs
are independently verified before and after owned artifact removal. Mobile
RX+TX is 26.226 MB for comparison, 26.814 MB through cleanup (nested), plus
1.011 MB staging, including background traffic rather than SIM billing.

**Intended next field isolation:** put only fresh-small-before-repair
scheduling onto C8, including main's passive queue telemetry. This avoids
combining the scheduling correction with the unaccepted estimator rewrite.

### C8 fresh-small priority repeat and TCP tradeoff — 2026-10-05, 23:05 UTC

**Observed:** source `a712bbc` (code `2e88765`, executable SHA-256
`fcf037e91d352c8ed58c33463190fe784ba92e9bf0627a3cceb6310590d80f76`)
repeats the scheduling change from `388a6d6`; those production diffs are
identical. This is another field observation of that policy, not a new
algorithm. The full non-privileged gate and native/ARM Nix builds pass.

The voice-only C8/candidate/C8 sequence completes on stable boots. Each host
sends 2750 echoes per phase, with separate ten-second edge-egress blocks
of only wanbond UDP on 5G and Starlink. Whole-stream measurements are:

| Measurement, edge/hub | C8 before | Candidate | C8 after |
|---|---:|---:|---:|
| Missing echoes | 48/45 | 5/7 | 6/6 |
| RTT p99, ms | 139.14/143.60 | 114.50/130.19 | 145.48/137.31 |
| Maximum arrival gap, ms | 189.08/132.65 | 137.95/128.25 | 133.30/234.53 |
| Maximum consecutive missing | 4/4 | 3/5 | 5/2 |

Candidate p99 is lower than both baselines, but loss is similar to the
return baseline and five consecutive missing echoes still fail continuity.
Immediate direct upload references are Starlink 0.489/0.519/0.506 Mbit/s
at a 1 Mbit/s offer and 5G 2.871/2.946/2.956 Mbit/s at a 3 Mbit/s offer.
Exact-route idle ICMP p99 is 57.8/48.6/57.8 ms on Starlink and
97.6/184.0/174.0 ms on 5G. Every sample has 100 observed replies; raw
transmit/receive summaries retain loss. These references do not prove equal
loaded service or isolate the policy's contribution. There is no bulk
workload in this set. Candidate local voice residence p99 is bounded by
50 ms on both hosts; guarded edge drops are three deadline and nine stale,
and hub drops are zero. Aggregate drops do not identify missing echoes.

**Observed:** matched virtual-time scenarios 1a–1c repeat three times with
identical verdicts. Fresh-small priority improves radio lane-0, direction-0
one-way blackout from failure to pass, but changes two baseline passes to
failures: bidirectional blackout and direction-1 one-way blackout, both with
bulk. The former has zero TCP delivery in seconds 22 and 28, the latter in
second 21. Voice improves, while some bulk progress worsens. A diagnostic
that moves only real-time originals first also fails the selected bulk
gates. TCP-state traces show holes persisting until retransmission timeouts;
**inferred:** whole-class priority alone does not resolve the recovery tradeoff.
No scenario threshold is changed and this policy is not promoted.

Both deployed C8 hashes, original `raspi5l` policy, empty runtime overrides
and timers, and clean queues/firewall are independently verified before
and after owned artifact removal. Mobile RX+TX is 16.292 MB for comparison,
16.965 MB through cleanup (nested), plus 0.772 MB staging, including
background traffic rather than SIM billing. Evidence under
`/srv/nvme/tmp/wanbond-adaptive-evidence` is
`c8-fresh-voice-field-blackout-20261005/`,
`c8-fresh-voice-outage-{baseline.txt,candidate.txt,comparison.json}`,
`c8-realtime-only-selected-outages-diagnostic.txt`, and
`c8-priority-tcp-trace/`. Installed `b444920` remains the accepted baseline.

### Matching-interval delay diagnostic rejected — 2026-10-05, 23:16 UTC

**Observed:** the delivered-rate qualification in prototype `2d48b75` fails
the existing persistent lane-improvement outcome three times: neither sender
places any of its 100 checked originals on the better lane. A Go overlay
replaces its unmatched sender/receiver rate averages with intervals ending
at the same physical acknowledgement, using acknowledged sender bytes and
send times, receiver bytes and receipt elapsed time. It evaluates the current
interval before qualifying delay and adds no threshold, constant or wire field.

That reproduction passes three times, as do the late-receipt, delivery-deficit
and busy-radio checks. Primed upload delivers 50400 B/s against the 48751 B/s
requirement. Buffered rate-fall delivery is 520800 B/s against 546092 B/s
available service, with voice RTT p99 122 ms and no missing echoes, but five
expirations still fail the burst gate. Slow-lane rate increase remains
38760 B/s against 417458 B/s available service. These are deterministic
model measurements, not field performance.

The complete bond diagnostic has 22 failed tests against 20 on its unchanged
predecessor. Three previous failures pass; five previously passing outcomes
fail, including sparse delay, forward/reverse noise service, single-lane
voice, fast-pair voice and voice's preferred lane under load. The choice is
rejected and never field-activated. **Inference:** correcting interval
provenance alone does not establish sound unloaded-delay qualification.
Next, reproduce that qualification with physically paced traffic and
packet-time evidence before choosing another estimator replacement.
The preceding per-ACK and quiet-at-send diagnostics are also unaccepted.
Evidence is `stage23-coherent-rate-window-{delay-diagnostic.txt,
capacity-and-voice-diagnostic.txt,bond-diagnostic-gate.txt,
bond-diagnostic-comparison.json,diagnostic.patch}` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. No production policy changes.

### C8 demand-step experiment — 2026-10-06

**Observed:** a real-time demand increase on a loss-free modeled lane loses
fresh datagrams because the startup pacing ceiling follows recent sparse
delivery. Removing that ceiling fixes all five deterministic cases, three
runs, but an existing jitter-only gate fails (137 ms p99 versus 130 ms).
The isolated candidate `f2ea16b` delivers zero lost echoes and zero local
queue drops in a complete field C8/candidate/C8 set; C8 loses 5/4 and 7/3
edge/concentrator echoes. Candidate mobile traffic rises 39–41%, receive
gaps exceed 150 ms and tail latency has no consistent gain. It remains
unaccepted; both hosts are independently verified back on C8. No wire or
estimator change was made. The plan's “C8 demand-step rule removal” entry
records the complete metrics, builds, rejected sampling diagnostics and
next capacity/redundancy work; raw evidence is under
`/srv/nvme/tmp/wanbond-adaptive-evidence/c8-demand-step-field-blackout-20261006`.

**Observed follow-up:** direct ICMP idle p99 in the demand-step field set is
Starlink 57.6/37.6/42.0 ms and 5G 170/71/96.5 ms, before/during/after the
candidate. Service-rate references alone do not establish stable latency;
the lower candidate median/p95 cannot be attributed solely to policy. Raw
ping reports retain transmitted/received counts and loss. Field scheduler
observer maxima are 1.61/3.41/24.68 ms on the edge and 0.85/4.32/1.32 ms on
the concentrator; these are observations, not estimates of network delay.

Before changing ordinary-copy policy, `TestVoiceCopiesHaveRoomOnTheOtherLane`
is restated as `TestVoiceLossDoesNotExceedLatencyBudget` in a separate test
commit. It retains at least 99.9% delivery and at most 0.1% later than
150 ms; only the requirement for at least 95% copied datagrams is removed.
Before and after, three virtual-time runs deliver 5,500/5,500, none later
than 150 ms, maximum 110 ms. Copy counts remain diagnostics. No production
copy behavior changes in this commit.

### Ordinary-copy removal remains insufficient — 2026-10-06, 01:00 IST

**Observed diagnostic:** with the demand-step ceiling removed, disabling the
ordinary copy budget while retaining suspect-lane fallback delivers
5,499/5,500 in the existing 0.4%-loss voice/bulk model, but 19 arrive later
than 150 ms (maximum 247 ms). The unchanged outcome gate allows at most
0.1% late. Giving normal retries their original traffic priority produces
5,500/5,500 but 20 late, maximum 279 ms. Sending an alternate first attempt
when a later physical packet is confirmed and this packet has waited one
measured RTT produces 5,500/5,500 but 16 late, maximum 232 ms. Every result
repeats three times. Demand-step, suspect-fallback and eight-seed takeover
checks pass in these selected runs. None of these diagnostics is shipped or
field-tested. Logs/overlays and `diagnostic-summary.json` are under
`/srv/nvme/tmp/wanbond-adaptive-evidence/c8-voice-demand-step-repro/fallback-only`.

**Inferred next constraint:** ordinary-copy removal needs a better bounded
fallback decision; neither liveness alone, retry priority, nor later-packet
confirmation plus mean RTT meets the retained voice outcome. Preserve these
counterexamples when replacing acknowledgement timing and capacity policy.
Main's non-privileged gate and Nix build pass at `19738ec`; production policy
remains C8, field hosts restored, and the improvement goal remains active.

### Conservative capacity cohort field trial — 2026-10-06

An isolated sampler uses the highest conservative delivery bound across
immutable ACK-prefix cohorts in the existing eight-round history. Idle or
sender-limited boundaries and capacity revision clear it; the immediate
cohort remains available for short pushes. It adds no wire field or constant.
The held-batch model reproduction passes three times, but the default gate
still has 20 bond failures. The capped C8/candidate/C8 field comparison
improves early upload goodput bounds to 0.183–0.220 Mbit/s from
0.134–0.159 and 0.154–0.170, while loaded voice p99 increases to
105.55/119.07 ms against 88.82/59.98 and 60.40/57.40 ms (edge/hub).
Late upload bounds overlap. All voice echoes arrive; local candidate voice
residence p99 is bounded by 1 ms. These are observed field tradeoffs, not a
policy acceptance. Both C8 services and network state are restored and
verified; mobile traffic is 27.154 MB through cleanup plus 0.874 MB staging.
The plan's conservative-cohort field record retains sources, uncertainty,
failed diagnostics and the next bounded-push experiment. Main policy remains C8.

### Held-service flight-window field trial — 2026-10-06

Source `4406761` sizes the flight window from held service during a push.
The deterministic slow steady-path outcome passes three times; fast queueing
and new jitter/service failures remain. Full non-privileged checks fail on
20 bond tests, with all other components passing; native Nix/ARM builds pass.
The capped field C8/candidate phases improve early upload bounds from
0.153–0.175 to 0.230–0.298 Mbit/s, while loaded voice p99 rises from
66.22/86.71 to 141.10/136.81 ms. All 2750 echoes arrive on each host.
The return baseline is held by the traffic guard, so the comparison remains
incomplete; high-rate subperiod improvements are not acceptance evidence.
Both C8 services and network state are independently verified restored and
owned artifacts cleaned. Mobile RX+TX is 22.168 MB through cleanup plus
0.779 MB staging, including background. Evidence and limitations are in the
plan's held-service record and `observed-service-window-field-20261006` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. No VM profile gate is claimed from
these field or deterministic measurements. Main policy remains C8.


### Observed ACK cadence completed field comparison — 2026-10-06

Source `d1b3f74` completes one capped C8/candidate/C8 set with immediate
physical references on stable boots. Early upload bounds are
0.130–0.177 / 0.283–0.335 / 0.149–0.184 Mbit/s; loaded edge/hub voice p99 is
53.01/50.48 / 84.07/75.31 / 50.72/44.43 ms. All 2750 echoes arrive per host
in every phase. Early goodput improves, voice tails regress and late bounds
overlap; no promotion or three-run field proof. The default gate fails on
22 bond tests, with two prior failures resolved and four new failures;
all other components and Nix/ARM builds pass. Selected steady-path gates
passing do not substitute for profile gates. The separate cadence-age
correction passes selected checks and has no field result. Both C8 services,
network state and owned cleanup are independently verified. Mobile RX+TX is
27.769 MB through cleanup plus 0.777 MB staging, including background.
See the plan's observed-cadence field record and
`observed-ack-window-field-20261006` under the evidence root for limitations.


### Cadence-age field and model results — 2026-10-06

The cadence-age follow-up `1366136` passes its failing idle-history outcome
three times and restores queued-receipt, slow-voice and ACK-sharing outcomes;
19 full default bond failures remain. Other non-privileged components and
Nix/ARM builds pass. A complete timer-backed B/C/B field comparison records
early upload bounds 0.220–0.264 / 0.204–0.268 / 0.140–0.176 Mbit/s and late
bounds 0.171–0.267 / 0.876–1.438 / 1.106–1.655. Loaded voice p99 is
97.5/122.3 / 130.8/152.0 / 59.3/59.0 ms edge/hub; all 2750 echoes per
direction arrive. The source is not promoted. Immediate RF references,
receiver-bound provenance and the preceding baseline's zero late 5G bulk
submissions are retained; causal attribution remains unknown.

Both C8 source/hashes, boots, policy, unshaped queues and cleanup are
independently verified. Disjoint measured mobile staging/cleanup intervals
total 27.333 MB with background. Evidence is
`fresh-ack-cadence-field-20261006/` and `ack-cadence-age-*.txt` under the
evidence root; full detail is in the adaptive-policy plan. No three-run
profile gate, completed stage or across-metric improvement is inferred.


### Physical batching finding — 2026-10-06

The unchanged new batching test passes original production `f75668e` three
times at 25/50/100 ms batching (5.412/5.403/5.227 MB/s, 750/750 voice,
70/90/140 ms one-way p99). Production and the existing model harness are
unchanged; the new test is supplied by overlay. Prototype 100 ms bulk is
2.913 MB/s, below 75% of modeled wire service. The combined ACK-volume/short
push source `d09b0b7` restores 5.048 MB/s at 120 ms and improves steady
queue/utilization, but discovery, radio service and isolation still fail.
No profile, stage or field gate is inferred. Exact logs and rejected source
diagnostics remain under the evidence root and in the adaptive-policy plan.


### Qualified ACK-volume field rejection — 2026-10-06

Exact source `d09b0b7` has 19 full bond failures; other non-privileged
components, native Nix and ARM builds pass. Its complete capped B/C/B field
comparison improves early upload bounds to 0.196–0.260 Mbit/s versus
0.147–0.182 and 0.132–0.175. Late bounds overlap both baselines. Loaded
voice p99 is 47.4/45.7 → 81.6/152.9 → 149.9/50.7 ms edge/hub, with all
2750 echoes each delivered. Low-rate TBF service-time p99 worsens, despite
a smaller high-rate backlog tail. The source is not promoted. Adjacent RF
references vary; no causal attribution or model/lab acceptance is inferred.
Both hosts are independently verified restored to C8, with owned runtime
files archived/removed. Measured disjoint mobile intervals total 29.831 MB
including background. Full gates, references and limits remain in the plan
and `qualified-aggregation-field-20261006/` under the evidence root.


### Current physical RTT field rejection — 2026-10-06

Exact source `4e036a9` has seventeen full bond failures, two fewer than
the preceding experiment and no new failure. Other non-privileged components,
native Nix and ARM builds pass. Its original-baseline RTT-age reproduction
and fourteen selected corrected-source outcomes are recorded in the plan.
A complete capped B/C/B comparison improves early upload to 0.214–0.261
Mbit/s versus 0.122–0.158 and 0.142–0.178, but loaded voice p99 is
82.2/53.2 → 157.3/131.6 → 48.4/44.3 ms edge/hub. All 2750 echoes per
host/phase arrive. Late bounds overlap; low-rate backlog tail worsens.
The source is not promoted. Adjacent RF references and first-submission
counters do not establish causation, and no VM/profile gate is inferred.
Both hosts are independently verified restored to C8 before/after owned
cleanup. Disjoint metered intervals total 29.536 MB including background.
Evidence is `current-rank-inputs-field-20261006/` under the adaptive evidence
root, with source/build checks and precise limits in the adaptive-policy plan.
