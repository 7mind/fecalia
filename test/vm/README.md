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
| `profiles/asymmetric.json` | 6+2 Mbit/s uplink, 1+7 downlink, 3 ms jitter | Same throughput gates; independent directional capacity estimates |
| `profiles/fast.json` | 32+96 Mbit/s in each direction | Same throughput gates; calibrate this profile before interpreting results |
| `profiles/jitter.json` | 2+6 Mbit/s, 15/40 ms delay, 4/10 ms jitter | Same throughput gates, including a 30-second idle period before load |
| `profiles/mobile.json` | 0.4+1.25 Mbit/s uplink, 0.5+100 downlink, 4/10 ms jitter | Same throughput gates; stress model for standby Starlink and asymmetric LTE |
| `profiles/radio.json` | Mobile capacities; Starlink 20±10 ms delay and 0.4% loss, LTE 40±30 ms delay, independently in each direction | Same throughput gates after 60 seconds idle; reproduces the collapse missed by milder jitter |
| `continuity.py` | Simultaneous TCP in both directions plus two 50 Hz, 160-byte UDP echo streams | TCP completes 65 seconds; each receiver reports progress in every measured outage interval with 1 KiB application blocks; each UDP stream has <1% loss, <150 ms maximum receive gap and <150 ms p99 RTT |

Continuity accepts `--profile`, defaulting to `profiles/basic.json`. At 15
seconds WAN1 is capped at 0.5 Mbit/s in each direction; lower profile rates are
preserved. At 25 seconds it loses all packets, and at 30 seconds its profile
conditions are restored. WAN2 loses all packets from 40–45 seconds. WAN1 has
at least 1% random loss after 55 seconds. Directional delay and jitter remain
as configured. The phase manifest records actual application times and each
direction's conditions. Echo traffic exercises both directions;
RTT includes forward and return delay.

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
