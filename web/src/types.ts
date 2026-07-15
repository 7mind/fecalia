// Wire-format DTOs mirroring internal/monitor/monitor.go EXACTLY (field names
// per the Go `json:"..."` tags). Keep this file in lockstep with monitor.go —
// it is the frontend's half of the W2 monitoring contract.

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
 * Mirrors monitor.MonitorSnapshot: the full point-in-time payload served by
 * the W2 monitoring HTTP/WebSocket endpoint. peerNames and multiPeer mirror
 * the metrics package's peer-label rule: multiPeer is true, and per-entry
 * `peer` fields are meaningful, only when 2+ peers are bound; on a
 * single-bound-peer source, peer is "" throughout.
 */
export interface MonitorSnapshot {
  paths: PathSnapshot[];
  fec: FECSnapshot[];
  reseq: ReseqSnapshot[];
  aggregation: AggregationSnapshot[];
  session: SessionSnapshot;
  peerNames: string[];
  multiPeer: boolean;
}
