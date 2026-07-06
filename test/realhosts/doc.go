//go:build realhosts

// Package realhosts holds the real-host end-to-end tier: it drives the two
// standing worker machines (the amd64 edge behind a symmetric NAT and the
// aarch64 concentrator with a public inbound IP) over SSH. It is guarded by the
// dedicated `realhosts` build tag — FULLY SEPARATE from the netns `e2e` tag — so
// neither the default unprivileged `go build ./...` / `go test ./...` nor the
// privileged `-tags e2e` netns suite compiles or runs any of it.
//
// The tier is REPORT-ONLY: it executes commands on the real hosts and records
// their output (connectivity, uname/arch, and — in later tasks — a synced,
// freshly built wanbond binary). It gates nothing; the netns `e2e` fixture
// remains the authoritative pass/fail harness. It COMPLEMENTS that fixture by
// exercising the actual internet path between the real NAT and the real public
// endpoint.
//
// Invoke it only via the opt-in `just realhosts` target; it is never part of
// `just test` or CI.
package realhosts
