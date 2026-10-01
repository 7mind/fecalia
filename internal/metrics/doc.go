// Package metrics exposes a localhost-bound Prometheus /metrics endpoint over a
// dedicated (non-global) registry.
//
// The endpoint reports per-path series labeled by path name — tx/rx byte
// counters, loss ratio, RTT, jitter, throughput, and liveness (up) — read from a
// Source at scrape time; the telemetry-derived signals (RTT/jitter/loss/state)
// come verbatim from the telemetry plane's Estimate/Prober, not re-measured here.
//
// Binding to any non-loopback or wildcard address is refused at construction
// (ErrNonLoopbackBind): the endpoint carries per-path operational data and must
// not be reachable off-host by default. Fetch is the scrape helper future e2e
// tests use to GET and parse the exposition.
package metrics
