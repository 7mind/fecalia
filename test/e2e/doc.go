//go:build e2e

// Package e2e holds the privileged end-to-end test harness (netns/netem two-path
// fixtures and the per-phase tunnel tests). It is guarded by the `e2e` build tag
// so the default unprivileged `go build ./...` / `go test ./...` never compiles
// or runs it; the suite requires root (CAP_NET_ADMIN + /dev/net/tun) and is
// invoked via the dedicated sudo target.
package e2e
