// Wire-format DTOs mirroring internal/monitor/monitor.go EXACTLY (field names
// per the Go `json:"..."` tags). Keep this file in lockstep with monitor.go —
// it is the frontend's half of the W2 monitoring contract.

/**
 * Mirrors monitor.AddressingSnapshot: one path's REDACTABLE addressing block
 * (Q61/Q62) — the bound local source address and the current wire remote.
 */
export interface AddressingSnapshot {
  source: string;
  remote: string;
}

/** Mirrors monitor.PathSnapshot: one per-(peer,path) traffic/quality entry. */
export interface PathSnapshot {
  name: string;
  peer: string;
  txBytes: number;
  rxBytes: number;
  throughputBps: number;
  rttSeconds: number;
  jitterSeconds: number;
  loss: number;
  up: boolean;
  bindMode: string;
  boundDevice: string;
  /**
   * Present ONLY when addressing is revealed (loopback bind, or the
   * default-off token-gated reveal_addressing opt-in); absent (server omits
   * the field, `omitempty`) otherwise — see MonitorSnapshot.addressingHidden.
   */
  addressing?: AddressingSnapshot;
}

/**
 * Mirrors monitor.ReseqSnapshot: one per-peer resequencer counter set.
 * holds/holdNanos (T242) mirror reseq.Stats' HoL-stall
 * / hold accounting verbatim. armedDeadlineUnixNano and armedWindowNanos
 * describe the live head-of-line gap and are 0 while none is armed.
 */
export interface ReseqSnapshot {
  peer: string;
  released: number;
  droppedDup: number;
  droppedOld: number;
  droppedSuspect: number;
  skipped: number;
  resyncs: number;
  rebaselines: number;
  holds: number;
  holdNanos: number;
  armedDeadlineUnixNano: number;
  armedWindowNanos: number;
  deadlineWakeups: number;
  gapFills: number;
}

/**
 * Mirrors monitor.SessionSnapshot: connection-scoped WG-session snapshot.
 * lastHandshakeSeconds is zero when no handshake has ever completed
 * (established is then false).
 */
export interface SessionSnapshot {
  established: boolean;
  lastHandshakeSeconds: number;
}

/**
 * Mirrors monitor.DaemonSnapshot: the process-scoped identity fields (role,
 * version/build string, process uptime), shown on any binding.
 */
export interface DaemonSnapshot {
  role: string;
  version: string;
  uptimeSeconds: number;
}

/**
 * Mirrors monitor.EndpointSnapshot: one entry of the ordered hub-endpoint
 * list with its active-vs-standby failover state. `address` is inside the
 * REDACTABLE addressing surface: blanked (empty string) when addressing is
 * NOT revealed (revealed = loopback bind, or the default-off token-gated
 * reveal_addressing opt-in) while the ordered active/standby shape is
 * preserved.
 *
 * `peer` (T257) attributes this entry to the bound edge peer whose OWN
 * endpoint list it belongs to, grouping the flat list into per-peer sections
 * on a multi-exit edge; it follows the same peer-label back-compat rule as
 * every other per-entry `peer` field on this contract — "" on a
 * single-bound-peer source.
 */
export interface EndpointSnapshot {
  peer: string;
  address: string;
  active: boolean;
}

/**
 * Mirrors monitor.PeerSessionSnapshot (T256/T257): one bound peer's OWN
 * WG-session health, distinct from the connection-scoped SessionSnapshot
 * above. `peer` follows the package-wide back-compat rule: meaningful only
 * once 2+ peers are bound; a single-bound-peer snapshot still carries exactly
 * one entry, with peer "".
 */
export interface PeerSessionSnapshot {
  peer: string;
  established: boolean;
  lastHandshakeSeconds: number;
}

/**
 * Mirrors monitor.MonitorSnapshot: the full point-in-time payload served by
 * the W2 monitoring HTTP/WebSocket endpoint. peerNames and multiPeer mirror
 * the metrics package's peer-label rule: multiPeer is true, and per-entry
 * `peer` fields are meaningful, only when 2+ peers are bound; on a
 * single-bound-peer source, peer is "" throughout.
 *
 * wgPublicKeyFingerprint is the truncated local WG public-key fingerprint
 * (Q63 — fingerprint ONLY; there is deliberately NO full-key field on the Go
 * contract, so this file MUST NOT add an optional `wgPublicKey?` either).
 * addressingHidden is true when addressing is NOT revealed (revealed =
 * loopback bind, or the default-off token-gated reveal_addressing opt-in)
 * and the per-path addressing blocks + endpoint addresses have been redacted
 * server-side; the frontend renders a placeholder and never reconstructs the
 * hidden values.
 *
 * peerSessions (T257) mirrors metrics.PeerSessions(): one entry per bound
 * peer's own WG-session health, following the same peer-label back-compat
 * rule as peerNames/multiPeer. activeExit (T257) is the name of the
 * exit-capable peer currently carrying the default route on a multi-exit
 * edge — "" on the concentrator role and on an edge with no default-route
 * ownership to report. It is a peer NAME, never an address, so it is NOT
 * part of the redactable addressing surface. exitCapablePeers is the
 * authoritative config-order set eligible to own the default route; the
 * control applies only with 2+ names, and the frontend must not infer the set
 * from generic endpoint or session telemetry.
 *
 * exitMode is the selected policy (auto or a fixed exit); activeExit is the
 * actual route owner. exitControlAvailable reflects loopback-or-token control
 * authorization, independently of addressingHidden.
 */
export interface MonitorSnapshot {
  paths: PathSnapshot[];
  reseq: ReseqSnapshot[];
  session: SessionSnapshot;
  peerNames: string[];
  multiPeer: boolean;
  daemon: DaemonSnapshot;
  endpoints: EndpointSnapshot[];
  peerSessions: PeerSessionSnapshot[];
  activeExit: string;
  exitMode: string;
  exitCapablePeers: string[];
  wgPublicKeyFingerprint: string;
  addressingHidden: boolean;
  exitControlAvailable: boolean;
}

/**
 * Mirrors the POST /api/exit request body (monitor.exitRequest in
 * internal/monitor/server.go): "auto" or an exit-capable peer name.
 * This is the ONE mutating control call (T258); it is available on
 * a loopback or token-authenticated monitor. T259/T260 wire the UI switch onto it.
 */
export interface ExitRequest {
  peer: string;
}

/**
 * Mirrors the POST /api/exit 200 response body (monitor.exitResponse): the
 * current route owner and the selected policy.
 */
export interface ExitResponse {
  activeExit: string;
  exitMode: string;
}

/**
 * Mirrors the stable error body (monitor.exitError) every non-200 POST
 * /api/exit response carries: 403 (forbidden Host/Origin), 400 (malformed JSON or an
 * unknown/non-exit-capable peer — the body names only the caller-supplied peer,
 * never selector internals), 401 (missing/invalid token), 405 (non-POST method).
 */
export interface ExitError {
  error: string;
}
