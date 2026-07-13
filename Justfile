# wanbond task runner. Targets assume the dev shell is active (`nix develop`),
# which puts go, golangci-lint, and the netem/DPI tooling on PATH.

# Show available targets.
default:
    @just --list

# Build all non-privileged packages.
build:
    go build ./...

# Cross-compile static release binaries (CGO_ENABLED=0, stripped, version
# stamped from git) for linux/amd64 and linux/arm64 into dist/. `file` reports
# them statically linked; see docs/install.md for the deployment steps.
release:
    mkdir -p dist
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" -o dist/wanbond-linux-amd64 ./cmd/wanbond
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags --always --dirty)" -o dist/wanbond-linux-arm64 ./cmd/wanbond

# The e2e/realhosts sources are gated behind //go:build tags, so a tagless
# vet/lint never compiles them and silently skips them (see D28). The tagged
# passes below are additive — the tagless run still analyses the default build
# exactly as before.
# Vet + lint the default build plus the e2e- and realhosts-tagged sources.
lint:
    go vet ./...
    go vet -tags e2e ./test/e2e/...
    go vet -tags realhosts ./test/realhosts/...
    golangci-lint run
    golangci-lint run --build-tags e2e ./test/e2e/...
    golangci-lint run --build-tags realhosts ./test/realhosts/...

# Format check (fails if anything is unformatted).
fmt-check:
    test -z "$(gofmt -l cmd internal test)"

# Unprivileged unit/property tests only (the e2e build tag is never set here, so
# no privileged test compiles or runs).
test:
    go test ./...

# Privileged end-to-end suite: netns/netem + TUN tunnel bring-up. Requires root
# (CAP_NET_ADMIN + /dev/net/tun); run on real or emulated hardware, not CI.
e2e:
    sudo -E go test -tags e2e ./test/e2e/... -count=1

# Run one named e2e phase test, e.g. `just e2e-run TestP1Failover`.
e2e-run TEST:
    sudo -E go test -tags e2e ./test/e2e/... -run {{TEST}} -count=1 -v

# Opt-in real-host e2e tier: drive the two standing worker machines (edge behind
# symmetric NAT, aarch64 concentrator) over SSH. Report-only — it records host
# uname/arch and gates nothing. Needs the `llm` SSH key (WANBOND_SSH_KEY, default
# /run/agenix/llm-ssh-key); no root required. NEVER part of `just test` or CI.
# Run all: `just realhosts`; one test: `just realhosts TestRealConnectivity`.
realhosts TEST="":
    go test -tags realhosts ./test/realhosts/... {{ if TEST != "" { "-run " + TEST } else { "" } }} -count=1 -v

# Opt-in idempotent real-host provisioning: ensure iperf3 + gcc + Go 1.26.x on
# both hosts and the concentrator's tunnel-interface INPUT ACCEPT rule. Report-
# only, idempotent (a second run reports no changes); same SSH key requirements
# as `realhosts`. Thin wrapper over TestRealProvision. NEVER part of `just test`.
realhosts-provision:
    go test -tags realhosts ./test/realhosts/... -run TestRealProvision -count=1 -v

# P0 real-link baseline: the SINGLE repeatable pre-pilot procedure (docs/manual-
# checklist.md §P0). A thin orchestration layer over three EXISTING realhosts
# tests — it provisions both ends, brings the tunnel up, and records:
#   - TestRealP0Smoke              single-uplink handshake + ping RTT + iperf3
#                                  (single/8x-parallel TCP, UDP goodput/loss);
#   - TestRealAggregationBufferbloat  per-path + bonded throughput and their
#                                  aggregation ratio, plus idle-vs-loaded RTT
#                                  bufferbloat delta under saturating load;
#   - TestRealMidTransferWANKill   mid-transfer LINK failover and HUB failover
#                                  (T57) recovery timings.
# It TEES the full -v output to a timestamped baseline report under
# test/realhosts/reports/ (gitignored). REPORT-ONLY (Q19): the underlying tests
# assert LIVENESS ONLY — no Mbit/s or ms threshold gates the run; the numbers are
# informational for the operator's (non-blocking) pilot-gate decision. Same SSH
# key requirement as `realhosts` (WANBOND_SSH_KEY, default /run/agenix/llm-ssh-
# key). NEVER part of `just test` or CI. A non-zero exit means the run itself
# could not complete (host unreachable, tunnel never came up), NOT that a
# performance number missed a target.
p0-baseline:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p test/realhosts/reports
    report="test/realhosts/reports/p0-baseline-$(date -u +%Y%m%dT%H%M%SZ).log"
    echo "wanbond P0 real-link baseline — $(date -u +%Y-%m-%dT%H:%M:%SZ)" | tee "$report"
    go test -tags realhosts ./test/realhosts/... \
        -run '^(TestRealP0Smoke|TestRealAggregationBufferbloat|TestRealMidTransferWANKill)$' \
        -count=1 -v 2>&1 | tee -a "$report"
    echo "baseline report: $report" | tee -a "$report"
