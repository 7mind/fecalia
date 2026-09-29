# wanbond: lab tuning after the v3 migration — report, 2026-09-29

## State

- `main` is `25aba42` (AmneziaWG v3 engine, capacity discovery, CoDel on the
  bulk queue). The user deployed it; Speedtest through the tunnel gave
  33.09 / 0.94 Mbit/s (raspi5l exit) and 32.79 / 1.36 Mbit/s (o2 exit).
- Branch `lab-tuning` holds the candidate described here. It is not merged and
  not deployed.
- Targets agreed with the user: TCP >= 75% and UDP >= 80% of combined link
  capacity in both directions; voice continuity through link failures. The
  voice criterion used is that of `test/vm/continuity.py` (loss < 1%, p99 round
  trip < 150 ms, longest receive gap < 150 ms); the user has not confirmed it.

## What the candidate changes

| Area | Change |
|---|---|
| Rate control | Hold at 95% of demonstrated capacity, probe in 150-500 ms pulses, drain at 85%; capacity re-measured after a drop below 75% |
| Delay signal | Number of delay samples required grows with the lane's own jitter |
| Scheduling | Three classes (real-time, small TCP, bulk); lanes reserved by unloaded latency; per-class in-flight accounting |
| Bulk queue | CoDel target and interval derived from lane round trips |
| Acknowledgements | 25 ms cadence stretching to 50 ms so that they stay within 8% of received bytes |
| Path MTU | A candidate size is rejected only after misses that a control probe at a known-good size does not share |
| Metrics | Drop causes, discovery state and small-datagram bytes per lane |
| Lab | `test/vm/udp.py`, constant-rate UDP gate at 80% |

`docs/design.md` describes each of these.

## Matched lab results

A = `25aba42`, B = candidate; order A, B, B, A in one lab session; two runs each.

| Measurement | Gate | A | B |
|---|---|---|---|
| Fast upload, Mbit/s | 96 | 93.5 / 90.2 | 100.4 / 103.4 |
| Fast download, Mbit/s | 96 | 101.3 / 91.5 | 100.2 / 101.9 |
| Radio upload, Mbit/s | 1.24 | 1.149 / 1.117 | 1.288 / 1.254 |
| Radio download, Mbit/s | 75.4 | 60.1 / 73.2 | 77.7 / 83.5 |
| Voice loss, % | 1 | 0.9 - 2.4 | 0.03 - 0.22 |
| Voice longest gap, ms | 150 | 111 - 295 | 109 - 128 |
| Voice p99 round trip, ms | 150 | 198 - 211 | 185 - 207 |
| TCP progress in each outage second | pass | pass / pass | pass / fail |
| Basic continuity p99, ms | | 99 - 135 | 73 - 88 |

Constant-rate UDP, B only, one run per profile: fast 104.7 / 105.4 Mbit/s
(gate 102.4), radio 1.359 / 81.6 (gates 1.32 / 80.4).

### Final tree

The binary built by `nix build` from the final tree (`d178eff6…`), two rounds,
not matched against A:

| Measurement | Gate | Round 1 | Round 2 |
|---|---|---|---|
| Fast upload / download, Mbit/s | 96 | 102.5 / 102.4 | 103.1 / 103.3 |
| Radio upload, Mbit/s | 1.24 | 1.289 | 1.291 |
| Radio download, Mbit/s | 75.4 | 73.4 (fail) | 84.2 |
| Voice loss, %, hub / edge | 1 | 0.09 / 0.15 | 0.15 / 0.12 |
| Voice longest gap, ms | 150 | 158 (fail) / 120 | 128 / 82 |
| Voice p99 round trip, ms | 150 | 198 / 178 (fail) | 204 / 191 (fail) |
| TCP progress in each outage second | pass | pass | fail, edge at 41 s |
| UDP fast up / down, Mbit/s | 102.4 | 104.3 / 105.6 | |
| UDP radio up / down, Mbit/s | 1.32 / 80.4 | 1.333 / 83.2 | |

The 158 ms gap was at 0.5 s into the run (cold start), not at a link failure;
the longest gaps at link failures were 109 and 128 ms.

Limits of this evidence: two runs per binary in the matched series. Radio
download of B was below the 75% gate in two of seven runs (70.2 and 73.4
Mbit/s; the others 77.7 to 84.2). UDP was measured twice per profile and only
on B.

## Verification of the final tree (`lab-tuning`)

| Check | Result |
|---|---|
| Frontend typecheck and tests | pass (40 tests) |
| `go build`, `go vet` (also with `progression` tag) | pass |
| Engine `go vet`, `go test ./device/...` | pass |
| `gofmt` | clean |
| `go test ./...` | pass |
| `go test -race` on bond, bind, telemetry, metrics, device, cmd | pass |
| `just lint` | fails with the same findings as on `main`; none added |
| `nix build` | pass |

Not run: the e2e path-MTU suite; the `progression`-tagged tests
(`jitter_trap_progression_test.go` fails on some seeds by design of the tag).

One intermittent failure of two `internal/bind` carrier-epoch tests was seen
once while the lab was loading the host and did not recur.

## Mixed versions and restarts

Recorded in `test/vm/README.md`. Either end can be upgraded first. A hub
restart interrupts the tunnel for 16 s on every build tested, including the
one before the engine migration; an edge restart for 0.7 s.

## Open points

1. Voice p99 round trip stays above 150 ms over the whole run. The first five
   seconds of a cold start and the window in which only LTE is up dominate;
   LTE's emulated jitter alone (40±30 ms each way) accounts for about 135 ms.
2. TCP progress in every second of the LTE outage fails in one of two runs:
   two voice streams take 56-74% of the remaining 0.4 / 0.5 Mbit/s.
3. A voice datagram in flight on a failing WAN is recovered about 250 ms later
   unless a copy was on the other WAN. The copy allowance (10% of capacity)
   covers about half of two voice streams. 20% gave 240 -> 152 ms one-way p99
   in the model, and breaks two tests that pin the allowance.
4. A jittery lane that is never idle can still hold its delay baseline too
   high (`jitter_trap_progression_test.go`).
5. Production path MTU was observed changing about every five minutes
   (1339, 1166, 1283, 1119) on `25aba42`. `9e9f7fb` corrects the lab
   reproduction; production has not run it.
6. Hub restart: 16 s interruption.

## Decisions for the user

- Merge `lab-tuning` to `main` and deploy.
- Copy allowance 10% or 20%.
- Whether the voice gate should measure p99 outside cold start and against
  the latency of the WAN in use rather than a fixed 150 ms.
- Whether TCP must progress in every second while voice occupies most of a
  0.4 Mbit/s WAN.
- Whether to shorten the reconnect after a hub restart.

## Scratch material

`/srv/nvme/tmp/wanbond-awg3-20260929/`: capture and trace scripts, series
logs, candidate binaries. Lab results are under the lab state directory. The
lab guests are stopped.
