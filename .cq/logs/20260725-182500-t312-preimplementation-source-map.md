# T312 pre-implementation source and test map

Read-only preparation; no files changed.

## Boundary of the task

T312 can serialize the shaped writer, bound recovery-tranche service, and
prevent recoverable gaps from waiting the full 250 ms. It cannot safely reduce
the receiver hold by itself:

- T313 must authenticate/acknowledge the sender contract and generation.
- T314 must consume that evidence, compute the shorter per-gap hold, and replace
  the fixed receiver poll with an exact wakeup.

Zero TCP retransmissions do not imply zero outer-sequence loss. FEC recovery or
a resequencer skip can occur below TCP's retransmission threshold while adding
loaded RTT.

## Current receiver mechanism

- `internal/bind/multipath.go:61`: fixed 250 ms resequencer timeout.
- `internal/reseq/reseq.go:372`: FEC-active construction uses that timeout.
- `internal/reseq/reseq.go:699`: FEC disables single-source immediate release.
- `internal/reseq/reseq.go:775`: a successor behind a gap arms `now+250ms`.
- `internal/reseq/reseq.go:730`: the missing prefix skips only at deadline.
- `internal/bind/multipath.go:2267`: engine-facing receive uses a separate fixed
  250 ms poll rather than an exact gap wakeup.
- Existing deterministic witnesses:
  `internal/reseq/holdbound_test.go:87` and
  `internal/reseq/reseq_d93_test.go:161`.

The decoder cannot know DATA-only group cardinality until PARITY supplies
`DataCount` (`internal/fec/decoder.go:481`). It reconstructs immediately when
known cardinality has enough shards (`:497`, `:531`). `M=0` supplies no wire
evidence of zero-parity closure. Recovered outer sequences enter the
non-repinning path at `internal/bind/multipath.go:2869`.

## T312 implementation seams

- `internal/shaper/shaper.go:292`: complete recovery-tranche/generated-priority
  admission API.
- `internal/shaper/shaper.go:372`: cancellable pre-copy B/C reservation and
  placeholder ordering.
- `internal/shaper/shaper.go:533`: retained P admission rather than post-write
  debt.
- `internal/shaper/shaper.go:601`: sole shaped socket writer and recovery-cut
  ordering.
- `internal/bind/multipath.go:192`: socket generation; current `writeMu`
  protects `WaitGroup.Add` but not the syscall.
- `internal/bind/multipath.go:1423`: deadline-capable writer adapter bound to
  one socket generation.
- T309's complete immutable group handoff must call the recovery-tranche API
  once. Do not independently patch the pre-T309 send paths.
- Direct-write bypasses to absorb:
  `internal/bind/probe.go:104`, `internal/bind/multipath.go:838`, and
  `internal/bind/multipath.go:2819`.
- `internal/config/config.go:315` and `:1322`: P/Fgroup/Lio storage and
  A/Ecompletion derivations.
- `internal/shaper/shaper.go:183`: reject overflow, `Rp>=R`,
  unrepresentable bounds, and invalid FEC geometry.

Peak retained ownership:

`Mtotal = B + C + P + Fgroup + Lio`

`Fgroup = Kdata*Lc + (Kdata+Mmax)*Ls + (Kdata+Mmax)*Lmax`

## Required deterministic fail-first cases

1. Recovery cut orders the bounded lower OuterSeq B/C prefix, pre-cut priority,
   complete DATA+PARITY tranche, post-cut priority, then later group. Inner
   control retains OuterSeq FIFO.
2. A cut applies one absolute `cutStart+I` deadline to an already-blocked
   predecessor and all tranche writes; timeout terminates once, invalidates,
   closes the generation, and resolves waiters.
3. First/middle/last tranche failures cannot receive fresh per-call slack.
4. Independent counters attain the exact peak formula and drain to zero.
5. First/middle/last recoverable DATA loss reconstructs in order before A and
   never reaches the 250 ms skip.
6. Ordinary probe, PMTU, and reactive echo use distinct bounded P semantics;
   none bypasses the writer.
7. A second group backpressures on Fgroup and Close/cancellation resolves it.
8. Every class conserves `accepted = emitted + terminal-error` bytes.

Use injected clocks and channel handshakes; wall-clock timeouts serve only as
deadlock guards.

## Risks

- `cutStart` begins when kernel-service ownership starts after the bounded
  prefix, not at group decision time.
- UDP write deadlines apply socket-wide; shared/multi-peer sockets must remain
  contract-disabled.
- Replace the current test `writeUDP` seam with a deadline-aware writer
  contract so dummy and real UDP adapters share semantics.
- T309 must hand off a complete immutable group; per-datagram egress cannot
  implement an atomic recovery cut.
- Memory counters measure simultaneous retained ownership, not GC-dependent
  heap residency.
