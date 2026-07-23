# Agent instructions — wanbond

Instructions for AI agents (and humans) working in this repo. Read the
[README](README.md) for what wanbond is and [docs/design.md](docs/design.md) for
the architecture before changing anything.

## Keep the docs current (required)

**Documentation is part of the definition of done.** When a change alters
behavior, configuration, architecture, the wire format, an invariant, or an
operational step, update the affected docs **in the same change**:

- **[README.md](README.md)** — the front door: capabilities, quick start, repo
  layout, status/limitations. Update when a capability, config surface, package,
  or limitation changes.
- **[docs/design.md](docs/design.md)** — architecture and what we built on top of
  amneziawg-go. Update when the data path, a layer/package, an invariant, the
  security model, or a deliberate boundary changes.
- **[docs/install.md](docs/install.md)** — setup and operation. Update when
  config keys, systemd units, firewall/provisioning steps, or metrics change.
- **[docs/manual-checklist.md](docs/manual-checklist.md)** — update when a
  verification step or a real-link measurement changes.

A change that moves the code but not the docs it invalidates is **incomplete**.
If you are unsure whether a doc is affected, grep it for the symbol/keyword you
touched. Prefer editing the existing doc over adding a new one; keep docs
consistent with each other (e.g. a config key described in README, design, and
install must match).

## Build, test, lint

The dev shell (`nix develop`) puts Go 1.26, golangci-lint, and the netem/DPI
tooling on `PATH`. Run tools from it (e.g. `nix develop --command bash -c '…'`).

```sh
just build      # frontend typecheck/test/build + go build ./...
just test       # frontend typecheck/test + root and patched-dependency Go tests
just lint       # go vet + golangci-lint, INCLUDING -tags e2e and -tags realhosts
just e2e        # privileged netns fixture: sudo -E go test -tags e2e ./test/e2e/...
just realhosts  # opt-in real-machine tier (-tags realhosts), report-only
```

The non-privileged gate a change must pass before merge:

```sh
(cd web && npm ci && npm run typecheck && npm test) && \
go build ./... && go vet ./... && \
(cd third_party/amneziawg-go && go vet ./device/... && go test ./device/...) && \
test -z "$(gofmt -l cmd internal test third_party/amneziawg-go/device)" && go test ./...
```

`-tags e2e` / `-tags realhosts` need root / real hosts and are **not** part of the
default gate; validate them separately (see Testing discipline).

## Load-bearing invariants — do not break

Full detail in [docs/design.md §Load-bearing invariants](docs/design.md). In
short:

1. The engine sees **exactly one virtual endpoint per peer** (design rule A1); the
   Bind fans out beneath it across all real per-path UDP sockets. No per-packet
   endpoint churn to the engine. In multi-peer concentrator mode (G4), each peer
   authenticates its own PROBE frames with its own PSK (authenticated
   `peerBySource` demux); the top-level `psk` authenticates no peer once a
   second peer is configured.
2. Use wanbond's **own outer sequence space**; never reuse/perturb the inner
   WireGuard counter.
3. **Resequence before** the inner anti-replay window validates.
4. Inner fail-closed; **PROBE/CONTROL are PSK-HMAC authenticated** with monotonic
   anti-replay; DATA/PARITY are unauthenticated **by design** (DoS-grade risk
   accepted — do not "fix" this without a design decision).
5. All engine (`conn`) coupling stays isolated to **`internal/bind/bind.go`** —
   preserve the fork-swap hedge.
6. Amnezia config is **all-or-nothing per device**. The local v1.0.4 source patch
   keeps message headers and packet-shape maps per `Device` (upstream #155), so
   concurrent engines must remain race-free and configuration-isolated. Keep the
   all-or-nothing config validation and the multi-device race regression.
7. On **any `klauspost/reedsolomon` (or amneziawg-go) version bump**, re-verify
   `TestKlauspostParityPrefixStableInvariant` (`internal/fec`) before landing —
   a flipped default matrix silently corrupts every reconstructed payload.

## Testing discipline

- **Reproduce before fixing.** For a suspected defect, write a failing test (or a
  documented minimal repro) that fails for the *expected* reason *first*; then fix
  and confirm it passes. Assertions must be non-vacuous — a test that cannot fail
  on the unfixed code proves nothing (e.g. a goroutine-leak fix needs a
  `goleak`/`NumGoroutine` gate, not a bare `-count` run).
- **The netns fixture is CPU/PPS-bound.** It validates *functional* bonding,
  failover, FEC recovery, and DPI — **not** absolute throughput or bufferbloat.
  Do not assert link-throughput numbers from it; use counter ratios / functional
  checks. Real throughput belongs to the real-host tier / manual checklist.
- **Hardware-validate** changes that touch the netns fixture or real-host
  behavior on the standing worker machines (an amd64 NAT edge + an aarch64 public
  concentrator) before considering them done; these tiers are report-only and not
  in the default gate.
- Never commit code changes on a ledger/docs commit and vice versa; keep commits
  scoped.

## Conventions

- **Surgical changes.** Touch only what the task requires; match surrounding
  style; don't refactor unrelated code or "improve" adjacent comments.
- **Fail fast at boundaries** (config load, external input); no silent fallbacks
  for internal logic.
- **No new dependencies** without a clear reason; prefer the standard library and
  the already-vendored `amneziawg-go` / `klauspost/reedsolomon`.
- Keep new comments minimal and only for the non-obvious; don't delete correct
  existing comments.

## Planning ledger (cq)

This repo tracks work in a cq planning ledger under `.cq/` (goals, tasks,
defects, reviews, decisions). If you use it, record provenance
(`author`/`session`) on every write and keep ledger commits (`.cq/` only)
separate from code commits. It is optional for one-off changes.
