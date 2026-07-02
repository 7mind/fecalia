# wanbond — cq project prompt

Paste into `/cq:plan`.

---

GOAL: Build `wanbond` — a single self-contained Go binary (runs on both edge and
concentrator) that bonds two unreliable, heterogeneous WANs (Starlink: low-latency,
jittery, intermittent obstruction loss; 4G/5G: metered, stable, variable latency)
into one resilient, DPI-resistant tunnel for GENERAL IP traffic, with adaptive FEC.
No FOSS tool combines transparent failover + aggregation + data-efficient adaptive
FEC; this fills that gap, tuned for the 2-link case.

ARCHITECTURE (decided — do NOT re-evaluate the base choice):
- Embed amneziawg-go (github.com/amnezia-vpn/amneziawg-go, WireGuard fork with DPI
  obfuscation, same Noise crypto) as a LIBRARY: its device engine provides TUN I/O,
  Noise handshake, AEAD, rekeying, peer roaming, persistent keepalive — all UNMODIFIED.
- All bonding logic lives in a custom `conn.Bind` transport implementation UNDER the
  WG engine (precedent: Tailscale magicsock does transparent path migration this way).
  The Bind owns one UDP socket per path (bound to its source address) and operates on
  OPAQUE encrypted WG datagrams — it needs no crypto of its own for the data plane.
- Outer bonding frame wraps each WG datagram: (outer-seq, path-id, fec-group, flags).
  Additional outer frame types: RS parity, per-path probes, path control.
- Receive side: unwrap -> FEC recovery -> resequencing buffer -> deliver to the WG
  engine as if from a single socket. WG sees ONE stable virtual endpoint per peer;
  the Bind privately manages the real per-path endpoints and their roaming.
- Security model: inner WG layer is fail-closed (injection/decryption impossible
  without keys). Outer DATA headers are unauthenticated — forgery costs only bounded
  buffer/decode work (DoS-grade, acceptable). Outer CONTROL/PROBE frames are
  authenticated via a static pre-shared key from config (HMAC or AEAD — vetted lib,
  not hand-rolled). Outer wire format must carry no plaintext magic constants and
  look like high-entropy UDP (see requirement 6).

CONTEXT / GIVEN:
- Edge = Linux box behind a router that ALREADY pins each path to a WAN (traffic from
  source IP A egresses Starlink, from source IP B egresses 5G). Path-to-WAN selection
  is SOLVED externally; the tunnel only binds one UDP socket per path source.
- Concentrator = small low-latency VPS, public IP, NATs tunnel traffic to the internet.
- Measured links: Starlink ~45ms (jittery), 5G ~64ms (stable). Start with 2 paths,
  design for N.
- Edge is sometimes used from hostile networks (hotels etc.) that block or throttle
  identified VPN protocols.

PRIORITIZED REQUIREMENTS (in order — earlier ones must not regress for later ones):
1. TRANSPARENT FAILOVER: a TCP flow survives a WAN dying with NO connection reset.
2. DATA-THRIFT: never duplicate all traffic; keep metered 5G ~idle until needed.
3. BANDWIDTH AGGREGATION on demand (single flow can use both links).
4. FEC: Reed-Solomon parity that masks loss without full duplication.
5. ADAPTIVE FEC: redundancy scales with measured per-path loss (low when clean).
6. DPI RESISTANCE: on-wire traffic must not fingerprint as WireGuard. The custom
   outer framing already destroys the WG wire signature; keep it free of magic
   bytes / fixed offsets and expose amnezia junk/obfuscation params (Jc/Jmin/Jmax,
   S1/S2, H1-H4) as defense-in-depth. NOTE: amnezia 2.0 protocol MIMICRY
   (QUIC/DNS/SIP lookalike) is destroyed by our outer wrapping — treat it as out of
   scope; target is "unidentifiable high-entropy UDP", not protocol mimicry.

FUNCTIONAL:
- TUN I/O, handshake, encryption, rekey: provided by the embedded amneziawg-go engine.
- Custom conn.Bind: per-path UDP socket bound to a source address; endpoint roaming
  per path; outer framing as above; MTU accounting for outer header + WG overhead
  (no fragmentation / ICMP black holes; MSS clamping guidance).
- Receive resequencing buffer (bounded window + timeout) for multipath reordering,
  applied BEFORE delivery to WG (its anti-replay window then never sees pathological
  reorder; verify window size gives margin anyway).
- Per-path quality measurement (RTT/loss/jitter) via PSK-authenticated probe frames
  + outer-seq gap accounting. WG keepalive is per-peer, NOT per-path — path liveness
  is entirely ours.
- Scheduler: active-backup (data-thrift) -> weighted aggregation under load, FEC-aware.
- RS FEC over outer frames: group + emit parity within a deadline; receiver recovers
  <=K losses/group. FEC operates on ciphertext — content-agnostic.
- Adaptive control loop: adjust FEC ratio + scheduler weights with hysteresis (no thrash).
- Path up/down + add/remove; survive edge public-IP change on any path (mobile).

NON-FUNCTIONAL:
- Robust while mobile (path flaps, IP changes) — must never wedge; fail-closed security
  (carries 100% of traffic + holds keys); single static binary + systemd + simple config
  (WG keys/peers + amnezia params + path list + PSK); metrics/logging per path
  (loss/RTT/throughput/FEC-overhead).
- Keep the Bind implementation portable across amneziawg-go and upstream wireguard-go
  (API drift hedge: the fork lags upstream; swapping bases must stay cheap).

PHASES (each independently shippable + verifiable):
- P0 (SPIKE, timeboxed) = embed amneziawg-go with a trivial single-socket pass-through
  Bind. Verify: tunnel passes traffic edge<->concentrator; measure baseline throughput;
  document conn.Bind contract pitfalls (batched send/recv semantics, GSO/GRO paths,
  Endpoint identity model, amnezia junk packets arriving at the Bind).
- P1 (MVP) = transparent failover (single active path + per-path roaming). Verify: kill
  the active WAN mid-SSH/download -> session survives, recovers within N seconds.
- P2 = aggregation + data-thrift policy. Verify: bonded throughput ~ sum; 5G bytes ~0 while
  Starlink healthy.
- P3 = fixed-ratio FEC. Verify: at injected X% loss, >=Y% recovered w/o retransmit; overhead <=Z%.
- P4 = adaptive FEC. Verify: overhead tracks loss; total data <= fixed-FEC baseline for same masking.
- P5 = DPI hardening pass. Verify: outer frames show no fixed bytes/offsets (capture +
  entropy check); amnezia params configurable end-to-end; nDPI/Suricata do not classify
  the flow as WireGuard.

LIBRARIES (evaluate within the decided architecture):
- amneziawg-go (base, decided) | klauspost/reedsolomon (RS FEC, de-facto standard).

KEY RISKS TO INVESTIGATE FIRST (P0 targets most of these):
- conn.Bind API impedance: batch-oriented ReceiveFunc/Send, GSO/GRO fast paths,
  Endpoint identity semantics when hiding N paths behind one virtual endpoint.
- amneziawg-go fork lag vs upstream wireguard-go (missing perf work / API drift).
- WG anti-replay window vs multipath reordering (expected fine at our pps + skew, but
  measure); rekey epochs — use OWN outer-seq, do not reuse the inner WG counter.
- Reorder-buffer tuning vs Starlink jitter; adaptive control-loop STABILITY (the crux);
  FEC grouping latency vs recovery; congestion/bufferbloat (need pacing?).
- Hostile networks that block UDP wholesale (hotels): no in-scope mitigation — document
  as a known limitation (TCP/TLS fallback transport is a non-goal for now).

NON-GOALS: not a general SD-WAN product; no GUI; not >3 links initially; path pinning is
out of scope (done externally); no TCP/TLS fallback transport; no protocol mimicry
(amnezia 2.0 style); do not re-evaluate the base-library decision (kcp-go, quic-go,
plain wireguard-go were considered and rejected in favor of amneziawg-go + custom Bind).
