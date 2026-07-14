# T99 — report-only 2-edge real-link check: infeasible on the standing 2-host inventory

**Status:** deferred (documented, per M10/Q12 report-only discipline). Not a
blocker for G4 — the netns e2e tier (T97, `TestMultiPeerConcentratorIsolation`)
is the authoritative, gating proof of per-peer isolation; this task only
assessed whether the real-host tier could ADD a real-internet-path
confirmation on top of that, per G4's own testing-direction note ("realhosts
extension if feasible").

## What was checked

`go test ./...` is green (see task result). Both standing real hosts are
reachable and were inspected read-only (no mutation):

```
$ ssh ubuntu@o3.7mind.io 'uname -a; ip -o addr show | grep -v " lo "'
Linux o3 6.17.0-1018-oracle ... aarch64 GNU/Linux
2: enp0s6  inet 10.0.0.92/24 ...            # the ONLY non-loopback interface
21: wanbond0 inet 10.77.0.1/24 ...          # tunnel device of an ALREADY-RUNNING wanbond process

$ ssh ubuntu@o3.7mind.io 'ps aux | grep wanbond'
root  73612  ...  /usr/local/bin/wanbond --config /etc/wanbond/concentrator.toml   # up since Jul 13

$ ssh ubuntu@llm-ubuntu-0.pgtr.7mind.io 'uname -a; ip -o addr show | grep -v " lo "'
Linux ubuntu-worker 6.8.0-134-generic ... x86_64 GNU/Linux
2: enp1s0  inet 192.168.98.90/24 ...        # the ONLY non-loopback interface, NAT'd
```

## Topology constraint (why 2 hosts cannot realize 2 distinct edges + a concentrator)

`test/realhosts/runner.go` models exactly the standing testbed: one `Edge`
Host and one `Conc` Host (`Config{Edge, Conc, ConcPubIP, SSHKey}` —
`LoadConfig` has no notion of a second edge). That matches physical reality
confirmed above: each host exposes **exactly one** non-loopback network
interface / one network vantage point —

- `o3.7mind.io` (concentrator): one NIC (`enp0s6`), one NAT'd-to-public IPv4
  path.
- `llm-ubuntu-0.pgtr.7mind.io` (edge): one NIC (`enp1s0`), one symmetric-NAT
  path.

G4's per-peer isolation claim ("edge A's traffic does not cross-talk with
edge B's traffic at the same concentrator") is only meaningful evidence when
the two edges arrive over **genuinely independent** network paths — otherwise
any "isolation" you measure is confounded by whatever plumbing the two edges
share, and you've proven nothing about cross-peer isolation that a
single-peer test didn't already cover. That needs **three** distinct network
vantage points: edge A, edge B, and the concentrator — mirroring the T97
netns fixture's topology (two independent edge namespaces + the base
namespace as concentrator) but over real links. The standing inventory has
only two.

The two ways to force "2 edges" out of 2 hosts are both degenerate:

1. **Both edges as one host's two source IPs / two netns, same NIC.**
   `test/realhosts/multipath_failover_test.go` already uses exactly this
   trick (`setupEdgeTwoPaths`, secondary source address + policy routing) to
   fake a *second uplink* for one peer's own multipath bonding — but reusing
   it to fake a *second peer* collapses both "independent" edges onto the
   same physical NIC, the same NAT mapping, and the same kernel queueing
   discipline. Any cross-peer isolation observed would be confounded with
   ordinary same-host multiplexing, not evidence of concentrator-side
   per-peer isolation across the internet.
2. **Time-multiplex one edge host through two configs sequentially.** Not
   concurrent, so it cannot exercise interleaved outer-seq streams at all —
   the exact scenario T97 exists to prove.

Additionally, `o3.7mind.io` is currently running a **live** standing
`wanbond` concentrator process (`/etc/wanbond/concentrator.toml`, up since
Jul 13) that other real-host tiers (soak, multipath, wan-kill-failover) rely
on. Improvising a differently-configured multi-peer concentrator run against
that same host — even via a second netns — risks colliding with that
process's config, firewall rules (`InspectTunnelFirewall` / D7 persisted
iptables rule), or listen port, for a report-only measurement that provides
no additional gating value (T97 already gates the functional claim).

## What inventory would suffice

A **third** standing host, in its own distinct network/NAT domain,
provisioned as a second edge — e.g. `WANBOND_EDGE2_HOST` alongside the
existing `WANBOND_EDGE_HOST`/`WANBOND_CONC_HOST` env vars in
`test/realhosts/runner.go`. That gives three genuine vantage points (edgeA +
edgeB + concentrator), letting a real-host capture mirror T97's topology:
two edges dial the same concentrator concurrently over independent real
network paths, each edge's bulk transfer verified independently (TCP's own
integrity check), while scraping the concentrator's per-peer `/metrics`
labels (T94) to confirm attribution — all report-only (absolute numbers not
asserted; only qualitative per-peer health/isolation), consistent with
Q12/M10.

## Disposition

Per the task's own acceptance ("OR the deferral/infeasibility is documented
with the concrete topology constraint and required inventory"), this
satisfies T99 without forcing a degenerate single-NIC "2-edge" setup onto
live shared infrastructure. If a third host becomes available, a follow-up
task can add the `WANBOND_EDGE2_HOST`-based real-host multi-peer capture
described above.
