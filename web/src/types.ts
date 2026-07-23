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
  linkBandwidthBps: number;
  linkRttSeconds: number;
  /**
   * Present ONLY when addressing is revealed (loopback-bound monitor); absent
   * (server omits the field, `omitempty`) on a non-loopback binding — see
   * MonitorSnapshot.addressingHidden.
   */
  addressing?: AddressingSnapshot;
}

/** Mirrors monitor.FECSnapshot: one per-peer connection-scoped FEC counter set. */
export interface FECSnapshot {
  peer: string;
  dataPackets: number;
  repairPackets: number;
  recoveredPackets: number;
  unrecoverablePackets: number;
  dataBytes: number;
  repairBytes: number;
  residualLossRatio: number;
  /**
   * Mirrors monitor.FECSnapshot.Adaptive (T263, D96): the adaptive-FEC
   * controller's most recent published decision. Absent (server omits the
   * field, `omitempty`) for a fixed-ratio or FEC-off peer.
   */
  adaptive?: AdaptiveFECStats;
}

/** Mirrors monitor.AdaptiveFECStats: the adaptive-FEC controller's per-drive decision. */
export interface AdaptiveFECStats {
  parity: number;
  smoothedLoss: number;
  eligibleLoss: number;
  eligiblePaths: number;
}

/**
 * Mirrors monitor.ReseqSnapshot: one per-peer resequencer counter set.
 * holds/holdNanos/immediateReleases (T242, D93) mirror reseq.Stats' HoL-stall
 * / hold accounting verbatim.
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
  immediateReleases: number;
}

/**
 * Mirrors monitor.AggregationSnapshot: one per-peer weighted-scheduler
 * aggregation-gate snapshot. Absent for a peer whose scheduler exposes no
 * gate (active-backup).
 */
export interface AggregationSnapshot {
  peer: string;
  aggregating: boolean;
  offeredLoadFps: number;
  engageThresholdFps: number;
  disengageThresholdFps: number;
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
 * REDACTABLE addressing surface: blanked (empty string) on a non-loopback
 * binding while the ordered active/standby shape is preserved.
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
 * addressingHidden is true when the monitor is not loopback-bound and the
 * per-path addressing blocks + endpoint addresses have been redacted
 * server-side; the frontend renders a placeholder and never reconstructs the
 * hidden values.
 *
 * peerSessions (T257) mirrors metrics.PeerSessions(): one entry per bound
 * peer's own WG-session health, following the same peer-label back-compat
 * rule as peerNames/multiPeer. activeExit (T257) is the name of the
 * exit-capable peer currently carrying the default route on a multi-exit
 * edge — "" on the concentrator role and on an edge with no default-route
 * ownership to report. It is a peer NAME, never an address, so it is NOT
 * part of the redactable addressing surface.
 *
 * exitControlAvailable (T280, G32) mirrors the Go RAW loopbackBound verdict —
 * the SAME hard gate the mutating POST /api/exit control enforces
 * server-side — and is deliberately independent of addressingHidden: a
 * reveal_addressing opt-in can set addressingHidden false on a non-loopback
 * bind while exit control stays unavailable there, so the frontend MUST key
 * exit-widget visibility off this field, never off addressingHidden.
 */
export interface MonitorSnapshot {
  paths: PathSnapshot[];
  fec: FECSnapshot[];
  reseq: ReseqSnapshot[];
  aggregation: AggregationSnapshot[];
  session: SessionSnapshot;
  peerNames: string[];
  multiPeer: boolean;
  daemon: DaemonSnapshot;
  endpoints: EndpointSnapshot[];
  peerSessions: PeerSessionSnapshot[];
  activeExit: string;
  wgPublicKeyFingerprint: string;
  addressingHidden: boolean;
  exitControlAvailable: boolean;
}

/**
 * Mirrors the POST /api/exit request body (monitor.exitRequest in
 * internal/monitor/server.go): the name of the exit-capable peer to make
 * active. This is the ONE mutating control call (T258); it is available ONLY on
 * a loopback-bound monitor (a non-loopback bind refuses it with 403, regardless
 * of a valid token). T259/T260 wire the UI switch onto it.
 */
export interface ExitRequest {
  peer: string;
}

/**
 * Mirrors the POST /api/exit 200 response body (monitor.exitResponse): the
 * resulting active exit name (the requested peer, whether an actual switch
 * occurred or an idempotent same-name no-op).
 */
export interface ExitResponse {
  activeExit: string;
}

/**
 * Mirrors the stable error body (monitor.exitError) every non-200 POST
 * /api/exit response carries: 403 (non-loopback bind), 400 (malformed JSON or an
 * unknown/non-exit-capable peer — the body names only the caller-supplied peer,
 * never selector internals), 401 (missing/invalid token), 405 (non-POST method).
 */
export interface ExitError {
  error: string;
}
