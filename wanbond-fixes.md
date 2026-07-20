# wanbond — production lessons (defects / improvements / docs)

Context: real deploy of edge = Raspberry Pi 4 / Debian / **NetworkManager**, concentrator
= `o3` (aarch64, OCI, public 89.168.124.91 via NAT from private `enp0s6` 10.0.0.92). Two WAN
uplinks arrive at the Pi as VLAN sub-interfaces `eth0.231`(Starlink)/`eth0.232`(5G), each a
single-address interface, pinned per-WAN by `ip rule from <source_addr>`. MP client LAN =
`eth0.223` (192.168.223.0/24, Pi is .1). Goal reached: a bonded Starlink+5G tunnel to o3
(inner 10.77.0.0/24), then VLAN-223 client traffic routed through it and NAT'd out o3.

Everything below was observed on real hardware/WANs. Unit + realhosts-P0 tiers are green;
these are all gaps that testbed (2 source IPs on ONE interface, both ends started fresh
together, never restarted independently) does not exercise.

Severity: **S1** = tunnel silently non-functional / multi-minute outage; **S2** = wrong/cryptic
behavior with a workaround; **S3** = ergonomics/observability.

---

## A. DEFECTS (wrong behavior — need a code fix)

### D1 (S1) — `allowed_ips = 0.0.0.0/0` wedges the handshake; tunnel never establishes
Bisected and confirmed. With the concentrator peer's `allowed_ips = ["0.0.0.0/0"]` on the
edge, the WG handshake never completes — even fresh-restarting **both** ends and waiting
minutes (edge `tx≈0`, o3 `rx` floods to 2.3 MB with `tx` 9 KB, i.e. o3 receives but never
answers; no handshake ever logged). Reverting the same peer to a concrete prefix
(`10.77.0.1/32`) establishes in ~25 s, deterministically. Workaround that also works: the
**split default** `["0.0.0.0/1","128.0.0.0/1"]`. The virtual-endpoint design (engine never
holds the real 89.168.124.91) rules out an endpoint routing-loop, so the cause is a
`0.0.0.0/0`-specific path (amneziawg-go allowed-ip trie or a wanbond special-case). **This
silently breaks the single most common full-tunnel config.**

### D2 (S1) — One-sided restart → multi-minute outage (no prompt re-handshake)
Confirmed repeatedly. When **either** end restarts, the peer still holding the old session
does not promptly re-handshake; the tunnel stays down for minutes until a rekey timer fires.
Restart edge → o3 stale → down for minutes; restart o3 → edge stale → down for minutes; only
restarting **both** ~together (both re-initiate from scratch) converges in ~25 s. For a
WAN-failover product this is severe — any reboot of the Pi or the concentrator is a
multi-minute client outage. Likely fix: force a fresh handshake on startup **and** on the
first path-up, and/or have the receiver treat a new valid init from a known static key as
immediately superseding the current session.

### D3 (S2) — Startup handshake fires before path liveness, then isn't aggressively retried
On every start the edge logs one `Failed to send handshake initiation: bind: no healthy
path with a known remote endpoint` (paths not up yet), paths transition up ~0.6 s later —
and then nothing. The first init is wasted and re-initiation is not visibly driven off the
path-up edge, compounding D2. WG should (re)initiate the moment the first path becomes live.

### D4 (S1) — Auto device-bind silently defeats source-policy routing
`selectDeviceBinds` picks `SO_BINDTODEVICE` (wildcard source) for a one-address/one-path
interface — exactly a VLAN-per-WAN edge. A wildcard-source socket never matches `ip rule
from <source_addr>`, so the lookup falls to `main` (no route to the concentrator via the
VLAN) → ENETUNREACH → silent path-down, zero packets on the wire, while `ping -I
<source_ip>` proves the WAN works. Fixed here with `ip rule ... oif <dev> table N`.

### D5 (S2) — `wanbond0` left DOWN + unaddressed; NetworkManager then flushes the address
The daemon creates the tun but never brings it up or addresses it. On an NM host the tun is
auto-managed: NM **flushes** the operator's address on link-up and the interface stays
**DOWN** across daemon restarts. Writing to it yields the cryptic `TUN write: input/output
error`. Fixed with NM `unmanaged-devices` + an addressing oneshot.

### D6 (S3) — `CAP_NET_RAW` (code) vs `CAP_NET_ADMIN` (shipped unit) mismatch
`pathsock` says `SO_BINDTODEVICE` needs `CAP_NET_RAW`; the shipped systemd units grant only
`CAP_NET_ADMIN`. Per the comment device-bind should always fall back, yet it bound
successfully on o3 — so the requirement is kernel-dependent and the comment/unit disagree.
Reconcile (grant `CAP_NET_RAW`, or fix the comment).

---

## B. IMPROVEMENTS (behavior/observability/ergonomics — not strictly a bug)

### I1 — Daemon should bring the `wanbond0` link UP
It already creates the device; leaving it DOWN is a silent-dead-tunnel footgun (see D5).
Link-up is low-risk; addressing can stay operator-owned.

### I2 — Expose a session/handshake state signal
The single biggest debugging obstacle: `path_up` goes to 1 long before the WG session
exists, and the only other signals are raw byte counters. Add `wanbond_session_established`
(and/or last-handshake-age) metric + a `handshake complete`/`session up` log line, so
"still converging" is distinguishable from "wedged" (D1/D2/D3 all presented identically).

### I3 — Actionable error instead of raw `input/output error`
On TUN write, check interface state / MTU and emit e.g. `wanbond0 is DOWN — address & bring
it up (install.md §4)` rather than EIO (D5).

### I4 — Downgrade startup `no healthy path` ERROR spam
Logged at ERROR during the normal ~1 s liveness warmup; reads as a hard failure. DEBUG/INFO
during warmup, or a single `waiting for path liveness` line (D3).

### I5 — Config toggle to force source-IP binding (opt out of device-bind)
For source-policy-routing topologies the roam-survival benefit of device-bind is moot and it
actively breaks routing (D4). A per-path/global `bind = "source"` switch avoids the whole
oif-rule workaround.

### I6 — Native full-tunnel support
Once D1 is fixed, accept `0.0.0.0/0`; better, let the daemon apply the split internally, or
provide a `mode = "default-route"` that wires client routing (see docs C3) so operators
don't hand-assemble policy-route + SNAT + concentrator NAT.

### I7 — Interface/route persistence across daemon restarts
Recreating `wanbond0` on every restart drops every address/route/rule referencing it, forcing
an external oneshot to rebuild them each time (addressing, `table 223` default, SNAT). Keep
the tun persistent across restarts, or ship the oneshot (see C4).

### I8 — Verify standby-path liveness is bidirectional
Observed `path_up{5g}=1` with `tx{5g}=0` (edge sends nothing on the standby). Confirm a
"up" standby can actually transmit before failover selects it, else failover could pick a
path that only proved receive.

---

## C. DOCUMENTATION UPDATES

### C1 — NetworkManager section (install.md §4)
Docs are networkd-only; most edge boxes (RPi OS/Debian/Ubuntu desktop) run NM. Add:
`unmanaged-devices=interface-name:wanbond0` in `/etc/NetworkManager/conf.d/`, or NM strips
the address (D5).

### C2 — `source_addr` + device-bind warning
State that per-WAN pinning via `ip rule from <source_addr>` is defeated by the auto
device-bind, and give the `ip rule add oif <dev> table N` recipe (D4). This is the most
common real edge topology and is currently undocumented.

### C3 — Full-tunnel / route-a-client-LAN recipe (the primary use case, entirely undocumented)
End-to-end: broad `allowed_ips` on the edge (the split, **not** `0.0.0.0/0` until D1 is
fixed); on the edge, policy-route the client subnet to `wanbond0` and **SNAT to the tunnel
IP** (so the concentrator's `allowed_ips = <edge>/32` still matches) *or* widen the
concentrator's `allowed_ips` to the client subnet; on the concentrator, `ip_forward=1`,
`MASQUERADE -s <tunnel-net> -o <wan>`, and a `FORWARD` conntrack-ESTABLISHED accept (the
shipped `-i wanbond0 ACCEPT` covers only the forward direction — the default FORWARD REJECT
rules drop return traffic without it). **Required step (closes the D65 compounding fault):**
add the `TCPMSS --clamp-mss-to-pmtu` mangle `FORWARD` rule (iptables + ip6tables) on **both**
forwarding nodes so forwarded TCP negotiates an MSS that fits the tunnel MTU instead of
emitting segments that fragment / PMTU-blackhole — the routed-client-LAN recipe is broken
without it. The verbatim rules, persistence, and arithmetic now ship in **docs/install.md
§9.2** (and `docs/runbook.md`'s firewall-persistence step); see `docs/p1-mtu.md` for the
MSS=1360/1320 accounting.

### C4 — Persistence recipe for non-networkd hosts
Bless the `PartOf=wanbond-<role>.service` oneshot pattern that, after the daemon starts,
re-applies address + link-up + policy rules + per-table routes + nft SNAT — because all of
those die with the recreated interface (I7). Warn that a plain `ExecStartPost` races the
tun's creation.

### C5 — Expected reconverge window + restart guidance
Until D2 is fixed, document that restarting one end can leave the tunnel down for minutes,
and that restarting **both** ends ~together is the fast path to reconverge. Include the
session-state metric (I2) as the "is it up yet" check.

### C6 — Concentrator NAT/forwarding prerequisites
Spell out `ip_forward`, `MASQUERADE`, and the `FORWARD` ESTABLISHED accept as required for
any routed/full-tunnel use (part of C3, but worth an explicit checklist item).

---

## What went right (keep)
Static single binary; one-binary-two-roles; 0600-config enforcement; the per-path
liveness/loss/RTT metrics (indispensable here); netns + realhosts test tiers; the
virtual-endpoint multipath design; the transparent-failover datapath itself (once
established: 0% loss, ~18–37 ms over the Starlink+5G bond, and VLAN-223 clients egressing via
the concentrator's public IP). Every defect above sits at the **restart/re-handshake and
operator/edge-plumbing** boundary, not in the steady-state datapath.
