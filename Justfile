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

# Vet + lint.
lint:
    go vet ./...
    golangci-lint run

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
