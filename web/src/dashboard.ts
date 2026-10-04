import type {
  DaemonSnapshot,
  EndpointSnapshot,
  ExitError,
  ExitRequest,
  ExitResponse,
  LaneSnapshot,
  MonitorSnapshot,
  PathSnapshot,
  PeerSessionSnapshot,
  ReseqSnapshot,
  SessionSnapshot,
  TransportSnapshot,
} from './types';
import { pushSample, renderSparklineSvg } from './sparkline';

// Monitoring dashboard (T168, Q48): renders each MonitorSnapshot pushed by
// the T166 ResilientWsClient. Read-only display EXCEPT the one exit-switch
// control (T260, G28/M107) wired onto the T258 POST /api/exit mutating
// route — see below.
//
// Peer-label rule (mirrors metrics.md / types.ts's multiPeer contract):
// single-bound-peer sources render ONE flat section with no peer label at
// all; multi-peer sources on either role group paths/reseq/
// endpoints into one section PER peer, keyed off snapshot.peerNames (T259,
// G28/M107), each carrying its own session state (from peerSessions) and an
// ACTIVE-EXIT badge when that peer is snapshot.activeExit.
//
// Exit selection (T260, G28/M107): a single top-level <select> listing auto
// and the authoritative snapshot.exitCapablePeers candidate set, issuing a same-
// origin POST /api/exit {peer} on selection. Cookie
// auth rides automatically (ws-client.ts precedent — no token handling
// here). Hidden off the edge role and when !snapshot.exitControlAvailable
// (the server's loopback-or-token availability, independent of addressing
// disclosure) or when fewer than two exit-capable peers exist (no alternate).
// The control stays mounted across snapshot frames so its native popup and
// keyboard focus are not interrupted by live telemetry updates.

interface PathBuffers {
  loss: number[];
  rtt: number[];
  throughput: number[];
}

interface LaneBuffers {
  target: number[];
  send: number[];
  queue: number[];
}

/** Handle returned to the caller: feed it snapshots as they arrive. */
export interface DashboardHandle {
  healthContainer: HTMLElement;
  onSnapshot: (snapshot: MonitorSnapshot) => void;
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

function formatPct(ratio: number): string {
  return `${(ratio * 100).toFixed(2)}%`;
}

function formatMs(seconds: number): string {
  return `${(seconds * 1000).toFixed(1)}ms`;
}

function formatBytes(bytes: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)}${units[i]}`;
}

function formatBytesPerSec(bps: number): string {
  return `${formatBytes(bps)}/s`;
}

function formatHandshakeAge(seconds: number, established: boolean): string {
  if (!established) {
    return 'never';
  }
  return `${seconds.toFixed(0)}s ago`;
}

/** Humanizes a process-uptime duration, e.g. `90061` -> `"1d 1h 1m 1s"`. */
function formatUptime(seconds: number): string {
  const total = Math.floor(seconds);
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const parts: string[] = [];
  if (days > 0) {
    parts.push(`${days}d`);
  }
  if (days > 0 || hours > 0) {
    parts.push(`${hours}h`);
  }
  if (days > 0 || hours > 0 || minutes > 0) {
    parts.push(`${minutes}m`);
  }
  parts.push(`${secs}s`);
  return parts.join(' ');
}

/** Groups an array of peer-tagged entries by their `peer` field, preserving order within each group. */
function groupByPeer<T extends { peer: string }>(items: T[]): Map<string, T[]> {
  const map = new Map<string, T[]>();
  for (const item of items) {
    const list = map.get(item.peer);
    if (list) {
      list.push(item);
    } else {
      map.set(item.peer, [item]);
    }
  }
  return map;
}

/** Mutable state for the exit-switch control, held outside the per-frame render (T260). */
interface ExitControlState {
  pending: boolean;
  /** Adopted from a 2xx POST /api/exit response; cleared once the next snapshot frame reconciles it. */
  optimisticActiveExit: string | null;
  optimisticMode: string | null;
  error: string | null;
}

/**
 * Mounts the dashboard into `container`. Returns a handle whose onSnapshot
 * callback updates the control and re-renders the telemetry from the latest MonitorSnapshot,
 * accumulating client-side sparkline history across calls.
 */
export function mountDashboard(container: HTMLElement): DashboardHandle {
  const root = document.createElement('div');
  root.className = 'dashboard';
  const topbar = document.createElement('header');
  topbar.className = 'topbar';
  const header = document.createElement('div');
  const healthContainer = document.createElement('div');
  healthContainer.className = 'connection-status';
  topbar.append(header, healthContainer);
  const control = document.createElement('div');
  control.className = 'dashboard-controls';
  const telemetry = document.createElement('div');
  telemetry.className = 'dashboard-telemetry';
  root.append(topbar, control, telemetry);
  container.appendChild(root);

  // Rolling sparkline history, kept in memory only (Q50) — never sent
  // anywhere, never persisted, gone on reload. Keyed so multi-peer streams
  // don't collide across peers sharing a path name.
  const pathBuffers = new Map<string, PathBuffers>();

  function pathBufferFor(key: string): PathBuffers {
    let b = pathBuffers.get(key);
    if (!b) {
      b = { loss: [], rtt: [], throughput: [] };
      pathBuffers.set(key, b);
    }
    return b;
  }

  function renderPathCard(p: PathSnapshot, addressingHidden: boolean): string {
    const buf = pathBufferFor(`${p.peer} ${p.name}`);
    pushSample(buf.loss, p.loss);
    pushSample(buf.rtt, p.rttSeconds);
    pushSample(buf.throughput, p.throughputBps);
    const bindLabel = p.boundDevice ? `${escapeHtml(p.bindMode)} (${escapeHtml(p.boundDevice)})` : escapeHtml(p.bindMode);
    // Addressing is optional per path (Q61/Q62): present only when the
    // monitor is loopback-bound. The client trusts the server's redaction —
    // it never reconstructs a hidden source/remote from other fields.
    let addressingRow: string;
    if (p.addressing) {
      addressingRow = `
          <tr data-testid="addressing"><td>src</td><td colspan="2">${escapeHtml(p.addressing.source)}</td></tr>
          <tr><td>remote</td><td colspan="2">${escapeHtml(p.addressing.remote)}</td></tr>`;
    } else if (addressingHidden) {
      addressingRow = `
          <tr data-testid="addressing-hidden"><td colspan="3">addressing hidden on non-loopback binding</td></tr>`;
    } else {
      addressingRow = '';
    }
    return `
      <div class="path-card ${p.up ? 'is-up' : 'is-down'}" data-testid="path-card" data-path="${escapeHtml(p.name)}">
        <div class="path-card-heading">
          <strong>${escapeHtml(p.name)}</strong>
          <span class="state-pill">${p.up ? 'UP' : 'DOWN'}</span>
        </div>
        <table>
          <tr><td>loss</td><td>${formatPct(p.loss)}</td><td>${renderSparklineSvg(buf.loss)}</td></tr>
          <tr><td>RTT</td><td>${formatMs(p.rttSeconds)}</td><td>${renderSparklineSvg(buf.rtt)}</td></tr>
          <tr><td>jitter</td><td>${formatMs(p.jitterSeconds)}</td><td></td></tr>
          <tr><td>throughput</td><td>${formatBytesPerSec(p.throughputBps / 8)}</td><td>${renderSparklineSvg(buf.throughput)}</td></tr>
          <tr><td>tx / rx</td><td colspan="2">${formatBytes(p.txBytes)} / ${formatBytes(p.rxBytes)}</td></tr>
          <tr><td>bind</td><td colspan="2" data-testid="path-bind">${bindLabel}</td></tr>
        </table>
        ${addressingRow ? `<details class="path-details" data-path-detail="${encodeURIComponent(p.peer)}:${encodeURIComponent(p.name)}"><summary>Path details</summary><table>${addressingRow}</table></details>` : ''}
      </div>`;
  }

  const laneBuffers = new Map<string, LaneBuffers>();

  function laneBufferFor(key: string): LaneBuffers {
    let b = laneBuffers.get(key);
    if (!b) {
      b = { target: [], send: [], queue: [] };
      laneBuffers.set(key, b);
    }
    return b;
  }

  // A lane is one local path paired with one of the peer's. Its target is
  // held just below the capacity it has demonstrated; a queue delay above
  // the threshold is what its control takes for congestion, and the counters
  // say what it concluded.
  function renderLaneCard(l: LaneSnapshot): string {
    const buf = laneBufferFor(`${l.peer} ${l.lane}`);
    pushSample(buf.target, l.targetBps);
    pushSample(buf.send, l.sendBps);
    pushSample(buf.queue, l.queueDelaySeconds);
    const state = !l.up ? 'DOWN' : l.discovering ? 'PROBING' : 'HOLD';
    const capacity = l.capacityBps > 0 ? formatBytesPerSec(l.capacityBps / 8) : 'unknown';
    return `
      <div class="path-card lane-card ${l.up ? 'is-up' : 'is-down'}" data-testid="lane-card" data-lane="${l.lane}">
        <div class="path-card-heading">
          <strong>${escapeHtml(l.path)} #${l.remotePath}</strong>
          <span class="state-pill" data-testid="lane-state">${state}</span>
        </div>
        <table>
          <tr><td>target</td><td>${formatBytesPerSec(l.targetBps / 8)}</td><td>${renderSparklineSvg(buf.target)}</td></tr>
          <tr><td>sent</td><td>${formatBytesPerSec(l.sendBps / 8)}</td><td>${renderSparklineSvg(buf.send)}</td></tr>
          <tr><td>delivered</td><td colspan="2">${formatBytesPerSec(l.deliveryBps / 8)}</td></tr>
          <tr><td>capacity</td><td colspan="2" data-testid="lane-capacity">${capacity}</td></tr>
          <tr><td>queue</td><td data-testid="lane-queue">${formatMs(l.queueDelaySeconds)} of ${formatMs(l.thresholdSeconds)}</td><td>${renderSparklineSvg(buf.queue)}</td></tr>
          <tr><td>RTT</td><td colspan="2">${formatMs(l.rttSeconds)}</td></tr>
          <tr><td>in flight</td><td colspan="2">${formatBytes(l.inFlightBytes)} of ${formatBytes(l.windowBytes)}</td></tr>
        </table>
        <details class="path-details" data-path-detail="lane:${encodeURIComponent(l.peer)}:${l.lane}"><summary>Control decisions</summary><table data-testid="lane-decisions">
          <tr><td>signals</td><td>delay ${l.delaySignals} · loss ${l.lossSignals} · stall ${l.stallSignals}</td></tr>
          <tr><td>probes</td><td>${l.pulses} · won ${l.pulseWins} · lost ${l.pulseLosses}</td></tr>
          <tr><td>estimate</td><td>remeasured ${l.capacityRemeasured} · decayed ${l.capacityDecays}</td></tr>
          <tr><td>discovery</td><td>ended by congestion ${l.discoveryCongested} · plateau ${l.discoveryPlateau} · restarted ${l.rediscoveries}</td></tr>
          <tr><td>repairs</td><td>${l.repairs}</td></tr>
          <tr><td>sent / acked</td><td>${formatBytes(l.sentBytes)} / ${formatBytes(l.ackedBytes)}</td></tr>
        </table></details>
      </div>`;
  }

  function renderTransportCard(q: TransportSnapshot): string {
    return `
      <div class="reseq-card" data-testid="transport-card">
        <table>
          <tr><td>dropped</td><td>${q.queueDrops}</td><td>queue full</td><td>${q.admissionDrops}</td></tr>
          <tr><td>AQM</td><td>${q.aqmDrops}</td><td>small dropped</td><td>${q.interactiveDrops}</td></tr>
          <tr><td>expired</td><td>${q.expired}</td><td>duplicates</td><td>${q.duplicates}</td></tr>
          <tr><td>small waiting</td><td>${q.interactiveQueued}</td><td>ACKs coalesced</td><td>${q.coalescedAcks}</td></tr>
        </table>
      </div>`;
  }

  function renderReseqCard(r: ReseqSnapshot): string {
    return `
      <div class="reseq-card" data-testid="reseq-card">
        <table>
          <tr><td>released</td><td>${r.released}</td><td>skipped</td><td>${r.skipped}</td></tr>
          <tr><td>dup dropped</td><td>${r.droppedDup}</td><td>old dropped</td><td>${r.droppedOld}</td></tr>
          <tr><td>suspect dropped</td><td>${r.droppedSuspect}</td><td>resyncs</td><td>${r.resyncs}</td></tr>
          <tr><td>rebaselines</td><td colspan="3">${r.rebaselines}</td></tr>
        </table>
      </div>`;
  }

  function renderSessionCard(s: SessionSnapshot, kind: 'session-card' | 'peer-session-card'): string {
    return `
      <div class="${kind} ${s.established ? 'is-up' : 'is-down'}" data-testid="${kind}">
        <span class="session-label">WG session</span>
        <span class="state-text">${s.established ? 'ESTABLISHED' : 'NOT ESTABLISHED'}</span>
        <span class="session-handshake">last handshake ${formatHandshakeAge(s.lastHandshakeSeconds, s.established)}</span>
      </div>`;
  }

  function renderDaemonHeader(d: DaemonSnapshot): string {
    return `
      <div class="daemon-header" data-testid="daemon-header">
        <div class="brand"><span class="brand-mark">w.</span><div><strong>wanbond</strong><small>Network monitor</small></div></div>
        <div class="daemon-meta"><span class="role-badge" data-testid="role-badge">${escapeHtml(d.role)}</span>
        <span data-testid="daemon-version">v${escapeHtml(d.version)}</span>
        <span data-testid="daemon-commit">Commit ${escapeHtml(d.buildCommit || 'unknown')}</span>
        <span data-testid="daemon-commit-time">Commit time ${escapeHtml(d.buildCommitTime || 'unknown')}</span>
        <span data-testid="daemon-uptime">Up ${formatUptime(d.uptimeSeconds)}</span></div>
      </div>`;
  }

  function renderWgKeyLine(fingerprint: string): string {
    // Q63/R242: the contract carries a truncated fingerprint only — there is
    // deliberately no full WG public key to render.
    return `
      <div class="wg-key-line" data-testid="wg-key-line">
        WG key: <code>${escapeHtml(fingerprint)}</code>
      </div>`;
  }

  function renderEndpointsSection(endpoints: EndpointSnapshot[], addressingHidden: boolean): string {
    // R242: an empty endpoint list (concentrator role, no configured
    // failover endpoints) omits the whole section — never render an empty
    // "active" row.
    if (endpoints.length === 0) {
      return '';
    }
    const rows = endpoints
      .map((e) => {
        // Q62: a blanked address (empty string) on a redacted (non-loopback)
        // binding is the server-side redaction, not a data gap — render the
        // same established placeholder copy as path addressing, never the
        // raw empty string.
        const addressCell =
          e.address === '' && addressingHidden
            ? `<span data-testid="endpoint-address-hidden">hidden on non-loopback binding</span>`
            : escapeHtml(e.address);
        return `
        <div class="endpoint-row" data-testid="endpoint-row" data-active="${e.active}">
          <span>${e.active ? 'ACTIVE' : 'standby'}</span> <span>${addressCell}</span>
        </div>`;
      })
      .join('');
    return `
      <div class="stat-group" data-kind="endpoints" data-testid="stat-group-endpoints">
        <h4>Endpoints</h4>
        ${rows}
      </div>`;
  }

  function renderExitControl(candidates: string[], activeExit: string, mode: string): string {
    const options = ['auto', ...candidates]
      .map((peer) => {
        const isSelected = peer === mode;
        return `<option value="${escapeHtml(peer)}" ${isSelected ? 'selected' : ''}>${escapeHtml(peer)}</option>`;
      })
      .join('');
    return `
      <div class="exit-control" data-testid="exit-control">
        <div class="exit-control-fields"><label for="exit-control-select">Exit policy</label>
        <select id="exit-control-select" data-testid="exit-control-select">${options}</select>
        <span data-testid="exit-control-active">Active: ${escapeHtml(activeExit)}</span>
        <span class="exit-control-hint">Auto follows the lowest healthy RTT.</span>
        <span class="exit-control-notes" aria-live="polite"></span></div>
      </div>`;
  }

  function renderSection(
    peerLabel: string | null,
    paths: PathSnapshot[],
    lanes: LaneSnapshot[],
    transport: TransportSnapshot[],
    reseq: ReseqSnapshot[],
    addressingHidden: boolean,
    endpoints: EndpointSnapshot[] = [],
    peerSession?: PeerSessionSnapshot,
    isActiveExit = false,
  ): string {
    const activeExitBadge = isActiveExit
      ? `<span class="active-exit-badge" data-testid="active-exit-badge">ACTIVE-EXIT</span>`
      : '';
    // Per-concentrator session state (T259, G28/M107): each peer's OWN
    // WG-session health, sourced from MonitorSnapshot.peerSessions — distinct
    // from the connection-scoped SessionSnapshot rendered once outside every
    // section. Only present in grouped (multiPeer) mode.
    const heading =
      peerLabel !== null
        ? `<div class="peer-heading"><h3 class="peer-label" data-testid="peer-label">${escapeHtml(peerLabel)}${activeExitBadge}</h3>${peerSession ? renderSessionCard(peerSession, 'peer-session-card') : ''}</div>`
        : '';
    const endpointsGroup = renderEndpointsSection(endpoints, addressingHidden);
    return `
      <section class="${peerLabel !== null ? 'dashboard-peer-section' : 'dashboard-flat-section'}"
               data-testid="${peerLabel !== null ? 'peer-section' : 'flat-section'}"
               ${peerLabel !== null ? `data-peer="${escapeHtml(peerLabel)}"` : ''}>
        ${heading}
        <div class="stat-group" data-kind="paths" data-testid="stat-group-paths">
          <h4>Paths</h4>
          <div class="path-grid">${paths.map((p) => renderPathCard(p, addressingHidden)).join('')}</div>
        </div>
        ${lanes.length > 0 ? `<div class="stat-group" data-kind="lanes" data-testid="stat-group-lanes">
          <h4>Transport lanes</h4>
          <div class="path-grid">${lanes.map((l) => renderLaneCard(l)).join('')}</div>
        </div>` : ''}
        ${transport.length > 0 ? `<div class="stat-group" data-kind="transport" data-testid="stat-group-transport">
          <h4>Transport queue</h4>
          ${transport.map((q) => renderTransportCard(q)).join('')}
        </div>` : ''}
        <div class="stat-group" data-kind="reseq" data-testid="stat-group-reseq">
          <h4>Resequencer</h4>
          ${reseq.map((r) => renderReseqCard(r)).join('')}
        </div>
        ${endpointsGroup}
      </section>`;
  }

  // Held outside render(): command state survives every telemetry frame.
  let lastSnapshot: MonitorSnapshot | null = null;
  let controlCandidates = '';
  const exitControlState: ExitControlState = {
    pending: false,
    optimisticActiveExit: null,
    optimisticMode: null,
    error: null,
  };

  function handleExitChange(peer: string): void {
    exitControlState.pending = true;
    exitControlState.error = null;
    render();

    const body: ExitRequest = { peer };
    fetch('/api/exit', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
      .then(async (res) => {
        if (res.ok) {
          const parsed = (await res.json()) as ExitResponse;
          exitControlState.optimisticActiveExit = parsed.activeExit;
          exitControlState.optimisticMode = parsed.exitMode;
        } else {
          let message = `exit switch failed (HTTP ${res.status})`;
          try {
            const parsed = (await res.json()) as ExitError;
            if (parsed.error) {
              message = parsed.error;
            }
          } catch {
            // non-JSON error body: keep the generic status message
          }
          exitControlState.error = message;
        }
      })
      .catch(() => {
        exitControlState.error = 'network error switching exit';
      })
      .finally(() => {
        exitControlState.pending = false;
        render();
      });
  }

  function render(): void {
    const snapshot = lastSnapshot;
    if (snapshot === null) {
      return;
    }
    // Reconcile the optimistic override with the server's own truth on
    // every real snapshot frame (T260) — a stale client-side guess never
    // outlives the next push.
    const effectiveActiveExit = exitControlState.optimisticActiveExit ?? snapshot.activeExit;
    const effectiveMode = exitControlState.optimisticMode ?? snapshot.exitMode;

    let sectionsHtml: string;
    const grouped = snapshot.multiPeer && snapshot.peerNames.length > 1;
    if (grouped) {
      const pathsByPeer = groupByPeer(snapshot.paths);
      const lanesByPeer = groupByPeer(snapshot.lanes);
      const transportByPeer = groupByPeer(snapshot.transport);
      const reseqByPeer = groupByPeer(snapshot.reseq);
      const endpointsByPeer = groupByPeer(snapshot.endpoints);
      const peerSessionsByPeer = groupByPeer(snapshot.peerSessions);
      sectionsHtml = snapshot.peerNames
        .map((peer) =>
          renderSection(
            peer,
            pathsByPeer.get(peer) ?? [],
            lanesByPeer.get(peer) ?? [],
            transportByPeer.get(peer) ?? [],
            reseqByPeer.get(peer) ?? [],
            snapshot.addressingHidden,
            endpointsByPeer.get(peer) ?? [],
            peerSessionsByPeer.get(peer)?.[0],
            effectiveActiveExit !== '' && effectiveActiveExit === peer,
          ),
        )
        .join('');
    } else {
      // Single-peer (edge): one flat section, no peer label, no per-peer
      // endpoints/session/active-exit grouping — matches the metrics
      // package's peer-label omission when only one peer is bound. Endpoints
      // are rendered in their own top-level section below, as before T259.
      sectionsHtml = renderSection(
        null,
        snapshot.paths,
        snapshot.lanes,
        snapshot.transport,
        snapshot.reseq,
        snapshot.addressingHidden,
      );
    }

    // Exit-switch control (T260, re-keyed T280/G32): hidden entirely when the
    // monitor's loopback-or-token control is unavailable or on a single-peer
    // snapshot — there is no alternate exit to switch to with only one peer
    // bound. Deliberately keyed on exitControlAvailable, not addressingHidden:
    // disclosure and control authorization are independent.
    const controlAvailable = snapshot.daemon.role === 'edge' && snapshot.exitControlAvailable && grouped && snapshot.exitCapablePeers.length >= 2;
    if (controlAvailable) {
      const candidates = JSON.stringify(snapshot.exitCapablePeers);
      if (controlCandidates !== candidates) {
        control.innerHTML = renderExitControl(snapshot.exitCapablePeers, effectiveActiveExit, effectiveMode);
        controlCandidates = candidates;
        const select = control.querySelector<HTMLSelectElement>('select')!;
        select.addEventListener('change', () => handleExitChange(select.value));
      }
      const select = control.querySelector<HTMLSelectElement>('select')!;
      select.disabled = exitControlState.pending;
      if (!exitControlState.pending && (document.activeElement !== select || exitControlState.error !== null || exitControlState.optimisticMode !== null)) {
        select.value = effectiveMode;
      }
      control.querySelector<HTMLElement>('[data-testid="exit-control-active"]')!.textContent = `Active: ${effectiveActiveExit}`;
      const notes = control.querySelector<HTMLElement>('.exit-control-notes')!;
      notes.replaceChildren();
      if (exitControlState.pending) {
        const pending = document.createElement('span');
        pending.dataset.testid = 'exit-control-pending';
        pending.textContent = 'Switching…';
        notes.appendChild(pending);
      }
      if (exitControlState.error !== null) {
        const error = document.createElement('span');
        error.className = 'exit-control-error';
        error.dataset.testid = 'exit-control-error';
        error.textContent = exitControlState.error;
        notes.appendChild(error);
      }
    } else {
      control.replaceChildren();
      controlCandidates = '';
    }

    header.innerHTML = `${renderDaemonHeader(snapshot.daemon)}<div class="daemon-status">${renderSessionCard(snapshot.session, 'session-card')}${renderWgKeyLine(snapshot.wgPublicKeyFingerprint)}</div>`;
    const openDetails = new Set(Array.from(telemetry.querySelectorAll<HTMLDetailsElement>('details.path-details[open]'), (detail) => detail.dataset.pathDetail));
    telemetry.innerHTML = `
      ${sectionsHtml}
      ${grouped ? '' : renderEndpointsSection(snapshot.endpoints, snapshot.addressingHidden)}`;
    for (const detail of telemetry.querySelectorAll<HTMLDetailsElement>('details.path-details')) {
      detail.open = openDetails.has(detail.dataset.pathDetail);
    }
  }

  function onSnapshot(snapshot: MonitorSnapshot): void {
    lastSnapshot = snapshot;
    exitControlState.optimisticActiveExit = null;
    exitControlState.optimisticMode = null;
    render();
  }

  return { healthContainer, onSnapshot };
}
