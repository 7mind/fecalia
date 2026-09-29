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
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive
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

## Scenarios and gates

| Scenario | Conditions | Pass criteria |
| --- | --- | --- |
| `calibrate.py` | Plain TCP and 1300-byte UDP, both WANs concurrently, both directions | Each WAN delivers at least 85% of its configured rate |
| `benchmark.py` | One TCP flow, then its reverse; default 2+6 Mbit/s, 15/25 ms one-way delay | Each direction reaches 75% of combined wire capacity; both WAN byte counters advance by over 100 kB |
| `udp.py` | Constant-rate UDP of full 1311-byte datagrams, then its reverse, offered at the rate full datagrams could carry (86.6% of wire capacity) | Each direction delivers 80% of combined wire capacity after a 10-second warmup; both WAN byte counters advance by over 100 kB |
| `profiles/asymmetric.json` | 6+2 Mbit/s uplink, 1+7 downlink, 3 ms jitter | Same throughput gates; independent directional capacity estimates |
| `profiles/fast.json` | 32+96 Mbit/s in each direction | Same throughput gates; calibrate this profile before interpreting results |
| `profiles/jitter.json` | 2+6 Mbit/s, 15/40 ms delay, 4/10 ms jitter | Same throughput gates, including a 30-second idle period before load |
| `profiles/mobile.json` | 0.4+1.25 Mbit/s uplink, 0.5+100 downlink, 4/10 ms jitter | Same throughput gates; stress model for standby Starlink and asymmetric LTE |
| `profiles/radio.json` | Mobile capacities; Starlink 20±10 ms delay and 0.4% loss, LTE 40±30 ms delay, independently in each direction | Same throughput gates after 60 seconds idle; reproduces the collapse missed by milder jitter |
| `continuity.py` | Simultaneous TCP in both directions plus two 50 Hz, 160-byte UDP echo streams | TCP completes 65 seconds and meets the progress rule below; each UDP stream has <1% loss and meets the gap and latency rules below (`continuity_gates.py`) |

Continuity accepts `--profile`, defaulting to `profiles/basic.json`. At 15
seconds WAN1 is capped at 0.5 Mbit/s in each direction; lower profile rates are
preserved. At 25 seconds it loses all packets, and at 30 seconds its profile
conditions are restored. WAN2 loses all packets from 40–45 seconds. WAN1 has
at least 1% random loss after 55 seconds. Directional delay and jitter remain
as configured. The phase manifest records actual application times and each
direction's conditions. Echo traffic exercises both directions;
RTT includes forward and return delay.

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
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/fast.json --warmup 15
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/asymmetric.json
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/jitter.json --idle-seconds 30
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/mobile.json --warmup 10
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
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
scheduling is nondeterministic even with fixed random seeds. TCP calibration
alone is insufficient because segmentation offload changes queue occupancy;
the UDP calibration verifies packet-rate capacity as well.

## Inspection and artifacts

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
