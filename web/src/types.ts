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
}

/** Mirrors monitor.ReseqSnapshot: one per-peer resequencer counter set. */
export interface ReseqSnapshot {
  peer: string;
  released: number;
  droppedDup: number;
  droppedOld: number;
  droppedSuspect: number;
  skipped: number;
  resyncs: number;
  rebaselines: number;
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
 */
export interface EndpointSnapshot {
  address: string;
  active: boolean;
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
  wgPublicKeyFingerprint: string;
  addressingHidden: boolean;
}
