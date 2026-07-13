// Package dnsresolve defines the DNS resolution seam: a small, context-bounded
// Resolver interface plus a system-resolver implementation over net.Resolver.
// It is the injection point every runtime and test consumer resolves hostnames
// through — designed so a future DoH/DoT transport drops in behind the same
// interface, and unit tests across the codebase inject the in-memory
// FakeResolver instead of touching the network.
//
// The package is deliberately free of any device/bind dependency so it stays
// import-cycle-safe from internal/config, internal/device, and tests.
package dnsresolve
