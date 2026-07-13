// Package dnsresolve defines the DNS resolution seam: a small, context-bounded
// Resolver interface plus two production implementations — SystemResolver
// (the Go standard resolver, over net.Resolver) and DoHResolver (DNS-over-
// HTTPS, RFC 8484). It is the injection point every runtime and test consumer
// resolves hostnames through — a future DoT transport drops in behind the
// same interface, and unit tests across the codebase inject the in-memory
// FakeResolver instead of touching the network.
//
// The package is deliberately free of any device/bind dependency so it stays
// import-cycle-safe from internal/config, internal/device, and tests.
package dnsresolve
