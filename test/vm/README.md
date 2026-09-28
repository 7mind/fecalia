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
| `continuity.py` | Simultaneous TCP in both directions plus two 50 Hz, 160-byte UDP echo streams | TCP completes 65 seconds and advances in every measured outage interval; each UDP stream has <1% loss, <150 ms maximum receive gap and <150 ms p99 RTT |

Continuity phases: at 15 seconds WAN1 falls to 0.5 Mbit/s, at 25 seconds it
loses all packets, at 30 seconds it returns at 2 Mbit/s. WAN2 loses all packets
from 40–45 seconds. WAN1 gains 1% random loss at 55 seconds. The phase manifest
records actual application times. Echo traffic exercises both directions;
RTT includes forward and return delay.

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
without a connection reset, but one measured one-second outage interval made
no progress, failing the stricter TCP progress gate.

An earlier correction passed the 2+6 Mbit/s jitter profile after 30 seconds
idle at 6.437/6.449 Mbit/s (`20260928-210003-adaptive`); the final small-packet
replication budget and receive coalescing refinement were added afterwards.
Mobile calibration measured 94.58 Mbit/s direct LTE TCP downlink and all UDP
wire-rate checks passed (`20260928-202421-calibration`). The calibration's
overall TCP gate failed on the slow uplink, so it is not an overall pass.

Remaining work: slow convergence and download headroom on the mobile profile,
investigating repair/expiry counters without injected loss, and the occasional
one-second TCP pause during a link outage. These VM measurements do not
establish production throughput. Deploy the correction to both ends before
comparing real links.
