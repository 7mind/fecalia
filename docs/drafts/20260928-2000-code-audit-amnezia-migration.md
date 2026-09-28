# Code audit and AmneziaWG migration

Inspected on 2026-09-28, application baseline `8ff0c5e`. This is a removal and
migration proposal, not a claim that the new adaptive policy is production-ready.
The operator's subsequent Speedtest results show approximately 0.45–0.48 Mbps
download and failed/0.19 Mbps upload. Resolve that regression before retiring
fallback implementations. No production deployment was performed for this audit.

## Agreed scope and work order

The operator requested a code audit covering obsolete policies, unnecessary
code, cleanup and deduplication, and a migration pathway to AmneziaWG 3. They
explicitly approved removing the legacy ledger. The detailed runtime removal
list below is a proposal; the audit request does not settle whether metered-link
conservation or FEC should remain supported features.

1. **Completed: remove the ledger.** `.cq/` was removed in `7d23ed7`, ignored,
   and retained in Git history. Do not recreate the ledger or rewrite history.
2. **In progress: validate adaptive bonding.** Reproduce actual asymmetric
   links, idle periods, jitter and temporary outages in the autonomous VM lab.
   Saturate both uplinks in both directions while preserving connections and
   low-latency interactive traffic. Bulk packets are striped, with bounded
   recovery; small packets may receive budgeted replication. The intermediate
   candidate `af2d54c` is being deployed by the operator. Its measured gains and
   remaining failures are recorded in [the VM results](../../test/vm/README.md#intermediate-correction--2026-09-28-623d36f).
3. **Next cleanup decision: supported modes.** Once the retained transport
   meets the supported envelope, decide explicitly whether static weighted
   scheduling, legacy active-backup/data thrift and Reed–Solomon FEC are to be
   retired. Remove each retired feature together with its exclusive knobs,
   metrics, examples, dependencies and tests. Preserve shared path health,
   authenticated demux, replay protection, ordering and lifecycle contracts.
4. **Simplify the remaining code.** Extract shared path membership from the
   legacy scheduler; reduce competing transport/control owners; deduplicate DNS
   result aggregation and repeated recovery documentation. Preserve behavioral
   assertions when consolidating tests. Do not count file splitting as cleanup
   or polish a legacy controller immediately before deleting it.
5. **Then migrate the engine.** Evaluate the pinned AmneziaWG v3 candidate below
   after resolving which local patches survive cleanup. Upgrade the engine
   with current wire settings first, prove mixed-version operation, and treat
   activation of new obfuscation settings as a separate coordinated change.
   The user performs all production deployments.

Acceptance is measured behavior and reduced supported complexity, not a target
line-count reduction. No runtime mode has been removed and no engine upgrade
has been applied by this audit. Outstanding decisions are the retained policy
set, whether FEC/data thrift remain requirements, and which engine statistics
justify keeping custom patches.

## Evidence and size

Counts below use tracked files, physical lines including comments/blanks, and
uncompressed file bytes. They are not executable-code counts. Binary fixture
lines are included in the test category; vendored tests are in the vendor category.

| Category | Files | Lines | Bytes |
| --- | ---: | ---: | ---: |
| First-party non-test Go/TS/CSS source | 105 | 38,461 | 1,630,166 |
| Tests and fixtures, including VM tooling | 277 | 93,008 | 3,604,558 |
| Vendored engine | 128 | 21,412 | 547,512 |
| Documentation | 15 | 9,188 | 573,916 |
| Build/configuration/other | 21 | 3,601 | 150,628 |
| Retired planning ledger | 826 | 37,218 | 110,178,681 |

The first-party source category includes test support: `internal/wireaudit`
(668 non-test Go lines) is absent from `go list -deps ./cmd/wanbond`; it is used
by wire-format tests. Do not confuse that with unused code. Every other
`internal` package is in the application dependency graph.

The ledger was removed with operator authorization in `7d23ed7`. Git history
retains it; this does not shrink existing repository history. No history rewrite
is proposed. Ignore `.cq/` to prevent raw logs being recommitted.

The largest source files are `bind/multipath.go` (5,757 lines),
`config/config.go` (2,514), `device/device.go` (2,074), `reseq/reseq.go` (1,738),
`bind/recovery_contract.go` (1,549), `metrics/metrics.go` (1,473), and
`shaper/shaper.go` (1,354). Size alone does not prove redundancy.

### Static checks actually run

```sh
nix develop --command bash -c \
  'golangci-lint run --no-config --enable-only unused,dupl --timeout=5m ./...'
nix develop --command bash -c \
  'golangci-lint run --no-config --enable-only unused --build-tags=e2e,realhosts --timeout=5m ./...'
nix develop --command go list -deps ./cmd/wanbond
```

`unused` reported zero findings, including with both test tags. This is not
whole-program proof that every exported API is needed. `dupl` reported three
pairs, counted twice by the linter:

| Locations at baseline | Recommendation |
| --- | --- |
| `bind/congestion.go:269` and `:355` | Same carrier/epoch selection loop. Extract once if this legacy controller is retained; avoid polishing it immediately before deletion. |
| `dnsresolve/doh.go:142` and `dot.go:105` | Centralize A/AAAA result, TTL, NXDOMAIN and NODATA aggregation. Keep transport-specific query execution separate and preserve current resolver behavior tests. |
| `device/peerteardown_test.go:366` and `:394` | Table-drive the two role cases, preserving both assertions. Small cleanup. |

## What to retire, and what must survive

The three inspected Nix configurations (`pi-mo`, `raspi5l`, `o2`) request
`adaptive`, with FEC disabled and no `[amnezia]` profile. These declarations do
not prove all users or deployments have migrated. `active-backup` remains the
application default; `weighted` remains accepted configuration.

| Candidate | Evidence and removal conditions |
| --- | --- |
| `weighted` policy | Static-capacity aggregation gate and frame pacer remain callable. Retire after adaptive meets the supported load/outage envelope. Remove its config knobs, metrics, examples and policy-specific tests together. |
| Legacy `active-backup` transport | Still implements priority failover/data thrift, which adaptive saturation does not promise. If metered-link conservation remains a requirement, specify and implement it in the retained transport before removal. Keep the current binary/config as a rollback artifact. |
| `adaptivefec`, `fec`, `bind/fec*.go` | Adaptive explicitly rejects FEC. Good retirement candidates if Reed–Solomon recovery is no longer a supported mode. Remove `reedsolomon` and exclusive transitive dependencies only after import-graph verification. |
| Legacy recovery-contract protocol and data-loss feedback | Supports the old FEC/shaper plane. Adaptive has its own authenticated ACK/repair protocol. Remove old negotiation and observability with the old plane, retaining anti-replay/session fencing and the outer CONTROL envelope. |
| `congestion`, `shaper`, TUN AQM and engine admission patches | `startTUNAQM` enables only paced active-backup with per-path shapers. Adaptive uses its own controller. Persistent TUN interfaces can retain old kernel qdiscs across upgrades: migration cleanup and its real-kernel test must precede deletion. Engine accounting is still called by metrics even under adaptive. |
| Legacy DATA/PARITY codec | Adaptive rejects these receive kinds. Retirement reduces parser/security surface, but is an explicit wire-compatibility break. Reject incompatible peers visibly; preserve PROBE/CONTROL authentication, challenge response and replay checks. |
| `sched` package | **Cannot delete wholesale now.** Adaptive constructs `ActiveBackup` with pacing off and reuses scheduler membership/health plumbing. Extract a small path registry before removing selection/pacing implementations. |
| `reseq` package | **Keep bulk ordering.** Adaptive uses it before WireGuard replay validation. Remove only legacy contract/FEC machinery after preserving restart, loss-gap, ordering and bounded-hold contracts. |
| Routing, DNS, PMTU/MSS, peer lifecycle, exit selection | Shared operational behavior. No evidence that these are junk. Keep coverage for multi-peer demux, NAT rebinding, failover and startup/shutdown. |
| Monitor, TUI, Prometheus projections | Some repeated field mapping is boundary code, not automatically duplication. Retire obsolete FEC/recovery fields with the feature; avoid a generic reflection serializer. Add adaptive-specific status where needed. |

The dedicated legacy-heavy files/packages above contain **10,593 non-test Go
lines**, including the entire shared `sched` package and TUN AQM. This is an
investigation envelope, not a promised deletion count. Further branches occur
inside `multipath`, configuration, device composition and observability.

Recommended order: stabilize adaptive → retire obsolete public modes → remove
their implementations and exclusive tests → simplify the surviving composition.
Consolidate path membership, peer state and lifecycle around the surviving
transport. Splitting `multipath.go` into arbitrary smaller files alone does not
reduce complexity; removing competing owners does.

### Test and documentation cleanup

Use retained behavioral contracts to decide what stays. Many `failfirst` and
numbered `round2/round3` files inspect private state; the filename is provenance,
not evidence of redundancy. Consolidate by contract only after recording which
distinct failure each assertion detects. Remove tests whose supported policy is
explicitly retired; retain shared regression cases.

`metrics/recovery_docs_test.go` requires the same recovery keywords/formula in
README, design, install, manual checklist and example configuration. This
actively enforces duplicated documentation. Replace those repeated derivations
with one authoritative design section and short operational links, then retain
checks for actual configuration/metric contracts rather than prose duplication.

Keep deterministic transport tests separate from real-kernel/VM performance
tests. At the audited baseline, VM profiles covered 3 ms jitter at most, short
fresh processes, and one peer. The intermediate correction adds 10 ms jitter,
idle-to-load and asymmetric mobile profiles, plus a real-kernel test for retiring
the persistent TUN cap. Multiple exits and broader RF conditions still require
coverage. Do not replace the
existing namespace/real-host suites wholesale: their routing, DNS, roaming and
multiple-peer contracts exceed the VM benchmark's current scope.

## AmneziaWG: concrete migration target

The relevant version is the **AmneziaWG protocol/Go engine**, not the desktop
application version. Official documentation now describes 3.1. The latest tag
returned by the upstream tags API on the audit date was `v3.1.20260828`, commit
`b5928efb6ca19f0153958460c3d141f04abc5c2e`; upstream master matched it. The GitHub
releases list was empty. Treat this as a tagged candidate requiring validation,
not a release with assumed compatibility guarantees.
Sources: [protocol documentation](https://docs.amnezia.org/documentation/amnezia-wg/),
[upstream tags](https://github.com/amnezia-vpn/amneziawg-go/tags).

The candidate declares module `github.com/amnezia-vpn/amneziawg-go/v3` and Go
1.25.0; our root pins Go 1.26.4. It raises the x/crypto, x/net and x/sys minima
and changes other nested dependencies. Recompute the root dependency graph and
Nix vendor hash rather than assuming all nested requirements are linked into
wanbond. [Pinned module declaration](https://github.com/amnezia-vpn/amneziawg-go/blob/v3.1.20260828/go.mod).

### This is not a one-file dependency swap

Comparison against the Go proxy's exact `v1.0.4` archive found **18 changed or
added engine files** in the local tree. Documentation claiming all coupling is
isolated to `bind.go` is stale: `multipath.go` and `device/tunaqm.go` import
`conn`, while device metrics/AQM require custom engine APIs.

| Local patch family | Candidate disposition |
| --- | --- |
| Per-device message headers and packet-shape maps (#155), regression test | Candidate uses per-device atomic header ranges/paddings. Port behavioral isolation/race tests; do not replay the old structural patch blindly. |
| Locked stateful ChaCha8 junk generator and concurrency test | Candidate removed the old `device/awg` implementation and uses different randomness paths. Reassess concurrent-handshake behavior; old patch no longer applies directly. |
| One-line pools test/vet repair (#157) | Inspect new tests and retain a passing nested-device vet/test gate. |
| `conn.BindBatchCompleter`, terminal completion plumbing | Custom API absent upstream. Retire with the legacy admission/shaper ownership model if no longer required, or port as an explicit generic patch with its tests. |
| `conn.BindPacketBatchCompleter`, pre-encryption `PacketMetadata` | New adaptive dependency: full IP flow identity and conservative pure-TCP-ACK metadata survive encryption and batching through a local side channel. Port the TUN classifier, pooled-element reset and send completion contract; rerun the real-engine metadata test, parser fuzzing and memory/UDP flow-isolation contracts. Metadata never enters the wire format. |
| `outbound_admission.go`, peer/container/send admission accounting | Custom APIs absent upstream. Required by current TUN AQM. Decide retirement before paying the rebase cost. |
| `outbound_stats.go`, pipeline/histogram/high-water metrics | Used even when adaptive is selected. Retain only measurements operators need, via a narrow engine adapter or a reviewed upstream patch. Do not replace missing accounting with fabricated zeros. |

A scratch copy with `/v3` imports and the candidate replacement failed
`go build -mod=mod ./...` on missing `conn.BindBatchCompleter`. Substituting
only that interface declaration locally for diagnostic purposes exposed missing
`OutboundStats`, `OutboundBatchHistogram`, `SetOutboundAdmissionLimit`,
`TrySetOutboundAdmissionLimit` and `OutboundAdmissionLimit`. This experiment
changed neither the working dependency nor the deployed binary. It does not
establish runtime interoperability.

### Configuration, framing and wire behavior

Upstream v3 adds S3/S4 prefixes, header ranges, shared header protection,
additional content padding, and configurable timing ranges. Header protection
requires S1–S4 of at least 12 bytes. Our config exposes only scalar H1–H4 and
S1/S2; `MaxJunkPrefix` reserves only their maximum. A faithful migration needs
typed ranges, profile validation, UAPI rendering and worst-case MTU accounting.
[Upstream configuration](https://github.com/amnezia-vpn/amneziawg-go/blob/v3.1.20260828/README.md).

The pinned candidate additionally exposes `random_trailers` and `disable_cookies`.
Do not turn new knobs on as an incidental dependency-upgrade side effect.
[UAPI implementation](https://github.com/amnezia-vpn/amneziawg-go/blob/v3.1.20260828/device/uapi.go).

Our `bind/classify.go` assumes fixed type words, prefixes only on handshakes,
and a 32-byte keepalive. Header protection and padded transport invalidate
those assumptions. Prefer retiring that legacy scheduler classification before
migration; if retained, use engine-provided traffic metadata instead of guessing
through encryption. Adaptive's <=384-byte priority heuristic also sees padded
ciphertext: large padding can change voice classification and replication cost.

Wanbond wraps the complete inner packet in its own XChaCha20 outer frame, with
authenticated CONTROL for adaptive data. Therefore Amnezia signature bytes
are hidden on the external wire; installing v3 does **not** make wanbond look
like native AmneziaWG 3.1. Packet sizes and timing still affect the outer trace.
Any claim of improved resistance to statistical detection needs captures and
appropriate evaluation. This is an inference from `frame.Codec` and the send
path, not a measured censorship-bypass result.

Upstream [issue #168](https://github.com/amnezia-vpn/amneziawg-go/issues/168) and
[open PR #169](https://github.com/amnezia-vpn/amneziawg-go/pull/169) concern stale
S4 across a blocking TUN read during configuration. Inspection of the candidate
still shows the offset captured before `Read`. Reproduce the reported race with
the candidate before deciding whether to carry the proposed correction. No
runtime reproduction of that upstream report was performed in this audit.

### Staged migration and acceptance

1. **Stabilize current transport.** Extend the lab to cover the production
   failure, idle-to-load transitions, variable delays, NAT and persistent TUN
   upgrades. Preserve the previous candidate/config as rollback artifacts.
2. **Resolve legacy scope.** Decide whether priority/data thrift and FEC remain
   supported requirements. Retire obsolete machinery in separate reviewable
   commits, preserving authenticated demux, ordering and bounded queues.
3. **Upgrade engine with old wire settings.** Pin the immutable v3 candidate,
   port only necessary patches, adapt the module path/config/metrics, update
   `vendorHash`. Run multi-device race tests and engine tests. No new obfuscation
   profile in this step.
4. **Prove compatibility before a rolling deployment.** Test old/new in both
   edge/concentrator combinations with current zero-Amnezia config, plus
   new/new. Test rekey, restart, two exits, handshakes under load, NAT rebinding,
   MTU ceilings and asymmetric outages. Preserve the outer protocol initially.
5. **Opt into a v3 profile separately.** Add the typed config and secret reference
   for header protection; test both ends with matching required parameters,
   padding boundaries, malformed packets and the small-packet class. Do not log
   the protection key. Changing per-device wire settings requires coordinated
   endpoints or a separate candidate device/listener; it is not negotiated by
   the existing adaptive hello.
6. **Operator deployment.** After mixed-version validation, update a standby
   concentrator, verify it, update the edge with the old profile, then the other
   concentrator. Enable new settings only after the full path is validated.
   If mixed-version tests fail, use parallel listeners/devices and an explicit
   cutover. Exit changes alter public NAT addresses, so existing external TCP
   flows are not guaranteed to survive that cutover. User performs deployments.

Required gates include the repository merge gate and `nix build`, linux/amd64
and linux/arm64 builds, retained replay/peer-isolation tests, and calibrated
bidirectional VM performance/voice tests. If FEC remains, retain the parity
prefix invariant required by AGENTS.md. Compare CPU, memory, packet overhead,
idle traffic and restart behavior as well as goodput.

## Reproduction artifacts

Local scratch: `/srv/nvme/tmp/wanbond-audit-20260928/` contains upstream tag/API
responses, extracted sources, `v1.0.4-local.patch`, `lint.txt`,
`unused-all-tags.txt`, `production-packages.txt`, and both compile-probe logs.
These are local audit artifacts, not portable test fixtures or committed
dependencies. The audit did not run a migrated engine in the VM lab.
