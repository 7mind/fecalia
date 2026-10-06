# wanbond design

This document explains wanbond's architecture and, specifically, **exactly what
we built on top of the amneziawg-go WireGuard engine**. For setup and operation
see [install.md](install.md); for the front-door overview see the
[README](../README.md).

## Thesis

> Put **all bonding logic** in a custom `conn.Bind` beneath WireGuard, operating
> only on opaque, already-encrypted datagrams. Keep engine changes limited to
> correctness patches that do not couple the engine to wanbond.

We embed [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) as a library
for TUN management, the Noise handshake, AEAD encryption, key rotation (rekey),
endpoint roaming, and keepalives. The local `third_party/amneziawg-go` source is
the AmneziaWG Go engine `v3.1.20260828` (module `github.com/amnezia-vpn/amneziawg-go/v3`,
commit `b5928ef`) plus engine-generic local patches:

- `conn.BindPacketSender`: local per-datagram flow and pure-TCP-ACK metadata,
  classified before encryption and handed to the Bind with each send batch.
  Metadata never enters the wire format.
- Outbound pipeline statistics (`OutboundStats`): TUN and send batch
  histograms, queue depths and active-send gauges.
- v3 chooses transport padding (S4 prefix, content padding, random trailers,
  16-byte rounding); the patch makes that choice at staging, in staging order
  (`transportPaddingSize`), instead of in the encryption workers.
- The unmerged upstream PR #169 correction for issue #168: a packet read while
  a UAPI update changed S4 is re-based to the current transport padding.
- The one-line upstream #157 test fix so `go vet ./device/...` remains a valid gate.
- The RFC 6479 anti-replay window (`replay.ringBlocks`) is 131008 messages
  instead of 8128: real-time datagrams are delivered ahead of bulk the
  resequencer still holds for repair, so counters arrive out of order by as
  many datagrams as are held.

v3 keeps message headers, paddings and junk parameters in per-`Device` atomics,
so the former v1.0.4 per-device protocol-state and junk-PRNG patches are no
longer needed; their isolation and concurrent-junk regressions remain in
`device/protocol_state_test.go`. wanbond renders only the v1-compatible
Amnezia keys (`jc`, `jmin`, `jmax`, `s1`, `s2`, `h1`–`h4`); v3-only settings
(S3/S4, header ranges, header protection, content padding, timings, random
trailers, disabled cookies) stay at their engine defaults. Everything wanbond-
specific — the multipath transport, outer-frame obfuscation and
authentication, receive resequencing, and per-path telemetry — remains in the
engine's `conn.Bind` transport implementation.

This gives a clean separation: WireGuard owns confidentiality, integrity, and
authenticity of the *payload*; wanbond owns *delivery* across multiple lossy
paths, plus outer obfuscation. The Bind never inspects plaintext — it moves
opaque ciphertext datagrams.

## Why amneziawg-go (and not plain wireguard-go)

The architecture decision is closed: amneziawg-go over plain wireguard-go,
kcp-go, or quic-go. The reason is **DPI resistance** (requirement 6). AmneziaWG
adds configurable obfuscation to the WireGuard wire — junk packets (`jc`, `jmin`,
`jmax`), handshake junk prefixes (`s1`, `s2`), and custom magic headers
(`h1`–`h4`) — as *defense-in-depth* beneath our own outer obfuscation codec.
Using the engine as-is means WireGuard's battle-tested crypto and roaming come
for free while the obfuscation knobs are available when configured.

**Fork-lag hedge.** amneziawg-go is a fork of wireguard-go and can lag upstream
security/perf fixes. We contain that risk: the engine's `conn` transport
interfaces enter through **one file**, `internal/bind/bind.go`, via type
aliases (`Bind = conn.Bind`, `Endpoint = conn.Endpoint`,
`ReceiveFunc = conn.ReceiveFunc`) and the flow-metadata contract
(`BindPacketSender`, `PacketMetadata`, `FlowID`, `TCPACK`).
`internal/bind/multipath.go` references only `conn` constants and sentinel
errors (`IdealBatchSize`, `ErrBindAlreadyOpen`, `ErrWrongEndpointType`);
`internal/device` also calls the patched `device` observability API
(`OutboundStats`). The local source patch is engine-generic and
covered by the root multi-device race regression, the nested concurrent-junk
race regression, and the nested module's `device/...` tests. The `replace`
remains until upstream provides the flow-metadata and statistics contracts, or
wanbond stops requiring them. The base `conn.Bind`/`conn.Endpoint` contracts
match wireguard-go, but swapping back to it (dropping obfuscation) also
requires porting or retiring the local metadata and statistics patches.

## The data path

```
                          EDGE                                     CONCENTRATOR
   ┌───────────────────────────────────────┐      ┌───────────────────────────────────────┐
   │  applications / kernel routing         │      │       kernel routing / NAT onward      │
   │                 │                       │      │                 ▲                       │
   │           ┌─────▼──────┐  TUN           │      │           ┌─────┴──────┐  TUN           │
   │           │ WireGuard  │  (amneziawg-go)│      │           │ WireGuard  │                │
   │           │  engine    │  Noise/AEAD    │      │           │  engine    │                │
   │           └─────┬──────┘  rekey/roam    │      │           └─────▲──────┘                │
   │   opaque encrypted datagrams  │         │      │       opaque encrypted datagrams        │
   │           ┌─────▼──────────────────────┐│      │┌──────────────────────┴─────┐          │
   │           │   wanbond conn.Bind        ││      ││   wanbond conn.Bind        │          │
   │           │  ┌───────────────────────┐ ││      ││  ┌───────────────────────┐ │          │
   │  send ───►│  │ transport (bond)      │ ││      ││  │ resequencer (reseq)   │ │──► recv  │
   │           │  │ frame codec (frame)   │ ││      ││  │ transport (bond)      │ │          │
   │           │  │ probes (telemetry)    │ ││      ││  │ frame codec (frame)   │ │          │
   │           │  └──────────┬────────────┘ ││      ││  └──────────▲────────────┘ │          │
   │           │   per-path UDP sockets      ││      ││   per-path UDP sockets     │          │
   │           └──────┬───────────┬─────────┘│      │└──────▲───────────▲─────────┘          │
   └──────────────────┼───────────┼──────────┘      └───────┼───────────┼──────────────────┘
              starlink│    cellular│   ══════ real internet ══════│  path A    │ path B
```

**Send**: the engine hands the Bind a batch of opaque encrypted datagrams with
their local flow metadata → the owning peer's **transport** queues each by
traffic class and, on its next poll, chooses a lane and wraps the datagram in
a data frame → the **frame codec** obfuscates and authenticates it → it leaves
on that lane's per-path UDP socket.

**Receive**: frames arrive on the per-path sockets → the **frame codec**
verifies and de-obfuscates them → PROBE frames go to the telemetry plane,
CONTROL frames to the owning peer's **transport**, which deduplicates them,
records receipts for acknowledgement and yields the datagram → bulk passes
through the **resequencer**, small datagrams are delivered at once → the
opaque datagram is delivered up to the engine as if from one endpoint.

Both ends run the same Bind; the diagram shows the dominant direction per role.

## What we built — layer by layer

Each bullet names the package (`internal/…`) that owns it.

### Outer frame codec — `internal/frame`

Defines the wire format of every datagram wanbond sends. Layout: a fresh
24-byte XChaCha20 nonce, then the **obfuscated** body (`kind` byte ‖ per-kind
header ‖ opaque payload), then a 16-byte truncated HMAC-SHA256 tag over the
nonce and the obfuscated body (encrypt-then-MAC). The obfuscation and
authentication subkeys are derived from the PSK with HKDF-SHA256 under
distinct labels. Every frame is authenticated; `Decode` rejects a frame whose
tag does not verify. Frame kinds:

- **PROBE** (kind 3) — telemetry/liveness. Carries the sender's path id, a
  monotonic `ProbeSeq`, a timestamp, the sender's per-boot session id and a
  challenge (see *Per-path telemetry*). An unpadded PROBE may carry a payload;
  the transport's 22-byte hello rides there. A PROBE may instead be **padded**
  to a target on-wire datagram size (a `Padded` flag riding in the echo/flags
  byte plus a `PadLen` count of trailing zero bytes): the reflector echoes the
  same size, so a fresh echo confirms *a datagram of N outer bytes traverses
  this path* (path-MTU probing). Padding reuses the same authenticated
  probe/echo channel and anti-replay — it is not a parallel plane. A padded
  probe carries no payload.
- **CONTROL** (kind 4) — a control-type byte, a MAC-covered `Seq`, and a
  payload. The transport's data (`0xa1`) and acknowledgement (`0xa2`)
  datagrams travel in it, with epoch-scoped per-lane replay windows (below).
  `frame.ControlOverhead` is 50 bytes: nonce 24 + kind 1 + type 1 + seq 8 +
  tag 16.

Kinds 1 and 2 were the unauthenticated DATA and PARITY frames of the removed
transports. `Decode` rejects them as `frame.ErrMalformed`; the values must not
be reassigned.

DPI resistance comes from here: the nonce randomizes every frame, the body is
XChaCha20-obfuscated, and there are **no magic bytes or fixed offsets** — the
wire is high-entropy UDP indistinguishable from noise (verified by
`internal/wireaudit` and `TestP5DPI`/`TestWireFormatAudit`). The fixed
per-datagram cost is subtracted from the TUN MTU (see [p1-mtu.md](p1-mtu.md))
so there is no fragmentation.


### Adaptive transport — `internal/bond`

`internal/bond` is the only transport; `[scheduler] policy = "adaptive"` names
it and is the default. `bond.Transport` owns no I/O or goroutines: its
owner supplies authenticated frames, validated paths, monotonic time and calls
`Poll`. `bind/adaptive.go` holds one transport per peer, serializes its state
and performs UDP I/O outside its state mutex; a per-peer goroutine polls it
every millisecond and whenever a send or a receive wakes it. Every call reads
the clock while it holds the transport, so the times the transport is given
follow the order of its calls. A flush that read the clock before it waited for
the transport was stamped earlier than the datagrams received meanwhile; its
acknowledgement then reported a negative time held, and the peer rejected it as
malformed: a quarter of the acknowledgements of a download, and a lane left
unconfirmed for 0.2-0.4 s at a time (production, 2026-10-02;
`TestAdaptiveAcknowledgementIsNotStampedBeforeItsDatagrams`).

The transport enforces that clock precondition: `Path`, `Enqueue`, `Receive`
and `Poll` reject a time earlier than the preceding timed call, before changing
transport state. Equal times are valid. Bind reads the clock under the mutex;
a rejected internal path update or poll is an invariant violation. Receive
rejections are counted by cause (`malformed`, `epoch`, `path`, `lane`, `ack`,
`type`, `time`) in `wanbond_adaptive_rejected_frames_total{peer,cause}`. These
counts cover authenticated CONTROL frames passed to the transport; outer
authentication and route demultiplexing precede them.

Stage 0 introduced diagnostics without changing policy decisions.
Per-lane metrics and `wanbond monitor` report transit floor, whether
it is known, its evidence age, path delay, rank and liveness. The floor is the
receiver-relative transit minimum of the most recently sampled wire-size
bucket: it includes the clock offset and may be negative. Its age is measured
from the sample that set the minimum, rather than the last ACK. Path delay is
still the legacy unloaded round trip; rank is the legacy latency score. This
baseline had live/dead liveness. The C8 policy released as `v0.0.2` uses
live/suspect/dead ACK-progress liveness and exports its evidence age and known
flag. Delay/rank and capacity still use the legacy estimators; the complete
continuously aged link model remains unfinished.

`realtime_original_packets_total` records each lane's first real-time
submissions, excluding copies, repairs and small TCP datagrams. Its
transport-to-metrics reproduction exercises all three exclusions. This
diagnostic distinguishes a moving primary from delivery by an existing copy;
`realtime_original_path_moves_total` counts changes of lane between these
submissions. Neither counter establishes socket delivery; both observe the
transport's selected route. They do not alter scheduling or the wire format.
`bulk_original_packets_total` likewise counts first bulk submissions per lane,
excluding copies, repairs and small datagrams. It distinguishes restored-lane
bulk use from its ongoing ACKs and keepalives.
`received_bulk_packets_total` instead observes authenticated physical bulk
DATA receipts on the destination lane. It counts a repeated datagram arriving
in a new physical attempt, but excludes replayed attempts, small datagrams
and empty keepalives. This proves receipt on a lane; it does not establish
delivery through the inner engine or TCP goodput.
The lab's returning-lane gate uses this receipt counter and brackets each
metrics read from request through completion, including clock uncertainty.
Only a counter increase certainly inside the two-second window proves a
timely receipt. Missing counters, resets and reads crossing the deadline are
inconclusive; original submissions cannot substitute for receipt.

The stage 0 scenario collector records actual voice send times and captures
iperf's test-start events in each guest. Its gate evaluator uses TCP receiver
bytes and actual echoed send stamps, and distinguishes missing evidence from
a pass. Independent phase goodput and idle-latency references are required.
The first 84 baseline collections did not record actual TCP test starts:
iperf's JSON timestamp belongs to connection setup, so their absolute TCP
adaptation deadlines remain inconclusive. These are measurement changes;
the adaptive lane policy has not been replaced yet.
Deadline checks also retain the uncertainty between connection setup and the
captured start, plus guest clock-exchange uncertainty. Insufficient receiver
interval resolution is reported as inconclusive, rather than filling partial
intervals with an assumed arrival rate.
An interval contributes to a deadline pass only when it remains inside the
phase and before the deadline throughout its timestamp uncertainty. Failure
bounds include every interval that could overlap the final second.
Progress checks require certain positive receiver bytes in every required
second; missing intervals and a positive multi-second aggregate cannot prove
that condition. Bulk-continuity checks likewise cannot pass across missing
receiver coverage. The collector now requests 100 ms TCP reports on both
peers and reuses local SSH control sessions for 60 seconds of idle time to
reduce clock-exchange and application bounds. A live radio reproduction
resolved both a full 14-second progress pass and an explicit zero-delivery
second; the overall scenario still fails. These changes refine observations,
not the lane controller.
Each impairment event retains separate host submission and guest completion
times for every changed lane. A simultaneous two-lane change has four guest
application records; completion times alone do not establish when the change
first took effect.
The lab sampler also records root qdisc and policer statistics on each WAN.
For the policed plan-change phase, loss is a byte balance: bytes offered to
the match-all policer minus root-dequeued bytes and the change in backlog.
Root and child drop counts are not summed: a live accounting reproduction
observed root drops already including the policer's drops. The gate bounds
loss and offered bytes over the entire uncertain phase after three seconds;
it passes only when the upper loss bound is below 5%. Insufficient boundary
resolution or changed counter identity remains inconclusive. These counters
observe WAN egress frames, including feedback and probes, rather than inner
TCP delivery. Sampler command failures invalidate collection explicitly.
The evaluator uses earliest submission for a deadline pass and latest guest
completion for its failure bound. Voice quantiles retain both certain and
possible membership near a phase boundary. A verdict that depends on that
uncertainty is inconclusive. The 3a latency deadline also checks its final
second separately; a median over the later phase cannot establish recovery
within two seconds.
The deterministic 3a gate checks the final second before that deadline too:
a fixture recovering at 2.5 seconds passed its previous post-deadline window
and is now rejected. Its better-lane median comes from an independent
voice-only baseline on that lane, separately for each family and direction.
Direct-link UDP calibration initiates each transfer at its sender, including
downlink transfers from hub to edge. This avoids iperf's reverse-UDP startup
failure when data arrives before its acceptance reply; it changes the lab
measurement procedure, not the transport.

Stage 0 initially stopped on an observed lab/field disagreement: a voice-only 15-second
mobile-egress outage lost no echoes in the field, but lost 10 and 40 in the lab,
with a longest lab arrival gap of 880 ms. The initial lane states were not
matched, and the cause is unknown. The operator subsequently reported host
CPU saturation during those measurements and designated field measurements
as the behavioral reference. Stage 0 resumed with host/guest wake-delay,
CPU/steal and per-thread scheduler observations; the earlier lab timing
verdict is inconclusive. CPU saturation is operator evidence, not a recorded
cause. At that stage 0 checkpoint the legacy controller remained;
the continuously aged model and stages 1–3 were not implemented. See the
[measurement record](../test/vm/README.md#adaptive-policy-stage-0--2026-10-02-in-progress)
for provenance, incomplete gates and the independent calibration failure.

Stage 0 collection now records actual daemon start times and dispatches a
cold transfer 30 seconds after the later startup (observed verification:
30.000 seconds). Direct-link calibration stops the owned wanbond processes
and uses fixed-size UDP to verify emulated capacity; TCP is reported
separately. On the lossy gigaradio path, removing 0.4% loss raised direct TCP
from 7.556 to 286.840 Mbit/s, supporting that distinction. These harness
corrections establish neither the adaptive-policy gates nor a stationary
field rate: the operator's 5G/Starlink links require contemporaneous lane
measurements and interleaved baseline/candidate rounds.

The cold gate starts its seven-second deadline from bounded TCP startup
evidence, between connection setup and captured test start on the two peers,
rather than host command dispatch. A delayed-dispatch reproduction was
incorrectly failed before that evaluator correction. Missing start evidence
remains inconclusive; the seven-second limit is unchanged.

The stage 0 lab reference budgets TCP payload independently of the candidate:
calibrated UDP wire service minus required voice, feedback, keepalives and
the reverse TCP ACK stream. It uses the observed lab MTU/MSS and inferred
encapsulation costs. One-way outages use the same fully surviving-lane reference as
two-way outages. This reference changes no transport decision.

All 26 healthy phase profiles now pass their direct UDP capacity calibration;
independent goodput references cover all 106 scenario phases. Independent
idle-latency references include three runs on each WAN and the pair on both
families; their raw spread is retained. Complete gate evaluation remains pending.
The adaptive TCP model also excludes SACK reports from ACK coalescing, as the
production classifier does. Its failing reproduction preserved only two of
three reports before that model-input correction. The remeasured baseline
produced identical measurements across three runs of all 52 cases; radio 2d
passes its voice and bulk checks and that finding is retained. No lane policy
was changed by either correction.
The older one-flow TCP model now preserves SACK reports too. Its separate
three-report reproduction likewise delivered only two before correction;
the complete bond test suite passes with the corrected model input.
The adaptive scenario model also stops renewing a hello lease when its
incoming direction is dark. A reproduction observed the old model keeping
that lease live without incoming traffic. The corrected input renews the
unfailed direction and lane independently; it changes no transport rule.
Three repeated runs on the original controller now give five passes and
three failures for 1a, four failures for 1b, and five passes and eleven
failures for 1c. Current stage 0 outcomes match the original controller.
Before replacing liveness mechanisms, the interactive failover test now
checks public transport deliveries: the encrypted datagram arrives exactly
once over a healthy alternate within the applicable deadline, or expires
when no lane can reach the receiver. A low-rate physical alternate checks
the bounded datagram lifetime rather than setting a private pacing rate
below the controller's minimum. The idle keepalive test likewise verifies
bulk delivery over the healthy lane and preservation of the idle pacing
rate, instead of asserting the private stall mechanism. Both restatements
pass with the existing controller; no liveness rule has been removed yet.

A stage 1 attempt was tested and rejected on 2026-10-03. Observed: its
ACK-progress liveness and suspect-copy reproductions pass, but all bulk
outage/recovery model variants fail and a bursty-link goodput regression
remains. Removing the legacy single-survivor bulk restriction also regresses
voice latency. The attempt was never installed in the lab or field; the
repository's stage 0 controller was restored. See the
[stage 1 checkpoint](drafts/20261003-2245-adaptive-stage1-checkpoint.md) for
the measured outcomes, the direction-specific scheduler bound and retained
source. Stages 1–3 remain unproved; later estimator replacements are not
treated as evidence that these failures are resolved.

Operator direction on 2026-10-04 permits bounded temporary field trials
despite unsatisfactory lab results. The outstanding model and lab failures
must accompany those results; stage acceptance and permanent deployment
restrictions are unchanged. The plan's
[research follow-up](drafts/20261002-1730-adaptive-policy-plan.md#8-research-follow-up--2026-10-04)
proposes delivery sampling, a shared lane wire budget and completion-aware
scheduling. C8 implements shared pacing as described below; the sampling and
completion-aware scheduling proposals remain unimplemented.

A stage 1 trial draft was built on 2026-10-04 from `04a745d` plus the
retained `stage1-c4-source.patch` (SHA256
`5799be7e16dc6279d3101512a419d6980d34289b5062d0921cad2162012c880f`).
Its ARM64 executable is `c4-s1-share`, SHA256
`45b28c75b6e66bc2a65c566e7e50c2bbb12d07d5da1ae9df52ff7dd41999c5b4`.
The draft uses physical ACK progress for live/suspect/dead decisions, removes
`stalled` and the silent 0.7 pacing cut, and exempts suspect real-time copies
from the normal copy allowance. A hello does not invent ACK progress.
Metrics add `liveness_age_seconds` and `ack_progress_known`; `liveness_state`
is 0 dead, 1 live, 2 suspect. `up` means interactive eligibility; bulk
requires live state. Monitor reports the same progress evidence and age.

The draft keeps one pacing clock for DATA, copies, repairs, keepalives and
ACKs. Because ACK v1 reports DATA wire bytes, its pacing target remains a
DATA budget; measured outgoing ACK demand supplies the feedback reservation
on the shared clock without changing the cumulative wire-byte field.
On a sole voice-bearing slow lane, bulk uses residual service with one bulk
datagram outstanding once capacity is known or voice dominates the budget.
The legacy fixed 5% survivor restriction is removed. The multi-lane voice
isolation rule remains. Stall delay is excused only through the flight
outstanding at recovery, cleared by later physical receipt progress; its
fixed clearance timeout and declining time envelope are removed. The full
`control.go` constant count falls from 32 to 31.

Observed in the targeted model: the sole-survivor reproduction delivers
9,600–10,800 bulk payload B/s, all 500 voice datagrams, and one-way p99
65 ms, compared with 2,400–3,600 B/s and p99 55 ms on `f75668e`.
That is a model service improvement with a latency increase. The draft
still fails complete scenario gates and the held-target assertion in
`TestLightlyLoadedLaneTargetStaysWithinCapacity`; it is not an accepted
stage 1 implementation. Stage 2 delay/rank and stage 3 capacity replacement
remain unimplemented. The [trial record](drafts/20261004-1105-adaptive-stage1-trial.md)
separates observed behavior, code inferences and unfinished gates.

The subsequent `c8-s1-share` trial bounds the extra original-voice pacing
allowance by its datagram size, while a sole leased lane, lower-class flight,
or reserved copy may borrow a full datagram's slot. Its targeted voice,
target and cold-stall checks, full non-privileged gate and Nix build pass
before a later TCP-model correction. Its complete scenario gates still fail.
Temporary field blackouts lose no voice on the candidates, but the baseline
also produces a zero-loss repeat; the observed difference is not a consistent
field gain. Native capped TCP reaches the same offered ceiling on both.
Both production hosts were restored and independently verified.

The TCP test models now discard RTT sample eligibility when retransmitting
a segment. A failing reproduction on `f75668e` showed a timeout forgetting
that history and inflating RTO; this is an input-model correction, not a
production transport estimator. The Karn-corrected-input baseline completed all
52 cases three times with identical measurements and verdicts: outage
passes/failures remain 1a 5/3, 1b 0/4, and 1c 5/11. Stage 1 still fails
scenario gates. The [trial record](drafts/20261004-1105-adaptive-stage1-trial.md)
retains executable/source hashes, old and corrected model provenance,
field variability, counter deltas and outstanding acceptance work.

A further test-model reproduction found previously SACKed data being sampled
again on cumulative progress, while fresh SACK timing was ignored. Both
models now share RTT selection that excludes ambiguous retransmission timing
and already reported receipts, using fresh SACK timing as needed. Neither
correction changes the production controller or acceptance thresholds.
The corrected `c8` outage gates still fail: its live gigaradio survivor has a
0.676 MB/s wire target at the deadline requiring 20.24 MB/s of TCP payload.
Repeated delay cuts and its low capacity estimate precede the blackout.
This explains the candidate's failure; it does not establish a field cause.
The attempt stops under the operator's explained-gate-failure rule and is
retained on `adaptive-stage1-c8-final` (`94b15c4`). At that checkpoint the
main branch retained the stage 0 controller; stages 1–3 remained unaccepted.

Observed after fresh-SACK timing correction: all 52 original-controller
scenario cases produce identical measurements across three runs. Outage
verdicts remain 1a 5/3, 1b 0/4, and 1c 5/11 passes/failures. Radio 2d now
fails at 151 ms voice p99, superseding its older-input pass for current
acceptance; bulk still passes. No gate was weakened. The restored main branch
passes the full non-privileged gate and `nix build`.

The operator's current field caps (2026-10-04) are Starlink standby at
0.5 Mbit/s symmetric and 5G at 100 Mbit/s down/10 Mbit/s up. These are
operator evidence for configured maxima, not measured RF service. The
retained radio and built-in field fixtures differ; the 300+300 Mbit/s
gigaradio failure is a stress result rather than the field's required
goodput. Field references require contemporaneous direct-link measurements.

The additional `field-standby.json` fixture now uses those rate caps in both
the lab profile reader and the deterministic model. Fixed 14/28 ms delays,
zero random loss, satellite policing and a 300 ms mobile buffer are modeling
assumptions. It leaves the required radio/gigaradio families unchanged.
Observed on `f75668e`: three identical voice-only idle measurements establish
p99 references of 41 ms on satellite and 59 ms on mobile. Baseline and `c8`
pass the six additional voice-only outages but fail their six combined
bulk/outage/recovery cases across three repetitions. The candidate's higher
standby-survivor TCP delivery still falls below the unchanged goodput gate;
it establishes a model increase, not a field improvement. The approved next
comparison matches startup ages and uses deployed → candidate → deployed
rounds with current direct-link measurements. Stage 1 remains unaccepted.

A further failing reproduction corrects the TCP models' loss evidence:
one far-ahead SACK had triggered multiple retransmissions from sequence
distance alone. Both models now require three distinct selectively received
full segments above a hole. This corrects test inputs, not the production
transport. All 52 baseline scenarios again have identical measurements in
three runs; radio 1c lane 0 direction 0 with bulk now passes and that
finding is retained. The corrected `c8` survivor still fails bulk and
recovery gates. Warm deployed → candidate → deployed field voice rounds
all pass loss/gap checks, without establishing a repeatable gain. The
initial 24 kB/s TCP field comparison was incomplete because Starlink
availability and management SSH became intermittent; no bulk verdict follows. Both
deployed binaries, empty candidate overrides and absent test qdiscs were
verified afterwards. Detailed observations and input provenance remain in
the trial record above.

After the operator reported the environment fixed and the office parked,
three deployed → `c8` → deployed field comparisons completed at matched
startup ages. Observed during the 15-second mobile-egress blackout with
24 kB/s TCP offered each way: candidate uplink delivery is 15.3–15.9 kB/s,
against deployed rounds' 1.4–9.3. All loss/gap checks pass and all submitted
TCP bytes arrive, but candidate voice p99 reaches 131–144 ms in two rounds
and downlink results vary. This establishes an uplink gain for that bounded
workload. Independent survivor idle-latency and residual-goodput references
remain unmeasured, so it does not establish all section 4 gates. All deployed
binaries and cleanup postconditions were verified afterwards; mobile VLAN
counters increased 100.24 MB across the test interval, including background
traffic. The unchanged model failures still reject stage 1's retained attempt;
stages 1–3 remain unaccepted. See the trial record for raw provenance and spread.

**Operator-approved C8 release, 2026-10-04.** The operator subsequently
requested committing and tagging this exact candidate for their installation.
Production code in `4a1cd54` matches all 176 production files checked against
the tested `94b15c4`; later test-model corrections remain. `v0.0.2` contains
ACK-progress liveness, shared pacing and residual survivor bulk service
described above. It removes the boolean stall state, silent 0.7 rate cut,
fixed survivor bulk restriction and fixed stall-clearance timeout;
`control.go` has 31 constant names against the 32-name original baseline.
The ACK version, framing, overhead and configuration are unchanged.

**Operator evidence:** their 20:18–20:19 UTC run reports 66.19 Mbit/s tunnel
download and 0.33 Mbit/s upload, with loaded download latency 105.70 ms and
a maximum of 831.68 ms. Sequential direct 5G reports 44.58/2.42 Mbit/s at a
different server; this does not establish aggregation gain. The earlier
bounded blackout comparisons establish their narrower uplink gain. Failed
stage 1 gates and unfinished stages 2–3 remain recorded. Both hosts were
restored to deployed binaries and empty overrides, with inactive restoration
timers and unchanged WAN qdiscs verified at 20:22 UTC. The operator window
advanced mobile RX+TX counters by 213.36 MB, including tests, management and
background traffic. See the [release record](drafts/20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04).

**Installed baseline and upload investigation, 2026-10-04.** The operator
selects `b444920` (C8 plus build identity) as the operational baseline; both
running executables are observed on that commit. Original `f75668e` remains
the reproduction reference. Controlled same-destination field uploads reach
the offered 1 and 3 Mbit/s ceilings, including after a 10 Mbit/s tunnel
download. These lower bounds do not reproduce the operator's 0.33 Mbit/s
observation or establish capacity or a policy gain. No production correction
is justified by that unreproduced observation.

The model now supports either bulk direction independently while preserving
reverse TCP ACK and transport-feedback demand. Four healthy standby cases
pass three identical runs on both original and C8 controllers: findings,
not new fixes. All 52 section 4 cases have three identical completed C8
verdicts; failed gates remain. In a gigaradio blackout diagnostic the survivor
is live at the deadline but paced at 653,342 B/s against required payload
20,242,721 B/s. Inference: completing liveness alone cannot prove this gate
with the legacy estimator retained. The stage-order dependency must be
reported before later estimator work. No estimator, wire or configuration
changes accompany these tests. See the
[baseline checkpoint](drafts/20261002-1730-adaptive-policy-plan.md#10-installed-baseline-and-upload-investigation--2026-10-04)
for measurements, collection defects, accounting and reproducible commands.

**Flight-allowance experiment, 2026-10-05.** Removing C8's one-bulk-datagram
flight restriction in isolation improves a slow-lane model upgrade, but fails
three existing voice outcomes. A temporary baseline → experiment → baseline
field comparison observes tunnel downlink 8.23 / 8.15 / 10.01 Mbit/s and
uplink 2.99 / 2.99 / 2.86 Mbit/s, with zero measured loaded-voice loss.
Latency remains within the baseline spread. Upload is capped at 3 Mbit/s;
raw downlink capacity is unmeasured. No field gain is established. Both hosts
are restored to `b444920`; the experiment remains outside the main policy.
The [execution record](drafts/20261002-1730-adaptive-policy-plan.md#isolated-flight-allowance-field-comparison--2026-10-05)
retains exact identity, failed gates, collection corrections and byte costs.
Delay/capacity replacement continues against physical service outcomes;
neither a pacing target nor a delivery lower bound establishes a physical
capacity ceiling.

**Controlled uplink upgrade, 2026-10-05.** A second field comparison of the
same isolated flight-allowance experiment shapes only the mobile wanbond UDP
flow, from 400 kbit/s to 2 Mbit/s. Baseline → experiment → baseline payload
service in the later receiver window is approximately 1.36 / 0.24 / 1.55
Mbit/s. Whole-report uncertainty bounds preserve the experiment's deficit;
guarded TCP-active voice windows lose no datagrams. Both hosts are restored
and verified on `b444920`; the experiment remains unmerged.

A separate deterministic two-lane uplink reproduction fails identically
three times on original and C8 policy at the ten-second upgrade deadline:
27,600 B/s against a 217,408 B/s available-payload reference. A voice-bearing
low-latency lane does not learn its extra service. The fixed-input model also
fails the field baseline, so it does not reproduce the field candidate
contrast. In the isolated estimator replacement, scheduling bounded pushes
from waiting tunnel demand in `Poll` raises deadline delivery to 180,000 B/s.
Existing slow-lane voice and takeover gates still fail. These are model
observations, not a field gain or completed stage. Main production behavior
and ACK v1 remain unchanged; the
[execution record](drafts/20261002-1730-adaptive-policy-plan.md#controlled-uplink-upgrade-and-two-lane-reproduction--2026-10-05)
retains inputs, uncertainty, source and failed experiments.

**Estimator field tradeoff, 2026-10-05.** An unmerged delay/capacity replacement
(`b5948c5`) schedules bounded pushes of queued real traffic from `Poll` and
removes the superseded controller (12 `control.go` constants versus C8's 31).
In one temporary baseline → candidate → baseline rate-upgrade comparison,
late-window uplink payload is approximately 0.28 / 1.41 / 0.29 Mbit/s;
whole-report bounds preserve that increase. Voice regresses: edge p99 is
50 / 168 / 49 ms, with two candidate losses, and hub p99 is 48 / 139 / 48 ms.
The candidate's earlier low-rate payload also falls. The deterministic upgrade
model passes three times, but other voice and adaptation outcomes remain
failed. This is a measured tradeoff, not improvement across the metric set
or a completed stage. Both hosts are restored to `b444920`, and main's policy
is unchanged. The
[execution record](drafts/20261002-1730-adaptive-policy-plan.md#estimator-replacement-field-tradeoff--2026-10-05)
retains source, differing baseline runs, failed checks, accounting and the
post-push delay-qualification hypothesis for the next experiment.

A follow-up candidate (`ef36799`) corrects that qualification. Its paired
field low-rate payload increases (0.15 / 0.24 / 0.15 Mbit/s); later recovery
is 1.41 / 1.56 / 1.39 Mbit/s with overlapping report bounds. Voice loses none,
but candidate edge/hub p99 remains higher at 100/118 ms. Baseline recovery
varies between paired sets, so the correction's field effect is not isolated.
Both hosts are verified restored. Later isolated sampler corrections advance
the delivery-byte anchor on every fresh ACK and age capacity over complete
ACK rounds; selected model service outcomes pass three times, while slow voice
and utilization still fail. Those corrections have no field result. See the
[follow-up record](drafts/20261002-1730-adaptive-policy-plan.md#post-push-correction-and-sampling-follow-up--2026-10-05).

**Holding-source follow-up, 2026-10-05.** Unmerged source `1d272f9` fixes
application-limited sampling and stale holding, then preserves current/pre-push
pacing until matching physical delay feedback arrives. It removes plateau
push rules; `control.go` has 10 constants against C8's 31. Selected model
service/voice outcomes pass three times, while the full bond gate retains
eighteen failures. Its temporary paired field late upload is approximately
1.41/1.51/0.30 Mbit/s. Candidate voice p99 (63/75 ms edge/hub) falls between
the baselines, zero loss is preserved, and receive gaps grow to 131/134 ms.
Throughput bounds overlap the initial baseline; improvement across the metric
set is not established. Both hosts and network state are verified restored;
owned `/run` artifacts are archived and removed. The
[follow-up record](drafts/20261002-1730-adaptive-policy-plan.md#holding-source-model-and-field-follow-up--2026-10-05)
retains exact source, rejected trials, model failures, field bounds and mobile
accounting (22.601 MB comparison, 24.534 MB through cleanup, nested intervals).

**Receiver-cohort field rejection, 2026-10-05.** One paired set for
`a8fe4a9` has estimated late upload 1.432/1.561/1.395 Mbit/s with overlapping
bounds. Voice RTT p99 rises to 186/192 ms from baselines around 47–51 ms;
receive gaps reach 178/180 ms and voice loses datagrams against zero-loss
baselines. The combined source is rejected for promotion. Its accounting
reproductions pass, but the default bond gate has 20 failures; no completed
policy or download gain is claimed. Both deployed baseline executables,
network state and operator `raspi5l` policy are verified restored. Owned
runtime reference directories and candidate binaries are removed after
archival/identity checks. Mobile RX+TX advances 24.679 MB during comparison,
26.459 MB through cleanup (nested intervals). The
[execution record](drafts/20261002-1730-adaptive-policy-plan.md#receiver-cohort-field-rejection-and-congestion-history-reproduction--2026-10-05)
retains exact provenance. A subsequent model reproduction shows that holding
can undo a congestion cut using the preceding capacity sample without a new
delivery measurement; that defect is observed in the model, while its
contribution to the field regression remains inferred.

**Eligible-backlog field checkpoint, 2026-10-05.** Experimental source
`fd088cc` delivers estimated late upload 1.425 Mbit/s against 1.231/1.385
before/after; whole-report bounds overlap. Voice RTT p99 is 188/175 ms with
4/1412 and 1/1402 lost replies, while both baselines are lossless. It is
rejected for promotion. Both exact baseline executables and cleared temporary
network/runtime state are independently verified. The next reproduced defects
are unqualified sparse and expired samples revising capacity during congestion;
the first correction regresses sole slow-lane voice and is withheld from field
testing. These observations do not establish a field cause or all-metric gain.

**Congestion-history field checkpoint, 2026-10-05.** Source `5540733`
has late upload 0.232/1.511/1.388 Mbit/s; the returning baseline overlaps the
candidate's throughput bounds. Candidate voice p99 is 156/123 ms versus
baselines 54–65 ms and it loses two edge-origin voice datagrams. No promotion
or all-metric improvement follows. Both deployed binaries and network/policy
state are verified restored, and owned runtime references/binaries are removed.
The comparison uses 23.517 mobile RX+TX MB, 28.902 through cleanup; the enclosing
interval of this and the preceding new set is 63.778 MB including background.
Those intervals overlap. The execution record retains exact source and bounds.
A subsequent sampler reproduction finds that global queued bulk certifies
sparse voice service on a protected lane that cannot carry that bulk. Per-lane
eligible demand must qualify capacity sampling. This finding is observed in the
model; its role in the field regression remains inferred.

**Paths and epochs.** An unpadded challenge-protected PROBE carries a 22-byte
capability record: `bond`, version 1, physical path ID, process Boot ID and Bind
Open generation. Padded PMTU probes retain their original size. A logical lane
is `(local physical ID << 8) | remote physical ID`. One concentrator socket can
therefore schedule separately to both authenticated NAT mappings of an edge.
Each direction owns its own rate estimate and pacing clock. Capabilities expire
after one second without fresh probes. Missing data ACKs stall the lane earlier;
keepalives test recovery. Socket retirement invalidates its lanes. Endpoint
repointing requires fresh route evidence. No application datagram is sent to
a peer before its hello has been learned: until then `Send` only queues, within
the queue bounds below.

**Wire format.** CONTROL types `0xa1` (data) and `0xa2` (ACK) use version 1.
Both carry a 19-byte header: version, Boot ID, Open generation, lane ID. Data
uses CONTROL.Seq for its lane attempt, followed by the destination Boot/Open
epoch, a global datagram sequence, a delivery sequence whose high bit denotes
the small-packet class, and the untouched encrypted WireGuard datagram. Zero
sequence/order with no datagram denotes a keepalive. ACKs contain the observed
sender epoch, high lane attempt and 64-bit receipt bitmap, cumulative wire
bytes, receiver elapsed time, ACK delay, and a global receipt anchor plus 256-bit
receipt bitmap. ACK revisions increase per lane; an 8192-revision replay window
accepts each reordered ACK once. Older ACKs merge delivery receipts and may
contribute newly confirmed delivery timing, but cannot update cumulative rate
snapshots, propagation samples, pacing or liveness freshness. Fresh ACK byte
counts and elapsed times cannot regress. Epoch, lane, source, length and
replay checks precede feedback use. A changed Boot requires the existing
PROBE challenge; old Open generations and data/ACKs addressed to an old local
epoch are rejected. Each new epoch pair restarts its sequences at 1; the bulk
resequencer starts there explicitly, including when packet 2 arrives first.
Outstanding packets from the old epoch pair expire; the new pair retains
unassigned queued packets. A data frame adds `bond.Overhead` = 101 bytes to
the WireGuard datagram: the 50-byte CONTROL envelope, the 19-byte header and
32 bytes of destination epoch, sequence and order. Boot MTU and runtime PMTU
resizing both reserve it.

Receive deduplication keeps 8192 attempts per lane and 8192 global datagrams;
the shorter ACK bitmaps do not constrain valid receive reordering. A global
receipt releases an old attempt after the lane bitmap has advanced past it,
without attributing that receipt to a specific replicated physical attempt.
Global confirmation releases every copy's congestion-window ownership, including
copies on other lanes. Physical attempt metadata remains available for later
RTT/byte accounting; a later physical ACK cannot release those bytes twice.
The global ACK bitmap first covers the oldest receipt not yet reported, then
returns to the newest window. Additional pending windows drain on the ACK timer
even after incoming data stops. This acknowledges late arrivals outside the
newest 256 entries without changing the wire format. The receipt anchor may
move backwards; ACK revisions, byte counts and elapsed times remain monotonic.
Missing bits never acknowledge absent packets.
The engine-facing receiver drains ready packets into one batch while preserving
per-peer order and virtual endpoints. Adaptive bulk batches coalesce for at most
one packet's serialization time at the reverse pacing target, capped at 4 ms.
This lets TUN GRO combine packets and reduce reverse TCP ACK traffic, while fast
symmetric links use shorter batches. Interactive arrivals flush immediately.

**Rate and latency.** Each lane starts at 125 kB/s in a discovery phase. While
backlogged, each adjustment sets the target to at least twice the larger of the
smoothed and latest delivery measurements, so the target follows a sender's
slow start instead of growing 6% per adjustment. Discovery ends on the first
queue-delay reduction; on a delivery round (ended by acknowledgement of data
sent after it began) with at least three timed-out datagrams and 2% of the
round's transmissions; or, when the lane sends at least 75% of its target, on
three consecutive rounds without 25% delivery growth (BBR's full-pipe rule,
needed because radio jitter widens the delay threshold). An isolated timeout
during discovery holds the target instead of ending discovery. The target leads
what the lane carries by the discovery gain only: a target inflated while the
sender was the limit leaves the path unprotected when the sender catches up
(VM trace `20260929-140805-radio-down-e2`: 259 datagrams expired in one second).
While datagrams wait for the lane, measured delivery also replaces the initial
125 kB/s assumption: on a slower lane the window sized from it admits a queue
of hundreds of milliseconds (VM trace `20260929-155641-continuity`: 117 ms of
queue on a 0.4 Mbit/s lane in the first second). When discovery ends, lower
classes wait on that lane for twice the queue delay measured, at most 500 ms,
or until a clear interval shows the queue has gone; real-time traffic alone
may use most of a slow lane, and a reduced target would then drain nothing. A
fixed 500 ms pause filled the tunnel queue of a 100 Mbit/s lane beyond its
bound, and 995 datagrams expired at once (VM trace
`20260929-165408-radio-down-k1`).
A cold burst of TCP initial
windows otherwise loses most of its datagrams before feedback arrives, and the
resulting loss run sets every flow's slow-start threshold to a few segments
(VM trace `20260929-122641-ramp-radio-down-c1-ss`). ACK timing measures RTT and
delivery rate over receiver-clock intervals of at least 50 ms, avoiding ACK
arrival-compression bias. The same observation also measures bytes submitted by
the sender over at least 50 ms of local time. Receiver-relative arrival time
minus local send time measures changes in forward transit without synchronized
clocks. Minimum transit baselines are separated into 128-byte wire-size buckets;
comparing a full datagram with a tiny keepalive otherwise mistakes serialization
time for router queueing. Within a bucket the serialization difference is less
than 8 ms at the minimum supported pacing rate. A new bucket establishes its own
minimum, and explicit baseline calibration clears all buckets. Control intervals
take the minimum of the resulting queue-delay samples. A congestion signal is
material loss, or a minimum forward queue delay across a control interval
above the threshold: the largest of 10 ms, `2*idleForwardVariation`, and
`wander + 2*wanderVariation + 10 ms`. Queue delay is measured from the lowest
transit time seen, and a path whose latency wanders sits above that floor most
of the time; when the wander is slow, the samples of one control interval move
together and their minimum is no nearer the floor than any of them. `wander`
is the smoothed queue delay of control intervals in which the lane sent less
than half of what it has shown it can carry (half its target, before that is
known) while delivery kept up, so that whatever delay it saw it did not
queue, less the spread of independent samples over one more than their
number: jitter drawn anew for every datagram lifts the lowest of a few
samples above the floor as well, and taken for wander it raised the
threshold of the lab's 40±30 ms WAN by 20 ms, and the queue voice shares
with bulk there by as much (`TestVoiceOnJitteryLaneAloneKeepsItsLatency`;
in the lab, voice round trips with only that WAN up had a p99 of 172-212 ms
against 148-194 before the wander allowance). Delays above 30 ms are not
taken for wander. Latency measured on the
production links (600 pings each, idle, 2026-09-30) has a round-trip standard
deviation of 7-10 ms, and on one of them moves between levels 10-20 ms apart
that last for seconds; a model lane with that wander and the fixed 10 ms
threshold was held at its minimum rate (`TestWanderingLatencyIsNotAQueue`).
netem's jitter is drawn anew for every datagram and does not show this. The
threshold is at most 100 ms: what an idle lane sees is the path's own only
while nothing else loads the path. A Speedtest on the production mobile link
itself raised the tunnel lane's idle variation to 361 ms and its threshold to
722 ms, and the Speedtest through the bond that followed ran without a delay
signal at 764 ms of loaded latency (2026-10-01; `TestDelayNoisePreservesVoiceAndBulkService`).

Loss is measured from byte counts. Every acknowledgement carries the bytes the
receiver has received on the lane, and the sender knows the bytes it had sent
through the acknowledged sequence. The difference is the loss so far plus what
was sent before that sequence and has not arrived yet. That backlog is never
negative and empties as the late datagrams arrive, so the lowest difference
within 200 ms is the loss so far, and its growth from one such period to the
one a second later is the loss of that second. Datagrams below the
acknowledged sequence that the acknowledgement does not confirm, and that
were sent less than the path's reordering allowance before the acknowledged
datagram, count as still on their way: the allowance is the largest of 10 ms,
three times the delay jitter, three times the unloaded forward variation, and
1.25 times the longest reordering of the last ten seconds. Reordering is what
confirmations show: a datagram confirmed by a later acknowledgement than one
sent after it was overtaken by the difference of their send times. The
datagrams one acknowledgement confirms are not compared with each other,
since their order of arrival is not known (`TestInOrderPathShowsNoReordering`).
A timeout cannot tell a lost
datagram from a late one. A datagram whose timeout passes is sent again at
once, but timeouts are not counted: with latency that wanders, datagrams that
arrived were timed out, repaired and counted as lost, and the target was cut
for loss on links that dropped nothing (VM runs of 2026-09-30 with
`wander.py`: 284 loss cuts in one transfer). Loss is material when the last
second lost at least three datagrams' worth of bytes and 2% of those sent (BBRv2's startup
exit threshold, applied in every state): a path that loses a fraction of a
percent at random loses something in every control interval at a high rate, and
cutting on each of them held a 300 Mbit/s lane at 3 MB/s
(`TestRandomLossDoesNotCollapseTheTarget`); repair covers such loss. The share
is taken over a second and not over one delivery round: a round of a hundred
datagrams on a path losing 0.4% holds three losses once in fifty rounds, and
each then ended discovery at the sender's own rate
(`TestUnderusedLossyLaneDeliversBurstyBulk`). This outcome test retains the
same bursty, lossy link, checks delivery of at least 99% of admitted steady
payload after a 500 ms drain, and retains zero AQM drops; it no longer asserts
a controller target. C8 delivers 99.98% identically three times. A lane too
slow to send a hundred
and fifty datagrams in a second is judged over as long as that takes, up to
four seconds: three losses a second were 6.7% of a 45-datagram lane, and it
lost 5% for good. Material loss is its own confirmation and cuts at once; a
lane losing one datagram in several control intervals never gives two
consecutive signals. Its cut takes the target to 95% of what was delivered,
not 105%: a path that polices rather than queues delivers its capacity while
it drops the rest, and the estimate follows it down. Both apply only to a
lane sending about its target (85% of it or more); a lane sending well below
it lost to its bursts, and what it delivered says nothing about the path (a
lane that took over voice from a failed one sent 30 kB/s of a 42 kB/s target
into a policed 50 kB/s path and was cut to 25 kB/s, dropping 96 voice
datagrams in its own queue); it is cut by 10% and its estimate left alone
(`TestPolicedLaneIsNotOverdriven`).

Jitter spreads the queue-delay samples, and the
minimum of a few exceeds the threshold by chance: with a 60 ms spread and two
samples, in most intervals. The mean difference between consecutive samples
estimates the spread (a third of it for a uniform spread; a queue changes
little between samples), and a delay signal needs `2*spread/threshold` samples,
at most 16, before it counts. The estimate in force is the one from before
the interval, so the onset of a queue cannot excuse itself. Until eight
differences have been observed, the probes' unloaded round-trip variation
stands in for the spread: a single delayed sample otherwise ended the discovery
of a 300 Mbit/s lane with 30 ms of jitter within its first 100 ms, at a capacity
measured from a handful of datagrams (`TestJitteryLaneStartsUp`).

*Hold and pulse (`control.go`).* Raising the target until the path queues, then
cutting it, keeps a standing queue in the path's own buffer, where small
datagrams have no priority over bulk: a steady 100 Mbit/s model held 40 ms
there (`TestSteadyPathIsUsedWithoutStandingLinkQueue`). Discovery therefore
ends with a capacity estimate, the delivery measured while the path was
saturated, and the target holds at 95% of it. About once a second a lane with
waiting datagrams pulses its target to 110% of the estimate, long enough to
build 1.5 times the detection threshold of queue (150-500 ms), so the queue a
probe costs is bounded by the pulse and not by feedback lag. A congestion
signal before the pulse's feedback is complete confirms the estimate; the
target drops to 85% until a clear interval shows the queue has drained, and
signals in that period do not change the estimate. That period ends after the
time the queue may need to leave at 85%, `2*queue/0.15 + 2*SRTT` (twice the
queue measured, since the probe ran on while its feedback travelled; loss a
probe caused is known 200 ms later still): delay that outlasts it is not the
probe's and counts as a repeated signal on a holding lane. Taken for the probe's queue without limit, the delay of a lane that lost
half its capacity while a probe drained held the target at three quarters of
the old capacity, with the path's buffer full
(`TestCapacityDropIsNotALevelShift`). A pulse without a signal
raises the estimate by 5%, the next by 10%, and the third returns the lane to
discovery at a gain of 1.5. The estimate rises at once, but the pulse is won,
and the next follows, only after thirty datagrams and a loss settling period
(200 ms) have passed without loss: a path that polices rather than queues
drops the pulse's excess instead of delaying it, too little of it to be
material, and its loss is known a settling period late. Loss in that time
returns the estimate to what it was and doubles the wait before the next
pulse, up to 16 s, until one is won without loss. Discovery also uses that gain instead of 2 while
real-time datagrams are carried: its overshoot queues in the path's buffer
ahead of them (VM run `20260929-152215-continuity`: 370 ms voice round trips in
the first five seconds of a cold start).

Growth needs evidence. A lane that sends less than three quarters of its held
target starts no pulse: the pulse would test nothing, and its silence would
raise the estimate. On a lane that carries the originals of a steady real-time
stream (traffic in every 200 ms of the last second), discovery bounds the
target by 1.5 times the most the lane delivered in the last ten seconds, not by
what was sent and not by the initial assumption of 1 Mbit/s; a burst delivers
too little to measure anything and keeps that assumption. Other lanes are not
bound: their overshoot delays no real-time datagram, and a bound that followed
a paused sender's delivery down left TCP to start again from nothing (VM run
`20260929-234339-continuity`: no TCP delivery in 65 seconds). The bound applies on every acknowledgement, because the control
interval runs only for a backlogged lane and a lightly loaded one never is. A standby lane that carried two voice streams
at half its capacity otherwise drifted to 81-97 kB/s on a 62.5 kB/s path, and
when the other lane failed its traffic overfilled the path's buffer, where
voice has no priority (VM runs of 2026-09-29: 300-390 ms voice round trips for
seconds after the failure; `TestLightlyLoadedLaneTargetStaysWithinCapacity`).
The cost is a slower start of bulk traffic beside voice.

While holding, one signal may be jitter: only a
second consecutive signal acts. Delay that persists on a lane holding below
its capacity is either a queue or a path whose latency moved to a higher
level: measured from the transit floor of the lower level, the higher one
reads as a queue for as long as it lasts, and no reduction of the target
removes it. The two are told apart before the target is cut, when nothing
explains the delay already: the target is not above the estimate, and neither
a probe nor an earlier cut can still be draining its queue. Bulk pauses on the
lane for twice the delay the control interval judged (the delay is measured a
round trip late, and a queue may have grown since), divided by the share of
the target that bulk gives up, since the higher classes keep sending. What is
sent after the pause finds an empty path, and the control interval that begins
with it gives the verdict. One sample near the floor shows the floor where it
was: the delay was a queue, the pause has drained it, and the estimate is
lowered by 3% as a repeated signal lowers it, with the target at no more than
95% of it. If the samples are still late, as many of them as a delay signal
needs, the path's latency moved: the floor is measured anew from there, and
the target stands. No probe follows a test for a second. The least time
between two tests of a lane starts at 2 s, doubles with every test that finds
a queue, up to 16 s, and starts again when one finds the floor moved. No test
runs when the pause would exceed 200 ms: on a lane that mostly carries
real-time traffic a bulk pause drains little
(`TestVoiceSurvivesOnSingleSlowLane`). Otherwise a second consecutive signal
is a queue: it cuts the target by 10% and lowers the estimate by 3%, and the
queue it leaves counts as explained for the time it needs to drain. Without
the test, a 15 ms level shift on a 300 Mbit/s lane cut delivery by up to half
for two to three seconds, and one that fell on a probe held the target at
three quarters of the capacity for as long as the level lasted
(`TestLatencyLevelShiftDoesNotCutTheTarget`). A lane that lost 20-75% of its
capacity still settles within five seconds
(`TestCapacityDropIsNotALevelShift`). A peer that restarts takes the lane's sequence spaces and probing state
with it but not its capacity estimate: the path did not restart. Cleared with
the rest, the estimate left the lane out of discovery with no bound but a
congestion signal, and on the production satellite link, which drops rather
than queues, the target rose from 80 kB/s to 7.4 MB/s in fifteen seconds at
the first download after a redeployment while the lane delivered 65 kB/s
(`TestPeerRestartKeepsTrafficWithinThePathBudget`). Cuts that take the target below 75% of the estimate mean capacity fell;
delivery is measured again and a pulse follows at once. A signal while no
datagram has waited 5 ms for a lane measures the sender, not the path, and
starts no hold. The drained sample that re-establishes the delay baseline also
refreshes the unloaded round trip used for the window: a held target can no
longer compensate for a window sized from an outdated round trip. It moves the
estimate halfway and leaves its variation alone. One sample is a draw from
the lane's jitter: replacing the estimate with it ranked a lane of 80 ms mean
round trip, at 23 ms, ahead of one of 46 ms, and real-time datagrams moved to
the slower lane for tens of seconds (`TestUnloadedDelayChangesChooseTheBetterVoiceLane`). Unloaded estimates update only after two seconds without transmitting
application datagrams on that lane; a transient empty queue during a transfer
does not qualify. The forward estimate uses only empty keepalive transit samples
and excludes return-path delay. Its relative-clock mean is reset when the remote
epoch changes. Round-trip mean/variation remain separate inputs to the window
and timeout: reverse jitter must not permit a larger forward queue. All unloaded
estimates have history independent of loaded samples, so an outage recovery
sample cannot copy earlier congestion into them. Idle feedback still measures path health but does not reduce
the pacing target; a lost empty
keepalive stalls the lane without applying a congestion rate cut. Instantaneous
delay remains visible in metrics. Demand is sampled before ACK processing releases
the congestion window, so delivery of a busy window can raise the target even
when no packets remain queued. A control interval with timed-out data reduces
the pacing target by 10% and caps it at 105% of measured delivery. A shallow
buffer can otherwise discard sustained excess traffic without generating a
large delay signal. Actual send rate is reported
separately: a low interval average does not establish safe burst pacing and must
not suppress this ceiling. This also covers shallow router buffers
that drop without accumulating 10 ms of queueing. A queue-delay reduction also
caps the target at 105% of measured delivery when measured sending exceeds
delivery by more than 20%. Without that guard, propagation jitter and a partially
filled discovery window can incorrectly turn low observed delivery into a hard
capacity estimate. The ordinary 10% delay reduction still applies when this
additional evidence is absent. ACKs wait at most 25 ms or
64 arrivals, matching the per-lane ACK bitmap without
sending one ACK for every eight packets on a fast download. After three
consecutive ACKs on a lane the interval stretches, up to 50 ms, so that ACK
bytes stay within 8% of the bytes they acknowledge: at a fixed 25 ms they took
14% of a 0.4 Mbit/s lane (`TestSlowLaneAcknowledgementShareIsBounded`). Sparse
traffic keeps the 25 ms confirmation. The sender learns the peer's cadence
from ACK arrivals and uses it in the repair timer, the early copy of a
real-time datagram and the window, so a peer running an earlier version, which
always uses 25 ms, interoperates. ACKs leave immediately and advance the
shared pacing clock. Because ACK v1 reports only DATA wire bytes, the clock's
rate is the DATA target plus measured outgoing ACK demand; charging ACKs
against the DATA target alone would accumulate pacing debt while receiving
a fast bulk stream. Congestion samples reflect the data
capacity remaining after feedback traffic. Target rate, actual send rate and
measured delivery remain separate values in metrics. A persistent delay increase
with a collapsed target triggers baseline calibration, at most once per 10 seconds: bulk pauses
on that lane for `max(200 ms, 2*SRTT)` to drain its queue, then a fresh sample
establishes the forward-delay baseline. Small packets continue; other lanes
remain eligible. This distinguishes changed propagation delay from persistent
queueing without synchronized clocks. It is the backstop for a lane whose
target has already collapsed; a holding lane asks the same question on the
second consecutive delay signal, with a pause sized to the delay and a verdict
that keeps the floor when the delay was a queue (hold and pulse, above). Targets are bounded to 16 kB/s–1.25 GB/s;
links below that floor or beyond the bounded packet windows are outside the
tested envelope.
The congestion window uses the unloaded mean RTT,
twice the unloaded RTT variation, 20 ms of queue budget and the 25 ms ACK
interval, multiplied by the pacing target, with a 3000-byte floor. The unloaded
mean starts with the authenticated path's RTT estimate. Neither variation nor
the minimum of loaded RTT samples can expand this allowance. Discovery additionally
caps the window at 3000 bytes plus physically acknowledged application wire
bytes; empty keepalives do not grow it. Each physical receipt contributes once,
including a receipt arriving after global delivery confirmation through another
lane. The credit counter saturates at the transport's maximum bounded packet
storage. A datagram larger than the window may depart when no bytes are in
flight, so the startup limit cannot deadlock a supported larger MTU.

A cellular link serves nothing for a few hundred milliseconds now and then
and delivers afterwards what it held (production mobile link, 2026-10-01:
about once a second the bytes in flight on the downlink lane doubled for
0.2-0.4 s with queue delays of 65-180 ms, and the lane then delivered as
before). What was sent meanwhile is late by up to the stall's length, at any
rate. Read as a queue, that delay ended discovery with whatever the lane
delivered at that moment, lost the probe it fell on and lowered the estimate
at every repeated signal: one minute after a restart the mobile uplink ended
its first discovery at 0.26 MB/s and carried 1.5 Mbit/s of a Speedtest
upload, against 4.0-4.9 two hours later. A lane whose peer reported nothing
new for 100 ms, twice the longest interval between acknowledgements, and then
confirmed datagrams sent before that silence began, stalled. C8 remembers
the stall's length and the highest attempt sent when progress resumes.
Physical receipt progress beyond that flight clears the exception; a fixed
timeout no longer asserts that it drained. Before it clears, delay up to the
stall's length plus the congestion threshold is put down to the stall
(`wanbond_adaptive_stall_signals_total`): discovery goes on, a probe is
neither won nor lost, the estimate stands, and the target gives way as it
does while a probe's queue drains. Larger delay is treated as queueing.
Outcome tests retain bulk delivery through bursty stalls and slowdowns.

Discovery that ends while the sender, not the lane, is the limit has measured
the sender. A return to discovery that ends so keeps the estimate the lane
had; a first discovery has none, so its target gives way to the delay by a
tenth and discovery goes on (`TestSparseAndResumedSendersKeepBulkProductive`).
A probe's win waits for thirty more datagrams only on a path that showed
material loss within the last minute; elsewhere the next probe began before
they were sent, and a lane below about 170 kB/s rose by a twentieth per
probe without ever returning to discovery (`TestAdaptiveSlowLaneRateIncreaseMakesBulkProgress`). Taken for
capacity, the first datagrams of an upload left the production uplink lane,
which had demonstrated 1.16 MB/s a minute earlier, at the 16 kB/s floor
(2026-10-01).

A held estimate rises to the delivery of the last two control intervals when
that exceeds it by a twentieth: the lane sends below its estimate, so delivery
above it is the path catching up after it slowed, and it carries that much.
Judged by delay alone, probes on the production mobile link were lost to
coincidence about every other time (its delay rises 13-15 times in a 7 s
download at any rate), and a lane that began at 3.5 MB/s reached the 9 it
carries in its fifth download (2026-10-02; `TestRepeatedSlowdownsKeepBulkProductive`). A
path that showed material loss within the last minute is left alone. The
estimate overshoots when a stall's backlog arrives at the radio's peak rate;
delay signals then hold the target.

A path may also slow down without going silent, and delivery, measured over a
control interval and smoothed over a quarter of a second, follows it down. A
lane therefore remembers the highest delivery it kept up over half a second
within the last two and a half, from the receiver's byte counts. The end of
discovery takes that when it is more than the delivery of the moment, a delay
signal does not lower the estimate below it, and the estimate is measured
anew only once that memory is below it too; if capacity did fall, the memory
lapses within three seconds. Material loss lowers the estimate as before. In
production the mobile lane ended discovery at 5.06 MB/s after delivering
4.8-5.3 for a second and a half, and half a second later held an estimate of
3.22 MB/s, measured while the path delivered 3.0-3.4
(`TestSlowdownDoesNotLowerTheEstimate`).

Small datagrams wait at most 100 ms before first transmission. Bulk datagrams
use RFC 8289 CoDel at dequeue, so a burst or
target reduction produces spaced congestion signals instead of a contiguous
loss run, with a hard residence bound of 250 ms, or the sum of CoDel's target
and interval if that is longer, up to 1 s. Its target is the slowest
lane's round trip (10-100 ms), not RFC 8289's 5 ms, and its interval twice the
sum of that round trip and the target (at least 100 ms), since the sender's
round trip includes this queue and a second drop before its response to the
first is visible cuts its window twice: a loss-based sender cuts its window by 30%, and only a
standing queue of about 0.43 of its round trip keeps the lanes busy afterwards
(VM trace `20260929-134042-fast-up-d1`). Small datagrams bypass this queue.
While any up lane is
discovering, CoDel does not drop and a bulk datagram admitted then keeps a 1 s
bound: that queue reflects the lane's own pacing, not path capacity, and small
datagrams bypass it. Each datagram keeps the bound in force at admission. At
most 8192 datagrams are queued or outstanding per peer; bulk is admitted up to
1024 short of that, so a flood that holds the count at its limit does not have
every small datagram refused behind it (`TestBulkFloodDoesNotRefuseVoice`; lab,
a call beside 400 Mbit/s of UDP offered to a 32+96 Mbit/s bond: 35-61% of the
voice datagrams lost with one shared limit, none with the headroom). Bulk repair lifetime is 250 ms from
first transmission, when its receive-order sequence is assigned. Queue residence
must not consume that repair window: a packet queued for 90 ms could otherwise
expire before a 190 ms feedback timeout permits its first retry. Small packets
retain the 250 ms deadline from admission. Neither deadline is extended by a
retry, and both classes have at most four attempts. The repair timer is the maximum of
60 ms, `SRTT + 4*RTTVariation + 25 ms`, and
`feedbackRTT + 4*feedbackRTTVariation`. The feedback estimate includes the full
time until delivery confirmation, including receipt-window buffering. Each ACK
contributes the oldest newly confirmed attempt per lane. Physical receipts
identify the attempt; global receipts contribute timing only for datagrams sent
once, since replication makes their physical route ambiguous. Each attempt
contributes at most once. Sampling only the highest physical sequence favours
fast arrivals under reordering and underestimates the repair deadline. RTT
variation is the EWMA of absolute sample error; the 25 ms term covers delayed
ACKs before the feedback estimate has converged. Before the first confirmation,
the timeout calculation allows variation of at least the variation the lane
measured while idle, or half the path RTT if it measured none. Half the RTT
puts the first repair of a radio lane (80 ms RTT: 265 ms) beyond the 250 ms
repair lifetime, so a datagram lost as a transfer starts reaches the sender's
TCP as loss and ends its slow start. The
first confirmation initializes its variation to half that sample, following
the estimator initialization in [RFC 6298 §2.2](https://www.rfc-editor.org/rfc/rfc6298.html#section-2).
This is a bounded datagram repair policy, not TCP's full retransmission timer.
Physical attempt records expire after two seconds and release their in-flight
bytes even when an RTT spike has raised the repair timer beyond that horizon.
The peer's count of received bytes is a cumulative acknowledgement: when no
more bytes are missing below the acknowledged sequence than at the last
acknowledgement, every datagram sent between the two arrived and is confirmed,
whatever the bitmaps cover. The bitmaps report a receipt once; when datagrams
arrive a hundred at a time, those that only a lost acknowledgement reported
were never confirmed and were sent again (production, 2026-10-02: 900-1800
repairs in a 7 s download, all duplicates; the acknowledgements were not lost
on the link but rejected for their time stamp, the defect described above;
`TestLostAcknowledgementDoesNotCauseRepairs`).
Liveness follows fresh physical ACK progress, including progress in sequence
or received bytes. With attempts outstanding, twice the peer's ACK cadence
without progress makes a lane suspect; one RTO makes it dead. Suspect lanes
remain eligible for small traffic, divert bulk and force real-time copies
outside the ordinary copy allowance. Fresh hellos only renew the lane lease.
Dead lanes with a current lease send empty keepalives every 50 ms to detect
their return; progress restores live eligibility. Silence does not lower the
capacity estimate or pacing target.

Repairs prefer a different eligible lane. A repair returns to the lane of the
datagram's last transmission only once the peer has reported a later datagram
received on that lane, or when fewer than three followed it there, so that a
loss at the end of a burst is still repaired. While a lane delivers nothing,
everything in flight on it times out together, and what is sent again on it
arrives as a duplicate behind the originals: in production the concentrator
sent 1119 repairs in one 15 s download, 319-354 of them within a second each
time the mobile lane went unconfirmed, and the edge counted 1035 duplicates
while its resequencer skipped 4 sequences (2026-10-01;
`TestSilentLaneIsNotSentRepairs`). The lane was unconfirmed there because its
acknowledgements were rejected, not because the link delivered nothing; the
rule is for a lane that does.

**Traffic classes (`schedule.go`).** Datagrams of at most 384 encrypted bytes
are small. Small datagrams that are not TCP form the real-time class (voice,
DNS, WireGuard keepalives); small TCP datagrams, mostly ACKs, the second; larger
datagrams are bulk. This is a size and protocol heuristic, not an application
classifier. Classes are served in that order. Equal turns are not enough: two
50 Hz voice streams need 70% of a 0.4 Mbit/s lane, more than an equal share
beside a TCP ACK stream and bulk. The measured demand of each small class, plus
10%, is reserved on the lanes in order of unloaded round trip plus twice its
variation, and lower classes are paced and windowed to leave it free; a lane
filled by bulk otherwise sends voice to a slower lane. The loaded round trip is
not used for ranking, since it rises on the lane that carries the traffic
preferring it. Each lower class keeps 5% of one lane, the lane with the most
capacity left for it, so a flood in a higher class cannot take every
transmission slot; the guarantee does not sit on the lane voice prefers while
another lane has room, because one bulk datagram occupies a 0.4 Mbit/s lane for
28 ms. Small TCP datagrams are mostly the ACK stream of a transfer in the other
direction, which can be coalesced; bulk cannot. While bulk waits, that class is
held to half of what real-time traffic leaves and bulk gets the rest: given
everything, the ACK stream of a fast download left an upload nothing for ten
seconds at a time (VM run `20260929-152215-continuity`). Bulk keeps only its
guaranteed minimum on a real-time lane. While another up lane carries no
real-time originals and can take the bulk, that is a lane with such originals
on which one full datagram takes longer to send than the 10 ms of queue the
lane is allowed, a lane below 1.2 Mbit/s: one call needs a third of a
0.5 Mbit/s lane, and bulk in the rest put 22-24 ms of serialization ahead of a
voice datagram for a third of a megabit of throughput
(`TestBulkKeepsOffTheSlowLaneACallRides`: one-way p99 43 ms shared, 20 ms
kept clean). With no other live lane for bulk, bulk uses residual service
rather than being confined to its guaranteed 5% minimum. If one full bulk
datagram takes more than 10 ms to serialize and capacity is known or voice
originals plus copies exceed the bulk allowance, its class window permits
one bulk datagram. A lane whose capacity was never found is not excluded by
the multi-lane isolation rule: beside a real-time stream
its target is held near recent delivery, by that target the stream alone took
most of the lane, and bulk confined to its minimum could not raise delivery,
so on the only lane up a transfer started beside a call received nothing for
as long as the call lasted (`TestBulkBesideVoiceDiscoversTheOnlyLane`; lab,
one 6 Mbit/s WAN: 0.0 Mbit/s of TCP in 20 seconds before, 4.0 after). Bulk
discovers such a lane at the pace that bound allows, and the first congestion
signal fixes its capacity. Each class is held to its share
of the window by its own bytes in flight, and real-time datagrams within their
reserved share are not blocked by the bytes of lower classes: on a slow lane
one bulk datagram in flight is a third of the window. Small datagrams may borrow 5 ms of pacing, and bulk competing with
queued small datagrams gets the same lead. Real-time datagrams may exceed a
full congestion window by one datagram and go to the lane on which they would
arrive first. Only real-time datagrams are copied onto a second lane.
Additional copies have an allowance of 20% of the healthy lanes'
aggregate pacing target, capped at 64 kB/s, with a 100 ms burst allowance.
A budgeted copy leaves with its original, so the other lanes reserve
capacity for the copies of the originals they do not carry, as much as the
allowance and the real-time demand permit; lower classes are paced and
windowed to leave it free. Original voice may borrow its own datagram's
slot on the shared clock; a sole leased lane, lower-class flight or reserved
copy permits borrowing a full datagram's slot. The class pacing clock keeps
its ordinary lead. Suspect-lane copies bypass the ordinary token allowance,
and pending voice gets an alternate copy before its repair timer. Without
the reservation bulk filled the second
lane's window and pacing slots, four in ten voice datagrams went uncopied
whatever the allowance, and each of those lost on its lane arrived about
250 ms late (`TestVoiceLossDoesNotExceedLatencyBudget`). Two 50 Hz voice streams
need 30 kB/s of copies, 15% of a 0.4+1.25 Mbit/s uplink.

The wire budget bounds bulk beside voice on a sole survivor. Inferred from
the wire format: two 50 Hz streams of 224-byte encrypted datagrams consume
`100*(224+Overhead+28)` = 35,300 B/s before feedback. On the radio profile's
0.4 Mbit/s uplink survivor, that leaves at most 14,700 B/s for other traffic
with no voice loss. Thus the adaptive-policy plan's simultaneous voice gate
and bulk gate of 75% of the survivor's total capacity cannot both hold there,
even allowing the voice gate's loss budget. The operator approved using the
available TCP goodput after required voice traffic and protocol overhead as
the percentage denominator in voice-bearing adaptive-policy scenarios.
References must be independent of the candidate's estimates and unnecessary
traffic; voice gates and adaptation deadlines stand. The
[baseline reproduction](../debug/20261002-180053-adaptive-budget.md) records the
original conflict; this amendment removes the stage 0 blocker.

A real-time datagram that waited is worth less than the one behind it. When
real-time datagrams have waited more than 20 ms for a lane throughout 100 ms
without the queue emptying,
the oldest are dropped until the queue is current. A backlog formed when a
lane fails otherwise drains only as fast as the remaining capacity exceeds the
flow's rate, which on a slow lane takes seconds, and every datagram meanwhile
arrives late by the backlog (`TestVoiceCatchesUpAfterTakeover`).
An initially unreplicated real-time datagram can use this same budget for one earlier
cross-path copy after `max(60 ms, baseRTT + 25 ms)` if its original lane has
returned no ACK since it was sent. This avoids waiting for a long feedback tail
to consume its entire repair lifetime. Fresh feedback, insufficient budget,
or no eligible alternate path suppresses the early copy. Bulk retains the full
feedback timeout.

**Flow isolation and TCP ACK coalescing.** The engine classifies IP packets
before encryption when the Bind opts in (`PacketMetadataEnabled`).
`conn.BindPacketSender` carries local `PacketMetadata` alongside the encrypted
datagrams of a send batch; as with `Bind.Send` the Bind must not retain the
caller's buffers, and the transport copies each datagram into its own queue.
A Bind without the interface keeps the plain `Send` path. Pooled outbound
elements reset their metadata, including for generated keepalives. All engine
type aliases remain in `internal/bind/bind.go`.

The 40-byte flow identity contains the IP version, protocol, full source and
destination addresses, and TCP/UDP ports. IPv6 hop-by-hop, routing and destination
options are traversed with length checks. Fragments and unsupported extension
headers share an address/protocol queue; invalid IP headers and generated
packets use the unclassified queue. Within each small class a ring of nonempty
flow queues rotates after each datagram, preserving FIFO within each flow. Bulk
remains one FIFO. This prevents one flow from taking every turn of its class;
it does not identify applications or guarantee bandwidth against arbitrarily
many competing flows. Flow identities are neither transmitted nor metric labels.
Strict priority would starve the classes below; their minimum shares, described
under traffic classes, bound that.

For unfragmented IPv4 without options and IPv6 without extension headers, the
engine also identifies pure TCP ACKs with no payload, reserved/control/ECN flags,
urgent pointer, zero window, or options other than padding and a single valid
timestamp. Coalescing replaces only the middle of three queued, strictly
advancing cumulative ACKs in the same flow. Sequence number,
traffic class and timestamp presence must match; timestamp values cannot go
backwards. Comparisons use TCP serial arithmetic. At least two queued ACKs remain
in a sustained eligible burst. The newest advancing ACK carries the current
nonzero advertised window, which supersedes the earlier snapshot even if the
encoded window field changed. This follows TCP's window update ordering by
segment sequence and acknowledgement number ([RFC 9293 §3.10.7.4](https://www.rfc-editor.org/rfc/rfc9293.html#section-3.10.7.4));
it does not require learning the negotiated window scale. Duplicate ACKs, SACKs,
window-only updates, zero-window transitions, unknown
options and other control information are preserved. Replacement happens before
assigning any outer delivery sequence, so it creates no resequencing gap and
never changes an encrypted datagram. It is counted separately from queue drops.

**Receive ordering decision.** Bulk has its own delivery sequence and a
resequencing hold before WireGuard of 250 ms, the sender's repair lifetime
(`bind/adaptive.go` requests 300 ms, to add the tested one-way propagation
delay, and the resequencer clamps a request to its 250 ms construction
timeout; see *Receive resequencer*). The resequencer's window is 32768
frames, so that a gap can wait that long at 600 Mbit/s (12500 datagrams arrive
in 250 ms); at 2048 frames it was abandoned, a loss to TCP, once the flow
exceeded about 10000 datagrams a second
(`TestResequencerHoldsARepairAtHighRate`). An 8192-packet receive bitmap covers
cross-lane reorder; ACKs advertise 256-entry receipt windows plus their lane
receipts. Small packets are deduplicated and delivered immediately through a
bounded 256-entry queue. A late small packet does not become lost merely because
a newer packet arrived. This deliberately permits reorder at the engine instead
of letting bulk loss stall voice candidates. Inner anti-replay remains
authoritative; extreme delay/PPS combinations outside its window can still
discard stragglers. When a bulk gap fills, the next gap's deadline derives from
its own buffered successor observation, not the older gap's deadline.

Both ends must run the transport. Per-peer metrics expose lane targets, actual send and
delivery rates, physical and confirmation RTT/variation, idle variation, queue
delay, in-flight bytes, repairs, eligibility, drops and expiration.
Small queue drops also retain their size/protocol class and cause: full
admission, expired residence deadline, or persistent real-time backlog shedding.
The two class counters (`realtime`, `small_tcp`) each expose three causes;
their sum equals the existing small-packet drop aggregate. Counting preserves
the queue rules and adds no packet/application identification or wire field.
Small-class queue residence is recorded once when a datagram leaves the local
queue for its first transport submission. Its cumulative histogram separates
this local wait from the lane's measured transit; repairs, copies, unsent drops
and superseded ACKs are excluded. Snapshot counters remain passive and alter
no pacing or scheduling decision. Transport submission does not establish a
successful socket write; later socket/kernel/upstream waits are outside it.
The 2026-10-05 diagnostic on experimental C20 observes real-time local wait
p99 bounded by 5/1 ms while voice RTT p99 is 141/163 ms. Echo handling maxima
are below 0.32 ms; the edge's kernel shaper has up to 17.9 KB queued and the
lane's congestion allowance exceeds 200 ms. This narrows the observed delay
components without attributing individual echoes or proving a policy cause.
The [plan's measurement record](drafts/20261002-1730-adaptive-policy-plan.md#queue-residence-field-diagnostic--2026-10-05)
retains counter windows, extraction recovery, restoration and metering.
The same component control on accepted C8 behavior has zero guarded voice
loss, 64/59 ms RTT p99 and local real-time wait p99 bounded by 1 ms on both
hosts. Its fresh direct 5G reference differs, so the two diagnostics do not
establish a raw-throughput comparison. A public reproduction additionally
shows that lane-wide progress does not certify an older missing real-time
datagram. Per-datagram overdue recovery is an isolated experiment; one model
voice direction improves while the opposite tail worsens. Its subsequent
bounded B/C/B field comparison records about 1.20/1.08% voice loss and
206/205 ms RTT p99, against zero loss and 50–57 ms p99 in the surrounding
baseline phases. It also records 32 edge real-time/stale queue drops. This
rejects the experiment; individual echo attribution remains unknown. Accepted
policy still uses the lane-wide recovery predicate described above.
An isolated C8 scheduler experiment separately reproduces fresh real-time
wait behind pending recovery copies: 26 ms becomes zero when fresh small
datagrams receive their turn before repairs, while all 19 older originals
still recover. Its full non-privileged gate and Nix build pass. This
scheduler-only result does not establish
end-to-end delay.
Its bounded field rate-change comparison has overlapping upload bounds,
55/50 ms voice RTT p99 and one missing edge-client echo, with no local
small-queue drops. Real-time local wait p99 is bounded by 1 ms on both hosts.
That workload does not establish the modeled WAN-failure benefit or a
repeatable field improvement; the experiment remains unaccepted.
Three subsequent radio WAN-failure collections fail their whole-run arrival-gap
checks; one also misses a returning lane's physical bulk-receipt deadline.
Independent references are absent, and host/guest timing records show CPU
interference as a possible contributor. A voice-only field baseline completes,
but an edge reboot interrupts the candidate transfer before activation. Neither
measurement establishes the scheduler experiment's field WAN-failure benefit.
The subsequent field B/C/B set captures all three voice-only measurement
phases. Candidate loss is 6/2750 in each direction, against baseline
17/19 before and 10/14 after; hub RTT p99 is 69 ms against 103/101 ms.
Edge RTT p99 and arrival gaps are mixed, and four consecutive missing echoes
exceed the existing limit of three. The candidate's direct 5G reference is
also higher than both baselines. This single set does not establish a
repeatable gain. Phase cleanup completes, but a maintenance reboot interrupts
final verification; independent checks subsequently verify both deployed
hashes, the operator's `auto` policy and removal of owned runtime artifacts.
The operator reports maintenance and stable power; reboot causation is not
independently established. The counter reset leaves total mobile use unknown.
Three gigaradio collections also fail to establish the required three-run
pass: two have failed checks and one remains inconclusive. Combining fresh
priority with age-based recovery still fails the deterministic slow-link
rate-reduction guard, so that combination remains rejected. See the
[measurement record](drafts/20261002-1730-adaptive-policy-plan.md#fresh-first-single-wan-field-measurement--2026-10-05).
A subsequent complete field repeat does not reproduce the lower loss:
candidate losses are 22/21 against baseline 12/4 before and 31/42 after;
candidate voice p99 is 181/167 ms and consecutive losses are 7/8. All cleanup
and deployed-state verification pass. The fresh-first C8 choice remains
unaccepted. A stricter buffered rate-fall reproduction separately checks
actual TCP and echo traffic: C8 delivers 226800 B/s against a 409569 B/s
requirement at five seconds, with 65 ms voice p99. The unmerged estimator
with fresh-first scheduling delivers 511200 B/s with 90 ms voice p99, but
both fail the expired-datagram burst check. Its scheduler reproduction passes
after failing first; its full default gate retains 22 bond failures, and Nix
passes. These are model improvements within an unaccepted prototype, not
a field gain or a completed stage.
The estimator plus fresh-first prototype (`72fcc9d`) subsequently loses zero
of 8250 field voice echoes per direction across three B/C/B single-WAN
blackout comparisons, while every surrounding baseline phase loses echoes.
This is a repeated loss result in that bounded workload. RTT tails are mixed
in the second set, and arrival gaps exceed 150 ms in every set. Reconstructing
arrival times from monotonic send stamps and RTT confirms those gaps; clock
differences are below 0.1 ms in all candidate records. Guarded candidate
small-queue drops are zero, with local real-time residence p99 at most 5 ms;
these aggregates do not locate the individual stalls. No TCP transfer runs
in these comparisons, so goodput and bandwidth efficiency remain unproved.
Both deployed baselines and temporary-state removal are independently
verified after every set. The prototype remains unaccepted with 22 default
bond failures; the installed policy and release tag remain unchanged. See
the [three-set field record](drafts/20261002-1730-adaptive-policy-plan.md#estimator-plus-fresh-priority-three-field-sets--2026-10-05).
Separate model instrumentation finds 206 never-delivered bulk originals
expiring in the buffered rate-fall window, each with only one attempt and
an RTO at least as long as the 250 ms repair lifetime. A public progression
reproduction retains a proven alternate before expiry but receives none of
the lost original. Deadline-based repair experiments improve that case while
failing existing utilization or delay guards; none changes accepted recovery.
Capped field upload then exposes a throughput/voice tradeoff: `72fcc9d`
improves low-rate upload against both adjacent baselines but raises loaded
voice p99 to 158/154 ms. A qualified receipt-clock sampler (`fcb25f1`)
passes the quiet buffered rate-fall model while retaining 23 default bond
failures. Its field upload bounds overlap at least one baseline; loaded voice
p99 is 135/148 ms with 2/1 lost echoes against zero in both baselines. The
returning baseline sends no 5G bulk originals in either guarded upload window,
despite its immediately preceding direct reference reaching the offered rate.
Scheduling/rediscovery is a hypothesis; physical conditions during the tunnel
measurement are not held constant. A voice-primed FIFO progression model
reproduces an upload deficit but passes its voice gates, so it does not
reproduce the field tail failure. TBF backlog/rate is a proxy, not measured
packet latency or an exact FIFO-model equivalent. The qualified prototype
remains unaccepted; deployed state is independently restored. See the
[capped field and sampling record](drafts/20261002-1730-adaptive-policy-plan.md#capped-field-upload-and-qualified-receipt-sampling--2026-10-05).
The deterministic transport and real UDP adapter share a delivery contract test;
the [KVM lab](../test/vm/README.md) adds actual encryption, TUN interfaces, TCP
and independently shaped WANs.

**Outcome-test checkpoint, 2026-10-05.** The descriptions above remain the
installed C8 policy; the replacement delay/capacity model is an isolated,
unaccepted experiment. Tests now check service outcomes before the mechanisms
are removed. On unchanged C8, three identical repetitions give 54,080 B/s
through the policed slow lane after a peer restart against a 56,858 B/s payload
budget, with 6% of offered bytes dropped. Bursty bulk beside a lightly loaded
voice lane offers 41,646 B/s on that 62,500 B/s path, drops none of those bytes,
and delivers all 5,400 measured voice datagrams with 43 ms one-way p99.
These replace target-peak and loss-signal-counter assertions; a commanded
rate is not a measurement of physical service.

Stronger progression cases retain baseline failures: simultaneous forward and
reverse delay noise, sparse/resumed traffic on a policed path, stale floor
evidence and a formerly slow lane gaining capacity. They remain under the
`adaptivepolicy` tag. The latest prototype passes the slow-lane bulk gate
only after removing the inherited one-bulk-datagram flight restriction, but
other utilization and policing gates still fail. No field gain or completed
stage is established. See the [execution record](drafts/20261002-1730-adaptive-policy-plan.md#11-revised-execution-goal--operator-2026-10-04)
for inputs, observations and retained evidence.

**Fresh-evidence pacing field checkpoint, 2026-10-05.** Experimental `897004c`
raises controlled low-rate upload to 0.230–0.282 Mbit/s whole-report bounds,
above both paired baseline upper bounds (0.181/0.188). Voice loss instead
exceeds its gate at 1.77%/1.13%, so the source is rejected for promotion.
The returning baseline also has 314 ms receive gaps; its variation is retained.
The complete candidate gate has 20 bond failures, though its targeted
accounting/feedback outcomes and Nix build pass. The voice-first model passes
on both sources and does not reproduce field loss. Small-queue class/cause
counters are added to narrow that diagnosis without changing scheduling.
Both hosts are verified restored to `b444920` with the operator's exit policy
and network state. Mobile use is 25.767 MB during comparison / 34.153 through
cleanup, overlapping intervals. The
[paired execution record](drafts/20261002-1730-adaptive-policy-plan.md#fresh-evidence-pacing-field-comparison--2026-10-05)
retains measurements, CPU observation limits and the four-set enclosing meter.

The subsequent counter-only field diagnostic observes three guarded edge
real-time backlog-shedding drops, zero small TCP/admission/deadline drops,
and no hub queue drops. Four additional startup drops lie outside the guarded
window. Voice p99 is 75.4/161.3 ms; lower loss in this unpaired run does not
establish a policy gain. Both hosts and network state are verified restored;
mobile RX+TX is 9.665 MB during the diagnostic / 10.678 through cleanup.
See the [attribution record](drafts/20261002-1730-adaptive-policy-plan.md#small-queue-attribution-in-the-field--2026-10-05)
for timing, provenance and the still-unproven connection to pacing recovery.

The experimental branch now retains qualified recovery evidence separately
from raw congestion evidence, with observation age and the physical attempt's
send time. Its two narrow recovery reproductions pass three times. Broader
voice and utilization outcomes still fail, so this source is not promoted;
main retains the baseline policy. See the [recovery-cohort checkpoint](drafts/20261002-1730-adaptive-policy-plan.md#recovery-cohort-checkpoint--2026-10-05)
for the measured limitations and rejected alternatives.

The qualified-cohort paired field set rejects promotion again: candidate
late upload overlaps the returning baseline, while hub voice RTT p99 is
172.9 ms against baseline 63.5/57.4 ms. No local small-queue drop occurs,
so the large tail remains a separate unlocalized defect. Both deployed
baseline binaries and temporary state are verified restored. Mobile RX+TX
is 25.009 MB during comparison / 26.618 through cleanup. The
[paired checkpoint](drafts/20261002-1730-adaptive-policy-plan.md#qualified-cohort-field-comparison--2026-10-05)
records variation, 19 default bond failures and the still-incomplete gates.

### The multipath Bind — `internal/bind`

The heart of wanbond: the `conn.Bind` implementation the engine drives. It:

- **presents one stable virtual endpoint per peer** to the engine while privately
  fanning out across the real per-path sockets beneath it (design rule **A1**, see
  [p0-findings.md §3](p0-findings.md)). The engine must never see per-packet
  endpoint churn, so every receive returns the *same* `*udpEndpoint`; the learned
  destination is held in an `atomic.Pointer` because the Bind writes it under its
  mutex while the engine reads it locklessly.
- **learns edge endpoints dynamically** — the concentrator needs no edge endpoint
  config; it discovers each path's (possibly NAT'd) source from inbound traffic,
  enabling real CGNAT traversal.
- **routes each edge peer's send-traffic to ITS OWN concentrator (multi-exit edge,
  T251/Q68b)** — the send-side dual of the concentrator's dynamic learning. A
  multi-exit edge statically configures N concentrator peers, each with its own
  endpoint; `device.Up` seeds every peer's configured endpoint into the Bind
  (`SeedEdgePeerRemotes`) BEFORE the engine parses the UAPI config, so
  `ParseEndpoint` returns the **owning peer's DISTINCT virtual endpoint** (resolved
  via `edgePeerByRemote`) rather than the primary's, and `Open` seeds that peer's
  paths at ITS concentrator (per-peer `configuredRemote`, not a single bind-global
  default). Without this, the engine would map every edge peer's endpoint to the
  primary's virt and every peer's WG traffic — and probes — would egress to one
  hub. A single-peer edge keeps the bind-global-default path byte-identical. An
  endpoint-less (unresolved-hostname) edge peer boots remoteless and is driven by the
  R70 re-resolution loop: as of **T253** `startFailoverAndResolution` builds one
  hub-failover/re-resolution controller **per eligible peer** (every peer satisfying
  `peerNeedsHubFailover`, not just the first), each reading that peer's OWN
  per-(peer,path) prober set as its liveness plane and routing its OWN repoint and
  endpoint-less install through the per-peer seam below — so EVERY peer, primary or
  not, is driven and none cross-wires onto the primary's virt. This closed **D100**
  (the earlier T251 build drove only the first-qualifying peer — a non-primary
  endpoint-less peer was never installed, and a non-primary first-qualifying peer's
  install path `deviceInstallEndpoint`→`IpcSet`→`ParseEndpoint(ap)` mis-resolved to the
  primary's virt because `ap` was absent from `edgePeerByRemote`; that limitation is now
  structurally gone). The per-peer **remote-repoint/install
  seam** lands in **T252**: `Multipath.SetPeerRemoteFor(peerName, ap)` repoints exactly
  the named peer's paths at `ap` and makes only that peer's transport forget its lanes
  (they are re-learned from the new hub's hellos), WITHOUT touching the bind-global `defaultRemote` — so with N independent
  hub-failover controllers peer B's endpoint switch cannot clobber the remote peer A
  relies on (unlike `SetPeerRemote`, which drives the primary and does write
  `defaultRemote` for single-peer-edge back-compat). `SetPeerRemoteFor` ALSO updates the
  two durable seeds — the peer's `configuredRemote` and the `edgePeerByRemote` keying (old
  remote key out, new key in) — so an engine Close/Open re-seeds that peer's fresh paths at
  its CURRENT hub rather than its stale boot hub (**D101**), and `ParseEndpoint(ap)`
  resolves the new remote to the peer's OWN virt. Seeding these unconditionally lets the
  seam **install** a remote for a previously-unseeded (endpoint-less hostname) peer, closing
  the **D100** `ParseEndpoint` install-misresolution leg (an `ap` absent from
  `edgePeerByRemote` otherwise falls back to the primary's virt). Because
  `edgePeerByRemote` is keyed by remote and cannot represent two peers at one
  `addr:port`, the seam **fails fast** (returning an error and mutating no state) when
  `ap` is already owned by a DIFFERENT peer: config load rejects duplicate LITERAL
  endpoints across peers, but a hostname-only peer carries no literal to compare, so
  the seam is the only guard against two hostname peers resolving to the same
  `addr:port` and one silently stealing the other's send-routing key (repointing a peer
  onto its OWN current remote stays a valid idempotent no-op). **T253** wires one
  per-concentrator hub-failover/re-resolution controller per peer, routing each controller's
  hub switch and initial install through this seam, and surfaces a per-peer **endpoint-list-
  exhaustion** signal (`SetOnExhausted`) the cross-concentrator exit selector (T269) subscribes
  to: within-concentrator single/partial endpoint failure stays that controller's own
  round-robin business (Q72); only a full flattened-list wrap with hub loss persisting past the
  settle dwell — or, for a single-endpoint peer, its sole endpoint down past the dwell — raises
  the signal (R267). The concentrator never uses this — its
  peers learn remotes from inbound. The **concentrator-role dead-peer reclaim** (the D50
  `peerTeardownMonitor`, which sheds a dead edge's per-peer resequencer and demux
  state on session loss) is **inert on the edge role**: a multi-exit edge's standby
  peers are healthy warm standbys by design even while carrying no data, so
  `concentratorMonitoredPeers` returns an empty set off the concentrator and no warm
  standby is ever torn down.
- **demuxes multiple peers by authenticated source binding (G4 multi-peer)** — on a
  concentrator with more than one configured peer, inbound datagrams are routed to
  the owning peer via `peerBySource`, an atomic-pointer map keyed by the full source
  **`AddrPort` (address+port)** and populated only from authenticated PROBE frames.
  Keying on the AddrPort — not the bare address — lets two peers behind ONE public IP
  (CGNAT, distinct source ports) bind and demux independently. Each peer authenticates
  with its own per-peer `psk`: the first PROBE from a source that MAC-verifies under a
  peer's psk binds that source to that peer; subsequent frames from the same
  source are decoded under that peer's codec alone, without a trial over the
  other peers. The map is bounded by a global cap and a **per-peer quota**
  (`maxDemuxSources/len(peers)`, floor 1): a party holding one valid psk that floods
  spoofed sources exhausts only its own quota and never starves another peer's
  bootstrap PROBE. A peer that roams across CGNAT source ports past its own quota
  evicts its OWN oldest binding (LRU) to admit the new one — it is never dropped and
  never disturbs another peer's slot. With a single configured peer, a per-peer `psk` is **rejected** at
  config load (`config.validate`) and the top-level `psk` is the sole
  authenticator, byte-identical to pre-G4 behavior. Once a second peer is
  configured, per-peer `psk` becomes **required and pairwise-distinct**, and the
  top-level `psk` — still required by validation — **authenticates no peer**
  (`device.Up` feeds only each peer's own PSK, from `Config.PeerIdentities`, into
  the bind). Binding is learned only from PROBE frames — a CONTROL frame
  cannot establish or move a source-to-peer binding (D9/D11).
  Per-peer `name` is required in multi-peer mode and exposed as the metrics `peer`
  label for **every** bound peer, including the first/primary one: `device.Up`
  plumbs the primary's configured name into the bind
  (`bind.Multipath.SetPrimaryPeerName`) whenever a second peer is configured, so
  `peer=""` appears only on a true single-peer edge/hub/concentrator (D58).
- owns the **per-path UDP sockets**, their byte and error counters, each peer's
  transport instance (`adaptivePeer`) and resequencer, and the single
  engine-facing receive function that drains them.
- keeps, per view, a **return-address table** for the probe plane, keyed by the
  sender's path id (D94/T246). Only authenticated PROBE frames establish or
  refresh an entry (requests under the sender's stamped path id, echoes under
  the path's own id). One entry is *selected* as the destination of this
  path's own probes and PMTU probes. Selection is sticky — established by the
  first probe (cold start), moved only by a one-time DEAD fallback after
  `2 × DefaultDownAfter` of probe silence on the selected entry, or by the
  explicit `SetPeerRemote`/`SetPeerRemoteFor` override — and the roam callback
  (PMTU re-probe) fires only when the selected address changes. The transport
  does not send to the selected entry: each lane has its own route, the socket
  and the source address its hello arrived from, so a concentrator with one
  socket reaches each of an edge's WANs separately.
- **selects, per path, HOW its socket binds to the network** (`bind`, I5,
  Q42/`internal/bind/pathsock.go`'s `selectDeviceBinds`). Three modes, resolved
  per path from the path's own `bind` or, when that is omitted, the top-level
  default (itself `"auto"` when also omitted, matching pre-T105 behavior
  byte-for-byte): `"auto"` reproduces the original heuristic — device-bind
  (`SO_BINDTODEVICE`, wildcard source) only when provably equivalent to
  pinning `source_addr` (the address is the *sole* owner of its interface, so
  a device bind and a source-IP bind reach the same place), source-IP-bind
  otherwise; `"source"` forces the pre-T16 source-IP pin unconditionally; and
  `"device"` forces a device bind unconditionally. `"source"` is the fix for a
  one-address-per-VLAN policy-routing edge (each VLAN sub-interface is the
  *sole* address on its own device, so `"auto"`'s equivalence heuristic
  device-binds it — losing the source-based `ip rule` selector the operator's
  routing depends on); see [install.md §3b](install.md#3b-policy-routing-edge-topologies-source-ip-pinning-with-bind--source).
  A `"device"` path whose device bind cannot be honored — its `source_addr`
  resolves to no live interface, or the resolved interface's `SO_BINDTODEVICE`
  setsockopt fails (pre-5.7 kernel, permission) — silently fell back to
  source-IP pinning pre-D53, dropping the operator's roam-survival choice with
  no signal. `NewMultipath` now takes a component-scoped `log.Logger`
  (`log.Component("bind")`) and WARNs at both fallback points, naming the path
  and the (resolved or empty) interface, so the operator sees the roam
  property was lost; the same setsockopt fallback for an `"auto"`-selected
  device bind — never an operator's explicit choice — logs at INFO instead
  (D53). The WARN fires **only once a source-IP-pinned socket has actually
  materialized AND been installed as a live path** — a claim of "falling back
  to source-IP pinning" while the fallback bind itself also failed (the path
  stays `DEFERRED`, no socket at all) would be false, and that case instead
  logs a distinct, non-fallback-claiming "still deferred" WARN. A THIRD case
  — the fallback bind succeeds but installing the resulting socket into the
  running bond then fails (a peer-fan-out wiring defect) — logs
  NEITHER WARN: the T55 background reconciler closes that socket and keeps
  the path deferred for a clean retry next tick, so claiming a fallback that
  was never actually wired in would be equally false (round 3). The
  equivalent ordering applies at `Open()` and `AddPath()`, whose fallback
  WARNs likewise wait until the path is fully wired into every bound peer,
  not merely bound — a failure there aborts the whole call instead of
  retrying, so nothing is silently kept half-admitted either way. The
  still-deferred WARN is deduplicated
  **per condition-transition**, not per background-reconcile tick: the T55
  reconciler (`StartReconcileLoop`/`reconcileDeferred`) retries a deferred
  path every `DefaultReconcileInterval` (1s), and a persistently-unresolvable
  interface — a mobile edge before its DHCP lease, Starlink mid-obstruction —
  WARNs once for the whole deferral window rather than flooding the log at
  1 Hz; the latch clears (re-arming a fresh WARN) the moment the interface
  resolves or the fallback starts working, so a later re-roam that drops the
  interface again is reported too.
- **tolerates a not-yet-assignable `source_addr` at startup** (`Open()`). A path
  whose *well-formed* `source_addr` no interface holds yet — a mobile edge booting
  before its 5G modem has a DHCP lease, Starlink mid-obstruction — makes
  `net.ListenUDP` return `EADDRNOTAVAIL`. Rather than tear the whole bond down,
  `Open()` brings the tunnel up on the paths that **do** bind and *defers* the
  unbindable ones: a deferred path is recorded (with its boot prober) and left
  `Down` — its prober never echoes and no lane is learned on it, exactly as for
  a live-but-silent path. Hard guards: if **zero**
  paths bind, `Open()` still fails fatally (no transport ⇒ no tunnel); a
  **malformed** `source_addr` remains a hard config-load error (`config.validate`
  rejects it at load); and any bind error that is **not** `EADDRNOTAVAIL`
  (`EADDRINUSE`, permission) stays fatal. Startup and the runtime model are
  symmetric: a SIGHUP reload that introduces a not-yet-assignable path *defers* it
  the same way (`AddPath`), a reload that keeps a deferred path is a no-op for it
  (`PathNames` reports the durable membership, deferred paths included), and a reload
  that drops one retires it (`RemovePath`) — so a deferred path never regresses the
  SIGHUP-no-op invariant.
- **reconciles a deferred path in the background** (`StartReconcileLoop`, T55). A
  device-lifecycle goroutine (started after the first `Open`, stopped before `Close`
  by the same `Tunnel.Close` that stops the probe loop — no goroutine leak) polls the
  deferred set at `DefaultReconcileInterval` (1 s) and re-attempts each deferred
  path's bind. When a path's `source_addr` **becomes assignable** (its interface/
  address appears — the 5G modem finally got its DHCP lease), the reconcile **binds
  and promotes** it to a live path: it enters `m.paths` and gets its own reader,
  **reusing the preserved boot prober** so the path keeps its reserved id-stamp
  (no renumber, no peer-reflector collision) — the transport learns a lane on it
  from its first hello exchange, WITHOUT a `Close→Open` restart, as for any
  runtime `AddPath`. A path
  that still cannot bind stays deferred and is retried; a path REMOVED before it binds
  (`RemovePath`) is dropped from the deferred set and never promoted. Membership
  publication runs under `m.mu`; the transport transition mutex serializes it with
  `Open`/`Close`/`AddPath`/`RemovePath`, and a failed promotion's socket
  retirement runs after `m.mu` is released. It is a no-op on a closed bind.
  **Mechanism:** a bounded periodic poll, chosen over
  event-driven netlink route/addr subscription (`vishvananda/netlink AddrSubscribe`)
  because netlink is not an existing dependency and the deferred set is normally empty
  — so the steady-state tick is a single mutex-guarded length check. The full
  absent-then-added path flow over a real interface is validated by a netns e2e (T60).

This package is also the **amnezia boundary** (`bind.go`, above).

### Concentrator hub failover — `internal/device` (`failover.go`, T57)

Two *different* failovers exist and must not be conflated:

- **Per-path failover** (the transport, above): one uplink to the *active*
  concentrator dies; the transport stops using that lane when its
  acknowledgements stall or its hello lease expires, and repairs what was
  outstanding over another lane. The WG session is untouched. This is the
  common case.
- **Hub failover** (this section): the *concentrator itself* is unreachable —
  **every** path's liveness to the active concentrator endpoint is DOWN
  simultaneously (HUB LOSS). No surviving uplink can reach it, so switching
  uplinks cannot help; the edge must move to a *standby concentrator*.

An edge peer carries an **ordered** concentrator endpoint list
(`config.Peer.EndpointSpecs`, Q18/T54/Q35): index 0 is the active/primary hub, the
rest are ordered standbys. Each entry is either an **IP:port literal** or, behind
the peer's explicit `dns = true` opt-in (Q29), a **hostname:port** whose record set
is resolved at runtime (see *Re-resolution* below). All hubs in the set share the
peer's **single WireGuard static key**, so the same peer identity re-handshakes
against whichever hub is active.

The controller (`hubFailover`) runs a device-lifecycle monitor loop (started after
`dev.Up`, stopped before `dev.Close`, alongside the probe/reconcile loops):

1. **Detect** hub loss off the **existing per-path liveness plane** — each
   path's `telemetry.Prober` `State()` — as *every* path reporting `StateDown`.
   No second detector.
2. **Advance** to the next endpoint in the ordered list and **repoint every
   path's remote** at it via `bind.Multipath.SetPeerRemote` (a uniform override —
   a hub switch retargets the whole bond; it supersedes any per-path `dest_addr`).
   This changes only the per-path fan-out *beneath* the engine's single virtual
   endpoint — **invariant A1 holds** (the engine still sees one peer, no
   per-packet endpoint churn).
3. **Re-handshake**: expire the peer's current keypairs (a **fresh** session —
   **no hub-to-hub state handoff**) and send a fresh handshake initiation toward
   the just-repointed standby. This is the only engine-*peer* coupling the
   failover path takes; it lives in `internal/device` next to the rest of the
   engine wiring (the `conn`-seam isolation of `bind.go` is unaffected).
4. **Start a new transport epoch**: `SetPeerRemote` makes the peer's transport
   forget its lanes. The standby is a *separate process*, so the hello in its
   probes names a new boot epoch; the edge adopts it only from a probe that
   returns the edge's current challenge. On adoption the transport restarts
   its sequences at 1, expires what was outstanding towards the old hub, and
   `bind/adaptive.go` re-baselines the receive resequencer to sequence 1
   (`Resequencer.RebaselineAt`), discarding the dead hub's buffered datagrams
   while leaving already-delivered ones untouched. Lanes to the standby are
   learned from the same hello exchange.
5. **Re-arm** against the new endpoint: probes now flow to it, so if it too is
   fully down the controller advances again.

**Settle dwell.** After a switch (and at boot for endpoint 0) the newly-selected
endpoint gets a fixed dwell (`hubFailoverSettle`, 3 s) to prove itself LIVE before
another advance is allowed. It comfortably exceeds the liveness UP-recovery latency
(~3 echoes × 200 ms ≈ 600 ms once probes reach a reachable hub), so a still-DOWN
reading caused merely by echoes not having returned yet cannot skip past a healthy
standby, and it bounds the re-advance cadence (one switch / one handshake per dwell)
while a whole hub fleet is down.

**End-of-list policy: WRAP** (round-robin modulo the list length). Once the last
standby is exhausted the controller cycles back to index 0 and keeps retrying every
endpoint in order. Wrap is chosen over *stop* to preserve availability — a hub that
recovers earlier in the list is retried and settled on within one cycle, whereas
stopping at the last endpoint would strand the edge on a dead hub even after
endpoint 0 came back. The settle dwell keeps the round-robin a slow, bounded retry,
not a storm.

**GUARD (must-hold invariant).** A **single-endpoint** list takes **no** failover
action — no advance, no remote repoint, no re-handshake. A one-concentrator
deployment (including the legacy single `endpoint` form, normalized to a
one-element list) is therefore byte-for-byte the pre-T57 behaviour. The switch and
this guard are validated by the real-network netns e2e (`TestHubFailoverStandbySwitch`
+ `TestHubFailoverSingleEndpointGuard`, T62) and, over the real internet, by the
realhosts mid-transfer WAN-kill tier (`TestRealMidTransferWANKill`, T63).

**Re-resolution** (`resolution`, T73). A hostname endpoint spec has no fixed
address; its expansion is a **mutable, spec-keyed record set** the failover set
carries (`failoverSpec.addrs`, updated in place by `hubFailover.updateResolution`
under the endpoint-set lock — the sole mutation point). The re-resolution
controller keeps those record sets fresh. It mirrors the `hubFailover` shape (a pure
constructor over an injected `dnsresolve.Resolver`, the failover controller it
drives, a `telemetry.Clock`, and the `[dns]` poll interval + per-lookup timeout) and
runs its own device-lifecycle loop **off the send hot path** — all lookups happen on
its goroutine; results are applied only through `updateResolution`. Each evaluation:

- **Poll** every hostname spec on the fixed `[dns]` cadence; on a **successful,
  non-empty** lookup the addrs are **family-filtered then ordered** — addrs of a
  family no local path can source (a path binds a socket whose family is fixed by its
  `source_addr`, so an AAAA answer on a v4-only edge is unreachable and is dropped),
  then IPv4 first, then IPv6, deduped — a deterministic order so an unchanged answer
  yields a byte-identical expansion. The result is handed to `updateResolution`, which
  **repoints only on an actual active-IP change** (D32 no-op suppression). When the
  transport exposes a TTL (DoH/DoT), the next poll is clamped to
  `min(pollInterval, minTTL)`.
- **Liveness-loss trigger**: the instant every path to the **active** endpoint reads
  `StateDown` — the *same* `allDown` sweep the failover loop advances on (Q34: the
  two controllers coordinate purely through the shared lock and the update API) — the
  active spec is re-resolved **out of band**, edge-triggered, without waiting for the
  next poll tick. This out-of-band re-arm is clamped to `min(pollInterval, minTTL)`
  too, so record freshness holds on exactly the hub-loss path where it matters most.
- **Retention invariant (D46)**: a lookup that **fails** (error/timeout/NXDOMAIN) or
  yields an **empty** usable set — including an answer that **filters down to empty**
  because it carries no family any local path can source — **never publishes**: the
  spec keeps its last-good expansion and the controller retries next tick. A transient
  resolver fault, or an answer for a family the edge cannot reach, therefore never
  tears down a working endpoint set, and `hubFailover` never sees a previously-resolved
  active spec collapse to empty (the condition its `total < 2` guard could otherwise
  strand the bond on).

This controller runs **even for a single-hostname peer** (to track a changing DDNS
address), independent of hub-failover's `>= 2` guard; the first successful resolution
of a hostname-only peer is what boot-adopts its active endpoint.

**Device lifecycle** (T74). At `Up` the device does one **bounded initial resolve**
of each hostname spec (the `[dns]` per-lookup timeout) and builds the engine/UAPI peer
endpoint **only from resolved entries** — the flattened head of the seeded specs. If a
name does not resolve in the boot window (single-hostname peer, resolver down), the
tunnel comes up **without a peer endpoint** (tolerant boot, Q30 defer-and-reconcile —
an unresolvable name never hard-fails bring-up); the concentrator already runs
endpoint-less, so the engine supports it. The resolver is constructed **once**, and
**only when some peer carries a hostname spec** — a zero-hostname config builds no
resolver and starts no loop (Q29 inertness). The **first-resolve install path (R70)**
is load-bearing: `SetPeerRemote` repoints the bind's per-path remotes but **never sets
the engine peer's endpoint**, which is populated **only** by a UAPI `endpoint=` line
routed through `Multipath.ParseEndpoint`. So after an endpoint-less boot, the first
successful resolve must **install** the resolved endpoint on the engine peer via the
UAPI/IpcSet path (`deviceInstallEndpoint`) **then** re-handshake — the initiation now
has an addressable endpoint. Subsequent re-resolves of an already-installed peer take
the normal `SetPeerRemote` repoint path (the engine's virtual endpoint stays stable per
A1; only the bind remotes move). On a **multi-exit edge** the per-peer analogue is
`SetPeerRemoteFor(peerName, ap)` (T252): it repoints ONLY the named peer's remotes and
resets only that peer's lanes, without disturbing the bind-global `defaultRemote` another
controller's peer relies on, and — because it also updates that peer's `configuredRemote`/`edgePeerByRemote`
keying — a re-resolve or first install of a NON-primary peer re-seeds and resolves to that
peer's OWN virt across a Close/Open cycle (D101) instead of mis-resolving to the primary's
(D100); it fails fast (no state mutated) if two peers would map to one `addr:port`, since
`edgePeerByRemote` cannot key both. T253 routes each per-concentrator controller's
install/repoint through it. The
re-resolution loop's stopper is held on the
`Tunnel` and invoked by `Close` between the hub-failover stop and the engine teardown.
The whole flow — endpoint-less boot while the name is unresolvable, the R70
first-resolve install, a mid-session concentrator-IP change, and the re-resolve repoint
after which traffic resumes over lanes learned from the new address — is validated end
to end by the privileged netns e2e
`TestDNSHubResolveAndReroute` (Q36), with a hermetic in-namespace UDP DNS responder as
the sole answer source (no external DNS egress).

**Boot-time forced initiation (D37/T120).** A THIRD, unrelated mechanism also lives in
`failover.go`: `startFirstPathUpHandshake`, wired in `device.go`'s `up()` for the edge
role only (a no-op for the concentrator, which is the responder to every edge and
initiates nothing). It is **not** part of hub failover or re-resolution — it fires
**once**, at most, per tunnel lifetime, on the bind's `Multipath.SetOnFirstPathUp`
latch (T117), regardless of whether the peer's endpoint list has one entry or many.
On a **multi-exit edge** (T251/Q68b) the single latch initiates to **every** configured
concentrator peer, not just the primary (`deviceRehandshakeAllPeers` composes one
`deviceRehandshake` per peer), so all N sessions are driven warm concurrently the instant
the first uplink is usable; a single-peer edge composes exactly one, byte-identical to before.
The problem it addresses: the engine's own boot-time handshake initiation can race
`bind.Multipath.Open` — issued before any path telemetry exists, it may hit
`bind: no healthy path` and get dropped, yet the engine still stamps
`peer.lastSentHandshake`, so a bare retry moments later can be silently suppressed by
the engine's own `RekeyTimeout` guard, leaving the tunnel waiting out that ~5 s
retransmit timer instead of re-initiating the instant a path is actually usable. The
callback reuses the `deviceRehandshake` pattern (`ExpireCurrentKeypairs` backdates
`lastSentHandshake` so the immediately-following `SendHandshakeInitiation` is never
suppressed; a no-op on a cold boot with no keypairs yet). It **must** be registered
before the probe loop starts (the latch's edge is not retroactive), so `up()` wires it
right after the engine is constructed, well before `StartProbeLoop`.

**Initiation after a concentrator restart.** A restarted concentrator has lost its
sessions and, as the responder, starts none. The engine on the edge notices only when
its new-handshake timer fires (`KeepaliveTimeout` + `RekeyTimeout`, 15 s), and until then
sends under a session the concentrator cannot decrypt. The transport already
learns the peer's process epoch from authenticated probe payloads; when an adopted epoch
names a different process than the one known before, the bind calls
`Multipath.SetOnPeerRestart`'s callback with the peer's name, and
`startPeerRestartHandshake` (edge role only) runs that peer's `deviceRehandshake`. First
contact and a new generation of the same process do not trigger it.

### Per-path telemetry — `internal/telemetry`

Measures per-path quality (RTT, loss, jitter) by exchanging authenticated PROBE
frames, and derives each path's liveness verdict. A `Prober` emits one probe
per path every `DefaultProbeInterval` (200 ms); the peer's `Reflector` echoes
it. Probe anti-replay is a per-path high-water on `ProbeSeq` (`AntiReplay`)
within the sender's per-boot session; a new session is adopted only from a
probe that returns the reflector's current challenge, so a replayed probe
cannot reset the high-water. (`ControlGuard`, a per-type high-water for CONTROL
frames, remains in the package without a production caller: the transport
checks its CONTROL frames against its own per-lane windows.)

Ordinary probes and their echoes also carry the transport's hello
(`bind/probe.go`, `dispatchInbound`), so the probe exchange is what establishes
and renews lanes.

**What liveness drives.** A path's verdict (`wanbond_path_up`) feeds hub
failover (every path to the active concentrator down), the exit selector's
health and RTT inputs, the TUN MTU resizer (the minimum over up paths), PMTU
re-probing on a DOWN→UP transition, and the concentrator's dead-peer reclaim.
It does not select lanes. The transport keeps its own state per lane — a lease
renewed by each authenticated hello and expiring after one second, and a stall
when a datagram's repair timer passes with no acknowledgement since it was
sent — and so moves traffic off a failing path within its repair timer, not
after `down_after`.

**Config surface for the up/down threshold (D86, T203/T207).** The compiled-in
defaults (`telemetry.DefaultDownAfter` = 1200ms, `telemetry.DefaultProbeInterval`
= 200ms, fixed) are overridable: an optional `[liveness]` block's `down_after`
replaces the silence threshold that marks an UP path DOWN, and an optional
per-path `ride_through` on `[[paths]]` (default 0) is added to it for that
path, so the path goes DOWN after `down_after + ride_through` of silence
(`device.proberConfigForPath`). `internal/config` cannot import
`internal/telemetry` (the reverse import already exists, via `probe.go`), so
`defaultLivenessDownAfter`/`livenessProbeInterval` are restated in
`internal/config/liveness.go` with an explicit cross-reference. `down_after`
is rejected below `2*livenessProbeInterval` (400ms): fewer than two probe
intervals cannot even carry one round-trip echo, so the liveness `Tick`'s
silence check would outrun the echo cadence and every path would permanently
flap DOWN.

**Upper-side WARN-and-allow liveness budget (D86 decision 4, T211).** The
lower floor is a hard reject; the UPPER side is soft. The 3s recovery deadline
is `telemetry.RecoveryBudget`, alongside a pure derivation
`telemetry.FailoverBudget(downAfter, rideThrough, probeInterval) =
downAfter + rideThrough + 2*probeInterval` (the bound on how long a dead path
takes to read DOWN). At load, `normalize()` computes a
`Config.LivenessBudgetSane` verdict — `FailoverBudget(down_after, max path
ride_through, livenessProbeInterval) <= RecoveryBudget`. It NEVER rejects an
over-budget `down_after`/`ride_through`; instead the daemon logs ONE startup
WARN naming the numbers and exports a static, unlabeled
`wanbond_liveness_budget_sane` gauge (1 = within budget, 0 = over), seeded at
startup and re-set on a reload whose applied path change moves the worst-case
`ride_through`. The budget bounds what liveness drives (above), hub failover
first of all; it is not the transport's per-lane reaction time.

**Unexpected originating-PROBE socket write failures are counted (D96 item 4).**
`emitProbes` (`internal/bind/probe.go`) writes each path's ordinary or PMTU
originating PROBE directly to its socket. An unexpected failure (a concurrent
`Close` racing the probe-loop goroutine, or a transient socket error)
increments the per-path `probeSendErrors` atomic. For PMTU the same error is
returned to discovery so the search stays unconverged; for an ordinary probe
it is counted then discarded so other paths' cadence work continues. Expected
PMTU `EMSGSIZE` is different: it maps to the search's benign too-large verdict
and is excluded from this counter. The atomic is threaded through
`bind.PathTraffic` → `metrics.PathSnapshot` and exposed as
`wanbond_path_probe_send_errors_total`. Refused writes of the transport's own
datagrams are counted separately as `wanbond_path_socket_write_errors_total`.

### Receive resequencer — `internal/reseq`

Bonding across paths of different latency reorders packets. The resequencer
holds a **bounded window** with a timeout and restores the order of the bulk
class **before** the inner WireGuard anti-replay window sees the traffic. It
runs on the transport's **bulk delivery sequence** (the order field of a data
frame) and never touches the inner WireGuard counter (a core invariant). Small
datagrams carry their own sequence and bypass it (see *Receive ordering
decision* above).

One resequencer exists per peer, with a window of `resequencerWindow` (32768)
sequences and a timeout of `resequencerTimeout` (250 ms), both in
`internal/bind/multipath.go`. Its input is already authenticated and
deduplicated by the transport; `bind/adaptive.go` feeds it through
`Observe`. The delivering lane plays no part in the release decision.

- **Ordering.** Releases are strictly ascending. A sequence below the release
  point is dropped, never delivered late.
- **Exactly once.** A duplicate is dropped.
- **Bounded memory.** At most `window` datagrams are buffered. A sequence at or
  beyond `next + window` advances the release point: what is buffered below the
  new base is released in order and the gaps are counted as skipped.
- **Progress.** A gap at the release point is held for at most the hold bound,
  then skipped, and the run behind it is released.

**Head-of-line hold.** When a gap opens at the release point the resequencer
holds the datagrams behind it until the missing one arrives — as a repair, or
as a straggler on a slower lane — or the hold expires. The hold bound is
250 ms, the sender's repair lifetime: `bind/adaptive.go` requests
`adaptiveReorderHold` (300 ms) and `SetHoldBound` clamps a request to
`[holdBoundFloor` (10 ms)`, resequencerTimeout` (250 ms)`]`. A gap's deadline
is measured from the first observation of the oldest datagram still buffered
behind it, not from the moment the gap reached the head: several gaps that are
already due release in one pass, and a gap exposed behind an earlier one keeps
its remaining time instead of a fresh full hold. The buffered sequences are
listed in order of arrival, so the oldest is found without looking through the
window. A hold is armed on every datagram for as long as one that came early by
a faster lane waits for those sent before it on a slower one, each of which
fills the gap at the head and exposes the next; a scan of the 32768 slots each
time held an edge on a Raspberry Pi 5 to 3800 datagrams a second, 5.1 MB/s,
whatever the links carried (production, 2026-10-02;
`TestGapFillsBehindAnEarlyFrameDoNotScanTheWindow`).

Every gap waits for its hold, whichever lanes delivered the datagrams behind
it.

**Re-anchoring.** Sequence numbers belong to a pair of authenticated endpoint
epochs. When the transport adopts a new remote epoch — the peer restarted, the
edge switched to a standby concentrator, or the peer's Bind reopened — it
restarts its sequences at 1, and `bind/adaptive.go` calls
`Resequencer.RebaselineAt(1)`: buffered datagrams of the old epoch are
discarded, already released ones are untouched, and the release point starts
at 1 even when datagram 2 arrives first.
`wanbond_resequencer_rebaselines_total` counts these.

The resequencer also keeps a discontinuity guard: a sequence more than one
window below the release point, or `resyncFactor` (4) windows or more above
it, is *suspect* and dropped
(`wanbond_resequencer_dropped_suspect_frames_total`), and `resyncCorroborate`
(3) distinct suspect sequences within one window of each other re-pin the
release point (`wanbond_resequencer_resyncs_total`). A sequence within one
window below the release point is *stale*
(`wanbond_resequencer_dropped_stale_frames_total`).

**Metrics** (documented in [runbook.md](runbook.md#series-to-watch)):
`wanbond_resequencer_hol_holds_total` and
`wanbond_resequencer_hol_hold_seconds_total` are the count and the total
duration of armed holds; their ratio is the mean hold.
`wanbond_resequencer_gap_fills_total` counts holds that ended because the gap
was filled, `wanbond_resequencer_deadline_wakeups_total` those evaluated at or
after their deadline, and `wanbond_resequencer_skipped_seqs_total` the
sequences given up. `wanbond_resequencer_armed_deadline_timestamp_seconds` and
`wanbond_resequencer_armed_window_seconds` describe the hold currently armed.

### TUN lifecycle: persistence, the default-route exception, and the session signal — `internal/device`

Device-lifecycle surfaces beyond tunnel bring-up/teardown itself, all owned
by `internal/device`:

- **TUN persistence** (`tun_persist`, I7/Q38, `persist_linux.go`). By default
  `wanbond0` is a non-persistent TUN: the kernel destroys it when the daemon's
  last file descriptor closes on `Close`, so every restart drops the
  operator-owned addresses/routes/rules attached to it. `tun_persist = true`
  makes `device.Up` issue `TUNSETPERSIST` unconditionally with the config's
  value — `persist=false` explicitly *clears* the flag too, so a device left
  persistent by a prior `true` run and re-adopted under `false` reverts to
  non-persistent teardown, and a fresh TUN's `TUNSETPERSIST(0)` is a harmless
  no-op. amneziawg-go's `NativeTun.Close` only closes the fd/netlink socket —
  it never issues `RTM_DELLINK` — so this flag alone makes the link outlive
  `Close`; the next `Up` re-adopts the **same** persistent device by name via
  `CreateTUN`'s `TUNSETIFF`, preserving its ifindex, so operator-owned
  addressing survives untouched. Persistence does **not** exempt the interface
  from NetworkManager (D39) — an NM host still needs the unmanaged-devices
  drop-in regardless of `tun_persist`.
- **Removal of an obsolete TUN shaper** (`tunshaper_linux.go`). Builds that
  paced the TUN installed an HTB root `1:` with a `bfifo` `10:` leaf under
  class `1:1` on `wanbond0`. A persistent interface keeps its queue discipline
  across restarts and upgrades, so on Linux `device.Up` lists the interface's
  queue disciplines (`tc -j qdisc show dev wanbond0`) and, when exactly that
  arrangement is present, deletes the root and logs `removed obsolete TUN
  shaper`. Any other queue discipline is the operator's and is left alone. The
  check runs at every start and needs `tc` (looked up on `PATH`, then
  `/usr/sbin/tc` and `/sbin/tc`); a missing `tc` or a failing `tc` call fails
  bring-up.
- **The default-route routing exception** (`mode = "default-route"`, I6/Q41,
  `route_linux.go`/`splitDefaultRoute` in `device.go`). Elsewhere in this
  document and in [install.md](install.md), wanbond's interface-ownership
  posture is: the daemon creates and brings up `wanbond0` but otherwise **never
  assigns addresses and installs no routes** — addressing and routing stay
  operator-owned. `mode = "default-route"` is the **one deliberate exception**:
  an edge-only opt-in, rejected on the concentrator, that marks a peer as an
  exit-capable full-tunnel concentrator (since T250 SEVERAL edge peers may
  carry the mode as alternates for the same egress role; the first in config
  order is the boot-default exit). When set, once the interface is up the
  daemon installs the active exit peer's `allowed_ips` split — the wg-quick-style
  `/1`+`/1` pair for a literal `0.0.0.0/0`/`::/0` (the same split
  `uapiConfig`'s `splitDefaultRoute` already applies when rendering the
  engine's UAPI `allowed_ip=` lines, so the installed routes and the engine's
  notion of "what this peer owns" always agree) — as plain scope-link device
  routes via `wanbond0`, and withdraws them on stop. This is **routes only**:
  no policy-routing rules, SNAT, or concentrator `ip_forward`/`MASQUERADE`/
  `FORWARD` programming, which stay documented operator recipes (client-LAN
  full-tunnel: [install.md §9](install.md#9-full-tunnel--client-lan-recipe-c3);
  concentrator NAT/forwarding: [install.md §5](install.md#concentrator-natforwarding-prerequisites-for-routed-traffic-c6)).
  A literal `0.0.0.0/0`/`::/0` is never installed or handed to the engine —
  only its `/1`+`/1` split, so the encrypted underlay path to the concentrator
  endpoint itself is never captured by the tunnel's own default route.
- **The active-exit selector: warm standbys + allowed-ips-ownership switching**
  (`exitSelector`, T254/G28/M105, `exitselector.go`). With SEVERAL
  `mode = "default-route"` peers (T250 admits N exit-capable alternates for the
  same egress role), exactly ONE carries the default route at a time — the
  **active exit** — while the others are **warm standbys**. The switching model
  lives entirely in the engine's **allowed-ips trie**, NOT in kernel routes: all
  peers share the one `wanbond0` TUN and the split `/1`+`/1` scope-link routes
  point at that interface unconditionally (`defaultRoutePrefixes` is unchanged),
  so the trie alone decides which peer a default-route packet egresses to.
  - **Boot render** (`uapiConfig`/`standbyExitPeers`): the FIRST exit-capable peer
    in config order boots owning the `/1`+`/1` split (Q74 — the selection does not
    persist); every STANDBY exit peer boots with its default-route entries
    **stripped**, carrying only its non-default allowed_ips (its inner `/32`). This
    relies on the T250 **R255** validation rule — every exit-capable peer in an
    N>1 config must carry at least one non-default allowed_ip — so a stripped
    standby still renders `>= 1` allowed_ip and satisfies `uapiConfig`'s invariant;
    the only-`0.0.0.0/0` shape is config-rejected, so the standby render can rely on
    the guaranteed inner `/32` (kept warm end-to-end by keepalive, provable via the
    inner ping). The stripping is inert (render byte-identical) for the single-exit
    and concentrator shapes.
  - **Selection target**: top-level `exit = "auto"` is the edge default. A peer
    name fixes the startup selection; a monitor command changes the live target
    until restart. `exitMode` reports that target, while `activeExit` reports
    which peer currently owns the default route. In `auto`, the selector scans
    exits with established sessions, takes each exit's lowest RTT among paths
    in `StateUp`, and selects the lowest RTT (config order breaks ties). It
    reevaluates every five seconds and waits five minutes after an RTT-driven
    switch before another RTT-driven switch. Endpoint-list exhaustion retains
    its immediate health failover regardless of this cooldown.
  - **Switch** (`exitSelector.Switch(name)`): validates `name` is a configured
    exit-capable peer (a typed `*unknownExitError`, mutating nothing, otherwise),
    no-ops idempotently when the peer is already active, and otherwise issues ONE
    `IpcSet` inserting the target peer's `/1` splits. WireGuard's allowed-ips trie
    **steals each prefix from its current owner on insert** (steal-on-insert), so
    the previous owner loses the `/1`s atomically per prefix while BOTH peers keep
    their inner `/32`. The switch deliberately does **not** use
    `replace_allowed_ips` (which would wipe the target's inner `/32`) and issues **no
    re-handshake** — the target session is already warm. Every effected switch logs
    at Info with `from`/`to` and a `reason` — `reason=manual` for this operator
    path, `reason=auto-promotion` for the health-driven path below — so the two
    switch causes are distinguishable in logs; `ActiveExit()` reports the current
    owner for the monitor. The API is deliberately narrow (Q69): a single active
    exit, no split-by-destination policy hooks. T255 (composition) wires the
    monitor exposure onto this seam.
  - **Auto-promotion** (`exitSelector.onActiveExhausted`, T269/G28/M105,
    reverses the earlier no-auto-jump scoping per Q75): the selector subscribes to
    the CURRENTLY-ACTIVE exit's per-peer hub-failover **endpoint-list-exhaustion**
    signal (`SetOnExhausted`, T253) and, when that exit FULLY fails — every one of
    its configured endpoints tried and down, distinct from the within-concentrator
    single/partial-endpoint T57 failover (Q72) which stays inside the controller —
    automatically `Switch`es egress to the first **healthy** warm-standby
    exit-capable peer in config order (healthy = WG session established AND at least
    one path up), reusing the same steal-on-insert repoint (no re-handshake — the
    standby is already warm). Because T253's signal also fires for a
    single-endpoint concentrator (its sole endpoint allDown past the dwell),
    auto-promotion is exercised even for the minimal
    one-endpoint-per-concentrator config (R267). That minimal shape needs explicit
    wiring: a single-literal exit peer can never round-robin, so
    `peerNeedsHubFailover` alone would build it no controller. On a multi-exit edge
    `startFailoverAndResolution` therefore gives every exit-capable peer an
    **exhaustion-only controller** — one whose failover poll runs (via
    `startHubFailoverLoop`'s `exhaustionOnly` override of the `canFailoverLocked`
    gate) solely to raise the `total==1` exhaustion signal, taking no
    advance/repoint/rehandshake action. A single-peer edge, and any non-exit
    single-literal peer, still get no controller (behaviour-identical to pre-G5).
    The trigger is **re-subscribed on every switch** (manual or auto) so it always
    tracks the current active exit; a stale signal for a peer egress has already
    moved off is a no-op (an active-guard). The callback fires OUTSIDE the
    controller's own lock (`hubFailover.check` releases it before invoking the
    subscriber), so the selector taking its own lock — and re-subscribing, which
    re-takes controller locks — cannot deadlock against the firing poll loop
    (`s.mu` → `controller.mu` ordering, never inverted).
    A probe-cadence retry checks the current controller's latched exhaustion and
    live all-paths-down state, then promotes when a standby session becomes healthy
    after the one-shot exhaustion signal. Recovery of the active exit cancels that
    condition; `Close` stops the retry before engine teardown.
    - **MANUAL WINS**: an operator's manual switch during or after a promotion
      stands; auto-promotion never overrides a standing choice beyond moving egress
      off a dead exit.
    - **No failure-only failback**: recovery alone does not reverse a promotion.
      In fixed mode, the promoted exit stays active until it fails or the
      operator switches. In `auto`, RTT evaluation can select the recovered
      exit after the five-minute cooldown if it is the fastest healthy exit.
    - **No healthy standby**: if no warm standby is healthy, egress stays on the
      failed exit and the condition is logged. Promotion is retried while the
      active exit remains exhausted, without repeated no-standby logs.
    - **No persistence** (Q74): runtime mode changes and promotion do not rewrite
      the configured selection; restart restores `exit`, whose default is `auto`.
- **WG-session liveness signal** (`wanbond_session_established`, T101,
  `session.go`). The per-path liveness plane (probes) tells you a path's
  **transport** is reachable; it says nothing about whether the **inner**
  WireGuard session has actually converged. A `sessionMonitor` polls the
  engine's UAPI `IpcGet` peer dump at probe cadence and resolves each peer's
  last-handshake age against WireGuard's `RejectAfterTime` (the point past
  which a completed handshake's keypair is dead) into a binary verdict exposed
  as the `wanbond_session_established` gauge, plus one INFO `session
  established` log record emitted on each `0→1` edge (never repeated while the
  session stays up, so a live poll loop doesn't spam the log). This
  distinguishes a tunnel that is **still converging** (no completed handshake
  yet) from one that is **wedged** (a path reads up but the handshake is
  absent or has aged out) — a distinction `wanbond_path_up` alone cannot make.
  The monitor is stateless (a pure function of engine state + clock), reads
  through the UAPI seam only (there is no public accessor for a peer's
  last-handshake instant), and takes no WG-session coupling anywhere else in
  the bind — the Bind stays WG-unaware.
- **Per-peer WG-session snapshots** (`wanbond_peer_session_established{peer}`,
  T256/G28/M106, `session.go`). `sessionMonitor` above collapses every
  configured peer into ONE connection-scoped "is SOME session live" verdict —
  fine for a single-peer edge/hub, but insufficient for warm-standby
  promotion (the auto-promotion bullet above), which needs proof of session
  health for a SPECIFIC candidate concentrator, not "some session is live". A
  `peerSessionMonitor` generalizes the same UAPI-dump read to EVERY configured
  peer: it reuses `perPeerHandshakeNano` — the identical hex-pubkey dump parse
  `deviceExitHealth.healthy` (T269, the auto-promotion health check) already
  drives — so the parse logic is written once and shared by both the
  auto-promotion decision and this observability leg. The peer set is built by
  `allMonitoredPeers`, a generalization of `concentratorMonitoredPeers` (T126)
  from the concentrator-only non-primary teardown set to EVERY role and EVERY
  peer including the primary, following the same D58 primary-naming rule:
  `Peer` is `""` for a true single-peer config so `metrics.Source.PeerSessions()`
  still returns exactly one (back-compat) entry there, and each peer's own
  configured name once 2+ are bound — the same T94/D58 rule the path and
  resequencer series already follow. `metrics.Source.Session()` (the
  connection-scoped verdict above) is untouched.

### Supporting packages

- `internal/config` — loads the single TOML config, validates fail-fast at load
  (0600 perms, unknown keys, complete-or-absent amnezia block, the single
  accepted `scheduler.policy`, unique `source_addr`). The optional `[dns]` block selects the
  resolver transport (system default, DoH, or DoT) a peer's opt-in hostname
  endpoint is resolved through, enforcing the BOOTSTRAP-IP invariant (a
  hostname-form `doh_url`/`dot_server` requires an explicit `bootstrap_ip`;
  an IP-literal host rejects a non-empty `bootstrap_ip` as a mode mismatch)
  and constructing the matching `internal/dnsresolve` implementation — when
  `bootstrap_ip` is set, that implementation dials it directly instead of
  resolving the configured hostname through the system dialer.
- `internal/device` — brings a tunnel up from a validated config (Up/Down/Reload),
  wires metrics, handles SIGHUP path add/remove without teardown.
- `internal/metrics` — a private-registry Prometheus `/metrics` endpoint that
  **refuses any non-loopback bind**.
- `internal/monitor` — the monitoring-UI endpoint for the `[monitor]` surface,
  read-only EXCEPT authenticated `POST /api/exit` (T258; see
  *Security model* below): an embedded (`//go:embed all:dist`) Vite/TypeScript
  dashboard at `/`
  showing per-peer throughput/loss sparklines, fed by a `/ws` upgrade that
  pushes a fresh snapshot every 1s. The frontend follows `prefers-color-scheme`
  for automatic light/dark styling, with compact, square sections. The top bar
  holds the connection-scoped WG session and WebSocket freshness; grouped
  peers show their own session and handshake age inline with the peer heading.
  Path metrics use flat grids with expandable diagnostics.
  Loopback-only by default, like `/metrics`,
  but MAY bind non-loopback when a `token` is configured (see *Security
  model* below for the auth layer and the accepted residual risk). The
  `wanbond monitor` CLI subscribes to this same read-only `/ws` stream on
  the local host, using the token from the daemon's protected config. It does
  not add a second telemetry sampler or a control route, and works for either
  daemon role when the monitor endpoint is enabled. Its ANSI status and heading
  colors are selected locally for capable terminals and can be disabled with
  `--no-color` or `NO_COLOR`; color controls do not alter snapshot fields. The
  `MonitorSnapshot` wire contract (`monitor.go`) also carries a
  `DaemonSnapshot` (role, version, source commit/time, process uptime,
  always shown); per-path
  `bindMode`/`boundDevice` (runtime-resolved, via the `bind.PathTraffic`
  pass-through), shown on any binding; a truncated WireGuard public-key `wgPublicKeyFingerprint` (any
  binding — see *Security model*); a `peerSessions` array mirroring
  `metrics.Source.PeerSessions()` (T256/T257, G28/M106) — one entry per bound
  peer's own WG-session health (`peer`, `established`,
  `lastHandshakeSeconds`), following the same D58 peer-label back-compat rule
  as `peerNames`/`multiPeer`: `peer` is `""` throughout on a single-bound-peer
  config; an `activeExit` string (T254/T257) naming the exit-capable peer
  currently carrying the default route on a multi-exit edge — sourced from
  `exitSelector.ActiveExit()`, `""` on the concentrator role and on an edge
  with no default-route ownership to report, and (being a peer NAME, never an
  address) NOT part of the redactable surface; an `exitCapablePeers` array
  containing exactly the configured `mode = "default-route"` peer names in
  config order (empty off the edge role), which is the authoritative UI switch
  candidate set rather than an inference from generic telemetry; and the
  REDACTABLE surface
  gated by the server-side `revealAddressing` verdict: a per-path `addressing`
  block (`source`, `remote`) and an ordered `endpoints` hub-failover list
  (`peer`, `address`, `active`) whose addresses are omitted/blanked (while
  `peer` and `active` survive) and `addressingHidden` set true when addressing
  is NOT revealed — revealed meaning the monitor is loopback-bound OR the
  operator set the default-off, token-gated `[monitor] reveal_addressing`
  opt-in (see *Security model*). Since T257, `endpoints` is GROUPED per
  bound edge peer — each entry's `peer` names the owning peer's OWN
  hub-failover controller (`hubFailover.EndpointsSnapshot()`, keyed by stable
  peer name, T253), so a multi-exit edge renders one ordered active/standby
  section per peer side by side in configured order; `peer` is `""`
  throughout on a single-bound-peer config, keeping that shape byte-compatible
  with the pre-T257 flat list. `monitor.Info.Endpoints` and
  `monitor.Info.ActiveExit` are both LIVE per-snapshot providers (not captured
  once at construction), while `monitor.Info.ExitCapablePeers` is immutable
  config metadata copied into every snapshot. A hub failover or an exit switch/
  auto-promotion after startup is reflected in the next pushed frame.
- `internal/buildinfo` — source identity from Go's executable VCS metadata,
  or explicit Nix linker stamps when the build source lacks `.git`. The CLI
  resolves it once and passes `buildinfo.Info` through device construction
  into the monitor seam. `daemon.buildCommit` preserves the full revision
  and `-dirty`; `daemon.buildCommitTime` is UTC RFC3339 source commit time,
  not compilation time. Empty metadata displays `unknown`, and invalid
  supplied timestamps return an error. Explicit commits do not borrow a
  timestamp from another VCS revision. These identity fields remain visible
  under address redaction. Monitor views identify the daemon; `wanbond version`
  identifies the invoked executable. This adds monitoring JSON fields and
  does not change the transport's authenticated hello, DATA or ACK encoding.
- `internal/wireaudit` — the requirement-6 DPI wire-format audit (pcap parse +
  per-offset value-entropy + coverage checks) used by the P5 tests.
- `internal/log` — slog-based structured logging.
- `internal/dnsresolve` — the DNS resolution seam: a context-bounded `Resolver`
  interface, a system implementation over `net.Resolver`, two private-resolver
  transports sharing one dnsmessage encode/decode/error taxonomy, and an
  in-memory `FakeResolver` for tests. `DoHResolver` is DNS-over-HTTPS (RFC
  8484): wire-encoded with `golang.org/x/net/dns/dnsmessage`, POSTed over a
  dedicated `http.Client` with standard system CA trust (no
  insecure-skip-verify knob). `DoTResolver` is DNS-over-TLS (RFC 7858): one
  `crypto/tls` connection per lookup per family, queries framed with the
  2-byte length prefix, same system CA trust and server-name verification.
  Both query A and AAAA and tolerate one family answering NXDOMAIN when the
  other resolves, and both treat an empty final addr set as a typed error
  (`NXDomainError`/`NoDataError`), never a silent `([], nil)`. Residual leak
  for both: TLS SNI/timing to the configured provider.

## DNS endpoints and resolver privacy trade-offs

The optional `[dns]` block selects which transport resolves a peer's hostname
endpoint when opted in with `dns = true` per-peer. It is **opt-in by default —
[dns] alone never enables hostname resolution**; every peer endpoint is always
an IP literal unless explicitly marked `dns = true`. Hostname endpoints are
resolved through the OS system resolver by default (when `[dns]` is absent).

### Why default-off: the DPI thesis

A **pre-tunnel hostname lookup** is an unencrypted (cleartext) signal: an
on-path adversary sees the edge asking for the public concentrator's hostname
*before* the tunnel is up, making the host blocklistable at the DNS level
without inspecting any encrypted traffic. This is true regardless of how the
lookup is done — system resolver, DoH/DoT — because the resolver *itself*
learns the query. The default posture (IP literals only; hostnames deferred
to an explicit opt-in with `dns = true`) keeps this leakage off by default and
surfaces it as an intentional choice. The `[dns]` block is **optional** even
once a peer opts in — it only selects the resolver *transport*; an absent
block still resolves hostnames, through the OS system resolver.

### Leaked artifacts per resolver mode

Once a peer opts into hostname resolution (`dns = true`), the transport choice
in `[dns].resolver` determines what information escapes to a passive on-path
observer — per Requirement-6 testing (test/e2e/p5_dpi_test.go, Q29/Q33):

- **system** (default, OS stub resolver): A **cleartext DNS query naming the
  concentrator's hostname** — the full QNAME in plaintext on port 53. This is
  the most visible artifact: a DPI engine observes the exact hostname being
  resolved and can block it pre-emptively at the network edge.

- **DoH** (DNS-over-HTTPS, RFC 8484): The **TLS ClientHello SNI** (Server Name
  Indication, naming the DoH provider's host) **plus timing/connection metadata**
  to the DoH provider. The query payload itself is encrypted within the HTTPS
  tunnel, but the SNI and the observed request/response cadence allow timing-based
  inference and correlation to the DoH provider — the concentrator hostname is
  *not* visible on the wire.

- **DoT** (DNS-over-TLS, RFC 7858): Identical to DoH: the **TLS ClientHello SNI**
  (naming the DoT server) **plus timing and connection metadata** to the DoT
  provider. The query is encrypted, but SNI + timing correlates the edge to that
  resolver.

In all three cases, **multi-record expansions** (a hostname resolving to multiple
A/AAAA records, e.g. in a concentrator failover `endpoints` list) feed back into
`hubFailover`: each address in the result set becomes a separate entry in that
spec's slot of the ORDERED, ACTIVE-STANDBY failover list, so the edge can
advance within that set on hub loss (one address down → try the next). This
selection is **always** active-standby: the transport bonds the paths that
reach whichever single endpoint hub-failover has currently selected, never
several endpoints of one peer at once.

### Opt-in defer-and-reconcile boot semantics

A hostname endpoint that cannot be resolved at startup (resolver down, DNS
outage, network unreachable) **never blocks tunnel bring-up**. The tunnel
boots without that endpoint, and a background re-resolution loop (at the
cadence `[dns].poll_interval`, default 30s) installs it and initiates the
Noise handshake on the first successful lookup. Steady-state re-resolution then
repoints the bond only when the **active** endpoint's own AddrPort no longer
appears in its spec's freshly re-resolved, non-empty expansion (TTL-driven or
faster, per the operational `poll_interval`) — see "Change suppression" below
for the exact scope of that check.

- **Re-resolution cadence**: `[dns].poll_interval` (default 30s, must be > 0).
  Governs how often an opted-in hostname endpoint is re-resolved; changes are
  reconciled immediately.

- **Liveness-loss trigger**: the instant every path to the peer's **currently
  active** endpoint reads `StateDown` (the same `allPathsDown` sweep
  hub-failover advances on), the re-resolution controller re-resolves the
  **active spec, once**, out of band — an edge-triggered kick, not a sustained
  faster-cadence retry mode. It then re-arms the next scheduled poll at the
  normal TTL-clamped `poll_interval` delay; a sustained outage is covered by
  that regular poll loop, not by a shortened cadence.

- **Change suppression**: suppression is **active-survival-scoped, not
  set-identity-scoped**. `updateResolution` repoints only when the currently
  active AddrPort is **absent** from its spec's freshly re-resolved,
  non-empty expansion. Any re-resolution where the active AddrPort still
  appears anywhere in the new expansion — including a genuinely changed set
  that merely *added* an address, reordered the standbys, or dropped a
  different (non-active) address — takes no repoint and no re-handshake; only
  the derived flattened index is re-mapped. A repoint (one `SetPeerRemote` +
  one re-handshake) fires only when the active AddrPort disappears from its
  spec's new non-empty expansion, and it re-points to that expansion's new
  first entry.

### Mixing rules with ordered endpoints

When a peer declares multiple endpoints via the `endpoints` list (hub-failover),
and one or more are hostnames (`dns = true`), the following rules apply:

1. **Each hostname expands to its full A+AAAA record set** at resolve time.
   `orderAddrPorts` imposes only a family partition — IPv4 records first, then
   IPv6 — and **preserves the resolver's own encounter order within each
   family**; it does not sort by value. It also drops any address of a family
   no local path can source (a v4-only edge drops AAAA answers). Consequently
   the expansion is byte-identical tick after tick only when the resolver
   returns the same records in the same within-family order; a same-set but
   differently-ordered answer (e.g. DNS round-robin rotating the record order)
   yields a differently-ordered expansion, which reorders the standby advance
   sequence for that spec. Endpoint *selection* is always ordered
   **ACTIVE-STANDBY** — `hubFailover` advances through the flattened, ordered
   list on hub loss (see above).

2. **Order preservation**: the `endpoints` list order is **strict** — index 0
   is the active concentrator, index N are ordered standbys, and hub-failover
   advances through them in that order on hub loss. When a hostname at index K
   resolves to multiple addresses, they are inserted *consecutively* in that
   order (first address fills index K, subsequent addresses shift later
   entries right in the flattened ordering) — in the family-partitioned,
   within-family-resolver-order form described in rule 1, not a value-sorted
   order.

3. **Deduplication is per-namespace and load-time only; resolved addresses are
   never deduplicated across specs.** At config load, `resolveEndpoints`
   rejects a duplicate **within** each of two disjoint namespaces — a literal
   duplicating another literal, or a hostname:port duplicating another
   hostname:port — never across the two, and never on a resolved address (no
   resolution happens at load, Q30). At runtime, `orderAddrPorts` dedupes
   addresses only **within a single hostname spec's own** A+AAAA answer. Two
   *different* specs (two distinct hostnames, or a hostname and a literal)
   that happen to resolve or point to the same address:port are **not**
   rejected or merged — both remain distinct entries in the flattened
   failover list.

4. **Resolver selection is global**: `[dns].resolver` applies to *all* opted-in
   hostname endpoints — you cannot mix system + DoH + DoT within one config.
   Choose one transport per tunnel.

## Load-bearing invariants

These are the rules that keep the design correct; break them and the tunnel
misbehaves subtly. Agents and contributors must preserve them.

1. **One virtual endpoint per peer (A1).** The engine sees a single stable
   `Endpoint`; the Bind fans out beneath it. Never surface per-path endpoint
   churn to the engine.
2. **Own outer sequence space.** The transport and the resequencer use
   wanbond's own sequences; never reuse or perturb the inner WireGuard counter.
3. **Bulk resequencing precedes inner anti-replay.** The transport's bulk class
   is resequenced; its authenticated, deduplicated small-packet class bypasses
   bulk ordering as described above. Inner replay validation is never bypassed.
4. **Inner fail-closed; every outer frame authenticated.** WireGuard
   authenticates the payload; PROBE and CONTROL are PSK-HMAC authenticated with
   anti-replay checks: PROBE freshness is monotonic, the transport's data and
   ACKs use bounded sequence windows. Frame kinds 1 and 2 (the removed
   unauthenticated DATA/PARITY) are rejected and must not be reassigned.
5. **Amnezia `conn` transport coupling enters through `bind.go`.** Transport
   interfaces go through the type aliases and the flow-metadata contract
   there; other files use only `conn` constants and sentinel errors. The
   engine-generic source patch under `third_party/` contains no wanbond logic.
6. **Amnezia is all-or-nothing per device.** Config validation enforces the
   complete parameter set. The v3 engine keeps magic headers, paddings and
   junk parameters per `Device`, so concurrent engines do not share mutable
   protocol state.
7. **One transport, one overhead figure.** `internal/bond` is the only
   transport. A datagram reaches a peer only over a lane established by an
   authenticated hello exchange. `bond.Overhead` (101 bytes) is the full
   per-datagram cost of a data frame; `bind.InnerMTU`,
   `config.outerPathOverheadBytes` (161) and the TUN MTU derive from it and
   must change together.

## Security model

- **Payload**: confidentiality, integrity, authenticity provided by inner
  WireGuard (Noise + AEAD). wanbond never sees plaintext.
- **Outer frames** (PROBE, CONTROL): every frame is PSK-HMAC authenticated; a
  frame that does not verify is dropped at decode, and there is no
  unauthenticated frame kind. PROBE freshness is monotonic per path and
  session. The transport's data and ACKs travel in CONTROL and accept unseen
  reordered frames within bounded sequence windows scoped to the pair of
  endpoint epochs. A duplicate cannot renew liveness, update rate control or
  be delivered twice.
  - **Per-peer PSK (multi-peer concentrator, G4):** on a concentrator with more
    than one configured peer, each edge authenticates PROBE frames with its OWN
    per-peer `psk` — this field is REQUIRED and must be pairwise-distinct across
    peers (config load rejects a duplicate). The concentrator uses PROBE
    MAC-verification to learn each source `AddrPort`'s owning peer (`peerBySource`
    binding in `internal/bind/multipath.go`, keyed by address+port so CGNAT-shared
    IPs demux per-port); a source that MAC-verifies under peer A's psk is bound to A,
    and subsequent frames from it are decoded under A's codec alone. The top-level `psk` remains REQUIRED by
    config validation in every configuration, but on a multi-peer concentrator it
    authenticates **no peer**: `device.Up` feeds only each peer's own PSK (from
    `Config.PeerIdentities`) into the bind, so an existing single-peer edge does
    NOT keep authenticating via the top-level psk once a second peer is added —
    it must be given its own per-peer psk at that point. With a single configured
    peer, a per-peer `psk` is instead REJECTED at config load (not merely
    defaulted) and the top-level `psk` is the sole authenticator, so existing
    single-peer deployments parse and run identically unchanged.
  - **Shared concentrator socket.** A datagram from a source `AddrPort` with
    **no binding** is trial-decoded against each configured peer's codec
    (`O(peers)`, bounded by the static peer count, `demuxInbound`). Only an
    authenticated PROBE establishes a binding, so a CONTROL frame from an
    unbound source is dropped even when it verifies under some peer's psk. A
    datagram from a source **already bound** to a peer is decoded under that
    peer's codec alone and dropped unless it carries that peer's MAC. Neither
    case lets a party without a peer's psk reach that peer's transport or
    resequencer; the cost of a forged datagram is the decode attempt.
- **Traffic analysis / DPI**: the outer wire has no fingerprint (random nonce,
  obfuscated body, no magic bytes); AmneziaWG junk params add defense-in-depth.
  Protocol *mimicry* (looking like HTTPS) is an explicit non-goal.
- **Monitoring UI (`[monitor]`, G12)**: like `/metrics`, **loopback-only by
  default**; UNLIKE `/metrics`, a **non-loopback bind is fail-fast REFUSED at
  config load unless a `token` is set** (`ErrMonitorNonLoopbackWithoutAuth`,
  `internal/config/config.go`) — `/metrics` stays loopback-only unconditionally,
  with no such opt-in. Independent of that gate, EVERY request the endpoint
  serves — including the `/ws` WebSocket upgrade — passes unconditional Host +
  Origin validation (`hostAllowed`/`originAllowed`, `internal/monitor/server.go`),
  defending against DNS-rebinding and cross-origin/CSRF regardless of whether a
  token is configured. `[monitor].allowed_hosts` adds exact DNS names to the
  Host and Origin allowlist for a wildcard bind; it does not bypass token
  authentication. Unlisted names remain forbidden. When a token IS configured, it is presented once as
  `?token=…`; the server then sets a `wanbond_monitor_token` `SameSite=Strict`,
  `HttpOnly` cookie and 302-redirects to the same path with the query stripped,
  so the token does not linger in the URL bar or browser history. All token
  comparisons are constant-time (`crypto/subtle`).
  - **Control surface — read-only EXCEPT authenticated `POST /api/exit`
    (T258, G28/M106).** The dashboard is read-only in v1 with
    ONE deliberate exception: `POST /api/exit` (`internal/monitor/server.go`)
    switches the active exit-capable peer on a multi-exit edge, wired to
    `exitSelector.Switch` (`reason=manual`) through a `monitor.ExitSwitcher`
    callback the device injects (`Tunnel.switchActiveExit`) — the monitor package
    never imports `internal/device`, matching the `monitor.Info` provider-
    injection seam. Every OTHER route stays a pure read. The mutating route is
    protected in depth:
    - **Frontend widget (T260, G28/M107).** `web/src/dashboard.ts` renders a
      single `<select>` policy control with `auto` and the snapshot's authoritative
      config-order `exitCapablePeers` field — never by inferring candidates from
      generic `endpoints`/`peerSessions` telemetry — and issues the `POST` on
      selection via a same-origin `fetch` — no token/cookie handling in JS,
      the browser's `SameSite=Strict` cookie jar carries auth automatically
      (the `ws-client.ts` precedent). The control mirrors the server's gate
      client-side rather than relying on it: it is omitted off the edge role,
      when `!snapshot.exitControlAvailable` (loopback-bound or token-authenticated
      non-loopback; independent of `addressingHidden`) or whenever fewer than two exit-capable peers
      are configured (nothing to switch to). Pending
      state disables the `<select>` while the POST is in flight; a 2xx
      response adopts the returned `activeExit` optimistically (reconciled
      against the next real snapshot frame, which always wins); a non-2xx or
      network failure surfaces a visible error notice and leaves the prior
      `activeExit` in place. This client-side state is held outside the
      telemetry render. The exit control stays mounted across snapshots so an
      open native select and its keyboard focus survive live updates; its
      options change only when the configured candidate list changes.
    - **CLI client.** `wanbond set-exit <exit-peer>|auto` (`cmd/wanbond/setexit.go`)
      is a second client of the same route: it resolves the local monitor
      address and token from the daemon's protected config (shared with
      `wanbond monitor`), sends `Authorization: Bearer <token>` with no
      `Origin`, and reports the `{activeExit, exitMode}` response or the
      rejection status and message. It adds no server-side surface.
    - **Control availability.** A verified loopback binding permits local
      control. A non-loopback binding requires the configured token, checked by
      the existing auth middleware before the handler. The addressing-reveal
      setting is independent and never grants control by itself.
    - **The auth middleware covers it like every route.** Host/Origin validation
      (DNS-rebinding + cross-origin/CSRF defense) and, when configured, token
      gating apply to the whole mux, so a cross-origin `POST` is **403** and a
      missing/invalid credential is **401** before the handler runs. The
      `SameSite=Strict`, `HttpOnly` session cookie carries the `POST` on a
      same-origin fetch (the legitimate dashboard case).
    - **Method + input contract.** A non-`POST` method is **405** (the handler is
      registered for both `GET` and `POST` so a `GET` returns a clean 405 rather
      than falling through to the `/` static subtree); malformed JSON or an
      unknown / non-exit-capable peer is **400** (the selector's typed
      `*unknownExitError` adapted to `monitor.ErrUnknownExitPeer`, the body naming
      ONLY the caller-supplied peer, never selector internals); a successful
      switch — and an idempotent same-name switch — is **200**
      `{"activeExit": "<name>", "exitMode": "auto|<name>"}`.
  - **Addressing redaction gate (Q62/Q64) — server-side, not client-side.**
    Per-path `addressing` (`source`, `remote`) and the ordered, per-peer-grouped
    `endpoints` list's `address` values are the one REDACTABLE part of the
    extended wire contract (role/version/source identity/uptime/bind-mode/
    fingerprint/`peerSessions`/`activeExit`/`exitMode`/`exitCapablePeers` are NOT gated — see the
    `internal/monitor` bullet above). `monitor.NewServer`
    derives a `revealAddressing` verdict via **act-then-verify**:
    `verifyLoopbackBind(ln.Addr())` inspects the address the KERNEL actually
    bound (`net.Listen`'s own independent resolution), never the requested
    `listen` string or a pre-bind DNS lookup — the same TOCTOU-safe
    discipline the loopback bind guard itself uses. The verdict is `true` when
    the kernel bound a loopback interface OR the operator sets the default-off,
    token-gated `[monitor] reveal_addressing` opt-in. `BuildSnapshot`
    (`internal/monitor/monitor.go`) then performs the redaction BEFORE
    `json.Marshal`: when `revealAddressing` is false it omits every per-path
    `AddressingSnapshot` (a nil, `omitempty` pointer — never serialized),
    blanks every endpoint's `address` while preserving the peer grouping and
    ordered active/standby shape, and sets `addressingHidden=true`. **No
    tokenless-reveal path exists**: non-loopback `listen` without a `token`
    still fails at config load with `ErrMonitorNonLoopbackWithoutAuth`. A
    **token-authorized non-loopback bind redacts by default** (Q62) — the token
    gates *whether a non-loopback bind is permitted at all*, not whether
    addressing is revealed; the operator must explicitly opt into `reveal_addressing`
    to disclose addressing to token holders. The frontend only ever reads
    `addressingHidden`; it renders a placeholder and never attempts to reconstruct
    hidden values. Proven at the marshaled-JSON-bytes level
    (`TestBuildSnapshot_RedactsAddressingWhenNotRevealed` asserts no address
    substring appears anywhere in the serialized frame) and at the raw
    WebSocket-frame level in the e2e suite.
  - **WG public-key disclosure (Q63): fingerprint only, on any binding — no
    full key anywhere.** `wgPublicKeyFingerprint` is the truncated (~10
    base64 chars) local WireGuard public key, present in `DaemonSnapshot`
    regardless of binding or redaction state; the wire contract has
    deliberately **no full-key field** to gate (`web/src/types.ts`'s header
    comment enforces this: it MUST NOT add an optional `wgPublicKey?`). A
    public key is not secret in WireGuard's threat model, but the literal
    user answer to Q63 was the fingerprint-only option, not the more
    permissive "full key, loopback-only" alternative that was also on offer
    — so v1 ships strictly less disclosure than the security review's own
    recommendation.
  - **Concentrator edge-source addressing (Q64).** On the concentrator role,
    each connected edge's roamed source address is exposed through the SAME
    per-path `addressing.remote` field and the SAME `revealAddressing` gate
    as the edge-side remote — the view's selected downlink destination already
    IS the edge's active-path source on that role (D94), so no separate
    mechanism was needed.
    Consequence: a concentrator's list of connected-client addresses is
    visible ONLY when the kernel bound a loopback interface OR the operator
    sets `reveal_addressing`, never on a token-authorized non-loopback bind by
    default, consistent with Q64's "loopback-binding only" answer. The
    `POST /api/exit` authorization remains independent of `reveal_addressing`:
    loopback control is local, and non-loopback control requires a valid token.
  - **Accepted residual risk: cleartext token over a non-loopback LAN bind
    (Q58, answer (a)).** The monitor serves plain HTTP — there is no TLS in v1.
    On a non-loopback `listen` (the explicit off-host opt-in above), the bearer
    `token` therefore travels in **CLEARTEXT**: once as the `?token=` query
    parameter and thereafter as the session cookie, on every request. A
    **passive on-path observer on that LAN segment can capture the token** and
    thereby gain the same access to live stats and exit selection as a legitimate
    operator. This is a knowingly accepted trade-off, not an oversight: the
    blast radius of a captured non-loopback token includes exit selection but
    not key disclosure; the token authorizes `POST /api/exit` remotely. The
    mitigation is operational rather than cryptographic. **Recommendation:** keep `[monitor]` on its loopback default
    and reach it from elsewhere with `ssh -L <local>:127.0.0.1:<port> …`
    port-forwarding; reserve a non-loopback `listen` + `token` for networks you
    already trust, and never for an untrusted/shared LAN.

## Not yet built / deliberate boundaries

These are recorded design boundaries, not defects:

- **One transport.** There is no alternative scheduler, no forward error
  correction and no operator-declared link capacity; the transport's rate
  control is always on and both ends must run it. It has no notion of a metered
  or preferred uplink: every usable lane carries traffic under load, and probes
  and keepalives while idle.
- **In-fixture throughput/bufferbloat measurement is CPU-bound (a fixture
  boundary).** The netns fixture proves *functional* bonding/failover/DPI but
  is CPU/PPS-bound, so absolute "bonded ≈ sum of links" throughput and bufferbloat
  are **not** measured there — they are measured in the KVM lab and on the
  **real-link tier**. The realhosts tier (`just p0-baseline` →
  `TestRealAggregationBufferbloat` / `TestRealMidTransferWANKill`, T58/T63) records
  the aggregation ratio and loaded-vs-idle RTT **report-only**. Note the realhosts
  topology shares a single physical uplink, so the measured aggregation ratio is
  ~≤1 — this is an informational, report-only measurement, not a bandwidth-
  aggregation guarantee (see
  [manual-checklist.md §P0](manual-checklist.md#p0--automated-real-link-baseline-realhosts-tier)
  and [p0-findings.md](p0-findings.md)).
- **Multi-concentrator hub-failover: UDP-only remains a non-goal.** Q18 brought
  edge-side ORDERED-ENDPOINT ACTIVE-STANDBY hub failover into scope; the config
  surface (T54), the switch (T57), the netns e2e (T62), and the real-link
  mid-transfer WAN-kill tier (T63) are all built and validated (see *Concentrator
  hub failover* above). What stays out of scope: UDP-only is deliberate — there is
  no TCP/TLS fallback for wholesale-UDP-block networks. The endpoint list itself
  is no longer IP:port-only: an entry may also be a hostname behind the peer's
  `dns = true` opt-in (see *DNS endpoints and resolver privacy trade-offs* above).
- **Per-path MTU sizing + PMTU auto-discovery (T200/T205 + D88).** An
  operator-declared per-path `mtu` (config.Path.MTU) is validated at config load
  (`1280..9000`, derived inner MTU `>= 576`) AND sizes the TUN: `tunMTU` sets
  `wanbond0` to the **minimum** inner MTU across all paths (`bind.InnerMTU`), so a
  full-size inner packet fits whichever lane the transport picks. A path that
  **omits** `mtu` is PMTU **auto-discovered**: `device.Up` runs a per-path
  `telemetry.PMTUDiscovery` (on its own goroutine) that DF-padded-probe
  binary-searches the largest echoing outer size between 1280 and
  `bind.DefaultPathMTU`; the discovered value flows through `PathSnapshot.PMTU`
  into the **T209 runtime resizer**, which auto-shrinks/regrows `wanbond0` live
  (re-probing on DOWN→UP, roam, and a slow refresh). Each padded request
  substitutes in an eligible local 200 ms probe slot and forces the next slot to
  ordinary liveness, so confirmation retries add no probe traffic and do not
  starve liveness; reactive echo replies remain immediate. Timestamping starts
  in the selected slot, excluding cadence wait from RTT. An unexpected write
  failure preserves its error and increments the send-error counter, while
  `EMSGSIZE` remains the PMTU search's expected too-large result. An explicit `mtu` PINS the path (no probing — operator override
  authoritative). See `docs/p1-mtu.md`.

## References

- [p0-findings.md](p0-findings.md) — the P0 spike that fixed the single-endpoint,
  resequencing, and fixture-is-CPU-bound decisions.
- [p0-checkpoint.md](p0-checkpoint.md) — phase gate + deferred-work record.
- [install.md](install.md) — deployment and operation.
- [manual-checklist.md](manual-checklist.md) — manual per-phase + real-link
  verification.

A corrected receipt-endpoint experiment (`1bc29ef`) repeats a bounded field
low-upload gain, but raises TCP-active voice p99 to 154/157 ms and loses 8/8
echoes against 0/0 and 0/1 in adjacent baselines. The field source still fails
24 default model checks and is independently restored to the deployed build.
A later public timing reproduction also finds bulk suspended before its known
path round trip can return feedback. Including aged path delay/jitter in the
suspicion interval fixes that case; its broader checks and field behavior
remain unproved. See the [endpoint field and timing record](drafts/20261002-1730-adaptive-policy-plan.md#receipt-endpoint-field-result-and-feedback-timing--2026-10-05).

The later current-delay/flight-cohort experiment (`684b193`) again improves
bounded low-rate field upload, while worsening loaded voice p99 against both
adjacent baselines. All phases deliver 2750/2750 echoes per direction; no source
is promoted. This experiment samples current RTT from every timed physical ACK
and bounds flight by an aged minimum, but includes self-queue delay in ranking.
The next unaccepted correction removes the settled delivery-rate prerequisite
for sparse unloaded-delay learning while retaining congestion qualification.
Model and field observations remain separate; CPU-distorted lab elapsed times
cannot establish a field performance gain. See the
[current-delay field record](drafts/20261002-1730-adaptive-policy-plan.md#current-delay-field-comparison--2026-10-05).

The unloaded-delay revision `97f5509` passes quiet rate fall completely three
times in the model but still fails 24 default bond checks. Its subsequent
field comparison raises low-upload bounds to 0.268–0.302 Mbit/s against
0.144–0.183 and 0.149–0.184. Loaded voice p99 is 112/95 ms, versus 95/100
before and 58/62 after; no improvement across all metrics is established.
The candidate delivers all 2750 echoes each way, and both hosts are restored
and independently verified. No policy or release is promoted. See the
[unloaded-delay field record](drafts/20261002-1730-adaptive-policy-plan.md#unloaded-delay-field-result--2026-10-05).

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
