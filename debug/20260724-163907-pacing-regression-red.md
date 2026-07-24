# T297 pacing loss-policer RED evidence

Base: `040256256470eec5af976d5477c4deb24652d731`

Disposition: these tests intentionally fail under the `pacing_repro` build tag.
T299 must convert or remove the bind RED cases, and T302 must convert or remove
the netns RED case, before G35 completes.

## Deterministic bind reproduction

Command:

```sh
nix develop --command bash -c "go test -tags pacing_repro ./internal/bind -run '^TestPacingLossPolicerRepro$' -count=1 -v"
```

The pacing-off and `ClassControl` controls passed. The semantic failures were:

```text
=== RUN   TestPacingLossPolicerRepro/D112-sub-BDP-burst-waits-instead-of-shedding
    pacing_regression_repro_test.go:171: RED D112 base=040256256470eec5af976d5477c4deb24652d731 fake_time=2023-11-14T22:13:20Z max_batch=3 burst_frames=3 offered_frames=4 observation_window=100ms average_offered_fps=40.0 capacity_fps=100.0 emitted_frames=3 emitted_wire_bytes=234 write_timestamps=[2026-07-24 16:34:15.165295999 +0100 IST m=+0.006877007 2026-07-24 16:34:15.165300529 +0100 IST m=+0.006881537 2026-07-24 16:34:15.16531198 +0100 IST m=+0.006892988] send_error=bind: datagram shed by send pacer (paths healthy, rate limited)
    pacing_regression_repro_test.go:182: RED D112: a one-frame backlog (<= one-RTT burst 3 frames) was dropped with bind: datagram shed by send pacer (paths healthy, rate limited) after a max-three-buffer batch; want bounded retention and later paced emission (offered=4, emitted=3, average 40.0 < capacity 100.0 fps)
=== RUN   TestPacingLossPolicerRepro/D108-size-parity-charges-at-egress
    pacing_regression_repro_test.go:204: RED D108-size base=040256256470eec5af976d5477c4deb24652d731 fake_time=2023-11-14T22:13:20Z data_frames=4 parity_frames=2 parity_bytes=166 parity_carry=2 emitted_frames=6 emitted_wire_bytes=478 write_timestamps=[2026-07-24 16:34:15.167388416 +0100 IST m=+0.008969434 2026-07-24 16:34:15.167392216 +0100 IST m=+0.008973234 2026-07-24 16:34:15.167395516 +0100 IST m=+0.008976524 2026-07-24 16:34:15.167400956 +0100 IST m=+0.008981964 2026-07-24 16:34:15.167403136 +0100 IST m=+0.008984154 2026-07-24 16:34:15.167422207 +0100 IST m=+0.009003215] next_data_pick=0
    pacing_regression_repro_test.go:210: RED D108 size-close: 2 parity frames/166 bytes reached the socket, but the next DATA Pick=0 was admitted; want parity charged at its egress so the next Pick is PickPaced=-2 (current charge is deferred in parityCarry=2)
=== RUN   TestPacingLossPolicerRepro/D108-deadline-parity-charges-at-egress
    pacing_regression_repro_test.go:237: RED D108-deadline base=040256256470eec5af976d5477c4deb24652d731 fake_time=2023-11-14T22:13:20Z deadline=5ms data_frames=2 parity_frames=2 parity_bytes=166 parity_carry=2 emitted_frames=4 emitted_wire_bytes=322 write_timestamps=[2026-07-24 16:34:15.169321288 +0100 IST m=+0.010902296 2026-07-24 16:34:15.169324378 +0100 IST m=+0.010905386 2026-07-24 16:34:15.174764936 +0100 IST m=+0.016345944 2026-07-24 16:34:15.174777176 +0100 IST m=+0.016358184] next_data_pick=0
    pacing_regression_repro_test.go:243: RED D108 deadline-close: 2 parity frames/166 bytes reached the socket, but the next DATA Pick=0 was admitted; want parity charged at its egress so the next Pick is PickPaced=-2 (current charge is deferred in parityCarry=2)
--- FAIL: TestPacingLossPolicerRepro (0.02s)
    --- PASS: TestPacingLossPolicerRepro/pacing-off-control (0.00s)
    --- PASS: TestPacingLossPolicerRepro/class-control-control (0.00s)
    --- FAIL: TestPacingLossPolicerRepro/D112-sub-BDP-burst-waits-instead-of-shedding (0.00s)
    --- FAIL: TestPacingLossPolicerRepro/D108-size-parity-charges-at-egress (0.00s)
    --- FAIL: TestPacingLossPolicerRepro/D108-deadline-parity-charges-at-egress (0.01s)
```

## Counter-based netns reproduction

The local container has `no_new_privs`, so the tagged package was compiled
locally and the statically linked ARM64 test and daemon binaries were executed
as root in a fresh temporary directory on the supplied Raspberry Pi. The
temporary directory was removed after the run, and `ip netns list` reported no
residual namespaces.

Local compile:

```sh
nix develop --command bash -c 'go test -tags "e2e pacing_repro" ./test/e2e -run "^$" -count=1'
```

Remote test-binary invocation:

```sh
sudo WANBOND_PACING_REPRO_BINARY=/tmp/wanbond-T297.PZQExJ/wanbond-T297-daemon \
  ./wanbond-T297-e2e.test -test.run=^TestPacingLossPolicerNetnsRepro$ -test.count=1 -test.v
```

Verbatim semantic failure:

```text
=== RUN   TestPacingLossPolicerNetnsRepro
    pacing_regression_repro_test.go:101: RED D112 netns base=040256256470eec5af976d5477c4deb24652d731 offered_frames=200 offered_bytes=80000 elapsed=2.582334916s achieved_offered_fps=77.4 capacity_fps=100.0 pacing_burst_frames=1.0 max_application_batch=1 cluster_gap=5ms cluster_period=25ms shed_records=4 shed_frames=102
    pacing_regression_repro_test.go:107: RED D112 netns: an ordinary clustered flow averaged 77.4 fps below the declared 100.0-fps capacity, and every application write was one frame (<= the 1-frame queue budget), yet the pacer shed 102 frames across 4 counter records; want wait-not-shed with path control still live
--- FAIL: TestPacingLossPolicerNetnsRepro (10.13s)
FAIL
```

The test's post-load tunnel ping passed before log inspection. Thus the failure
records data-class loss while the path and control traffic remain live, rather
than fixture or tunnel failure.

## Default gate

The non-privileged gate from `AGENTS.md` passed:

```sh
(cd web && npm ci && npm run typecheck && npm test) &&
go build ./... && go vet ./... &&
(cd third_party/amneziawg-go && go vet ./device/... && go test ./device/...) &&
test -z "$(gofmt -l cmd internal test third_party/amneziawg-go/device)" &&
go test ./...
```

The opt-in packages also passed static analysis:

```sh
go vet -tags pacing_repro ./internal/bind
go vet -tags "e2e pacing_repro" ./test/e2e
```
