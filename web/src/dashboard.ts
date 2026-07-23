import type {
  AggregationSnapshot,
  DaemonSnapshot,
  EndpointSnapshot,
  ExitError,
  ExitRequest,
  ExitResponse,
  FECSnapshot,
  MonitorSnapshot,
  PathSnapshot,
  PeerSessionSnapshot,
  ReseqSnapshot,
  SessionSnapshot,
} from './types';
import { pushSample, renderSparklineSvg } from './sparkline';

// Monitoring dashboard (T168, Q48): renders each MonitorSnapshot pushed by
// the T166 ResilientWsClient. Read-only display EXCEPT the one exit-switch
// control (T260, G28/M107) wired onto the T258 POST /api/exit mutating
// route — see below.
//
// Peer-label rule (mirrors metrics.md / types.ts's multiPeer contract):
// single-bound-peer sources render ONE flat section with no peer label at
// all; multi-peer (concentrator) sources group paths/FEC/reseq/aggregation/
// endpoints into one section PER peer, keyed off snapshot.peerNames (T259,
// G28/M107), each carrying its own session state (from peerSessions) and an
// ACTIVE-EXIT badge when that peer is snapshot.activeExit.
//
// Exit-switch control (T260, G28/M107): a single top-level <select> listing
// every exit-capable peer (those named in endpoints/peerSessions — the
// concentrator/remote-monitor case has none of either, so nothing renders
// there), issuing a same-origin POST /api/exit {peer} on selection. Cookie
// auth rides automatically (ws-client.ts precedent — no token handling
// here). Hidden entirely when snapshot.addressingHidden (mirrors the
// server's 403 gate — a remote/token'd monitor is read-only, so don't even
// offer the control) or on a single-peer snapshot (no alternate to switch
// to). Pending/error state is held OUTSIDE onSnapshot's per-frame render (in
// `exitControlState`, a mountDashboard-scoped closure variable) so it
// survives the innerHTML replacement every snapshot frame triggers; the
// change listener is re-attached after every render for the same reason.

interface PathBuffers {
  loss: number[];
  rtt: number[];
  throughput: number[];
}

/** Handle returned to the caller: feed it snapshots as they arrive. */
export interface DashboardHandle {
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

/** Derives the exit-capable candidate list: peerNames that appear in endpoints or peerSessions. */
function exitCapablePeers(snapshot: MonitorSnapshot): string[] {
  const named = new Set<string>();
  for (const e of snapshot.endpoints) {
    if (e.peer !== '') {
      named.add(e.peer);
    }
  }
  for (const s of snapshot.peerSessions) {
    if (s.peer !== '') {
      named.add(s.peer);
    }
  }
  return snapshot.peerNames.filter((p) => named.has(p));
}

/** Mutable state for the exit-switch control, held outside the per-frame render (T260). */
interface ExitControlState {
  pending: boolean;
  /** Adopted from a 2xx POST /api/exit response; cleared once the next snapshot frame reconciles it. */
  optimisticActiveExit: string | null;
  error: string | null;
}

/**
 * Mounts the dashboard into `container`. Returns a handle whose onSnapshot
 * callback re-renders the whole tree from the latest MonitorSnapshot,
 * accumulating client-side sparkline history across calls.
 */
export function mountDashboard(container: HTMLElement): DashboardHandle {
  const root = document.createElement('div');
  root.className = 'dashboard';
  root.style.cssText = 'font-family:system-ui,sans-serif; font-size:0.85em; display:flex; flex-direction:column; gap:1em; margin-top:1em;';
  container.appendChild(root);

  // Rolling sparkline history, kept in memory only (Q50) — never sent
  // anywhere, never persisted, gone on reload. Keyed so multi-peer streams
  // don't collide across peers sharing a path name.
  const pathBuffers = new Map<string, PathBuffers>();
  const fecBuffers = new Map<string, number[]>();

  function pathBufferFor(key: string): PathBuffers {
    let b = pathBuffers.get(key);
    if (!b) {
      b = { loss: [], rtt: [], throughput: [] };
      pathBuffers.set(key, b);
    }
    return b;
  }

  function fecBufferFor(key: string): number[] {
    let b = fecBuffers.get(key);
    if (!b) {
      b = [];
      fecBuffers.set(key, b);
    }
    return b;
  }

  function renderPathCard(p: PathSnapshot, addressingHidden: boolean): string {
    const buf = pathBufferFor(`${p.peer} ${p.name}`);
    pushSample(buf.loss, p.loss);
    pushSample(buf.rtt, p.rttSeconds);
    pushSample(buf.throughput, p.throughputBps);
    const stateColor = p.up ? '#2e7d32' : '#c62828';
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
      <div class="path-card" data-testid="path-card" data-path="${escapeHtml(p.name)}" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; min-width:16em;">
        <div style="display:flex; justify-content:space-between; align-items:center;">
          <strong>${escapeHtml(p.name)}</strong>
          <span style="color:${stateColor}; font-weight:600;">${p.up ? 'UP' : 'DOWN'}</span>
        </div>
        <table style="width:100%; border-collapse:collapse;">
          <tr><td>loss</td><td>${formatPct(p.loss)}</td><td>${renderSparklineSvg(buf.loss)}</td></tr>
          <tr><td>RTT</td><td>${formatMs(p.rttSeconds)}</td><td>${renderSparklineSvg(buf.rtt)}</td></tr>
          <tr><td>jitter</td><td>${formatMs(p.jitterSeconds)}</td><td></td></tr>
          <tr><td>throughput</td><td>${formatBytesPerSec(p.throughputBps)}</td><td>${renderSparklineSvg(buf.throughput)}</td></tr>
          <tr><td>tx / rx</td><td colspan="2">${formatBytes(p.txBytes)} / ${formatBytes(p.rxBytes)}</td></tr>
          <tr><td>bind</td><td colspan="2" data-testid="path-bind">${bindLabel}</td></tr>
          <tr><td>link</td><td colspan="2" data-testid="path-link">${formatBytesPerSec(p.linkBandwidthBps)} / ${formatMs(p.linkRttSeconds)}</td></tr>
          ${addressingRow}
        </table>
      </div>`;
  }

  function renderFecCard(f: FECSnapshot, bufferKey: string): string {
    const buf = fecBufferFor(bufferKey);
    pushSample(buf, f.residualLossRatio);
    return `
      <div class="fec-card" data-testid="fec-card" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; margin-bottom:0.5em;">
        <table style="width:100%; border-collapse:collapse;">
          <tr><td>data pkts</td><td>${f.dataPackets}</td><td>repair pkts</td><td>${f.repairPackets}</td></tr>
          <tr><td>recovered</td><td>${f.recoveredPackets}</td><td>unrecoverable</td><td>${f.unrecoverablePackets}</td></tr>
          <tr><td>data bytes</td><td>${formatBytes(f.dataBytes)}</td><td>repair bytes</td><td>${formatBytes(f.repairBytes)}</td></tr>
          <tr><td>residual loss</td><td colspan="3">${formatPct(f.residualLossRatio)} ${renderSparklineSvg(buf)}</td></tr>
        </table>
      </div>`;
  }

  function renderReseqCard(r: ReseqSnapshot): string {
    return `
      <div class="reseq-card" data-testid="reseq-card" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; margin-bottom:0.5em;">
        <table style="width:100%; border-collapse:collapse;">
          <tr><td>released</td><td>${r.released}</td><td>skipped</td><td>${r.skipped}</td></tr>
          <tr><td>dup dropped</td><td>${r.droppedDup}</td><td>old dropped</td><td>${r.droppedOld}</td></tr>
          <tr><td>suspect dropped</td><td>${r.droppedSuspect}</td><td>resyncs</td><td>${r.resyncs}</td></tr>
          <tr><td>rebaselines</td><td colspan="3">${r.rebaselines}</td></tr>
        </table>
      </div>`;
  }

  function renderAggregationCard(a: AggregationSnapshot): string {
    return `
      <div class="aggregation-card" data-testid="aggregation-card" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; margin-bottom:0.5em;">
        aggregating: ${a.aggregating ? 'yes' : 'no'} &middot;
        offered: ${a.offeredLoadFps.toFixed(1)}fps &middot;
        engage: ${a.engageThresholdFps.toFixed(1)}fps &middot;
        disengage: ${a.disengageThresholdFps.toFixed(1)}fps
      </div>`;
  }

  function renderSessionCard(s: SessionSnapshot): string {
    const color = s.established ? '#2e7d32' : '#c62828';
    return `
      <div class="session-card" data-testid="session-card" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; display:inline-block;">
        <span style="color:${color}; font-weight:600;">${s.established ? 'ESTABLISHED' : 'NOT ESTABLISHED'}</span>
        <span> &middot; last handshake ${formatHandshakeAge(s.lastHandshakeSeconds, s.established)}</span>
      </div>`;
  }

  function renderDaemonHeader(d: DaemonSnapshot): string {
    return `
      <div class="daemon-header" data-testid="daemon-header" style="display:flex; gap:1em; align-items:center;">
        <span class="role-badge" data-testid="role-badge" style="text-transform:uppercase; font-weight:600; border:1px solid #888; border-radius:4px; padding:0.1em 0.5em;">${escapeHtml(d.role)}</span>
        <span data-testid="daemon-version">v${escapeHtml(d.version)}</span>
        <span data-testid="daemon-uptime">up ${formatUptime(d.uptimeSeconds)}</span>
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
        const style = e.active ? 'font-weight:600; color:#2e7d32;' : 'color:#666;';
        // Q62: a blanked address (empty string) on a redacted (non-loopback)
        // binding is the server-side redaction, not a data gap — render the
        // same established placeholder copy as path addressing, never the
        // raw empty string.
        const addressCell =
          e.address === '' && addressingHidden
            ? `<span data-testid="endpoint-address-hidden">hidden on non-loopback binding</span>`
            : escapeHtml(e.address);
        return `
        <div class="endpoint-row" data-testid="endpoint-row" data-active="${e.active}" style="${style}">
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

  function renderPeerSessionCard(s: PeerSessionSnapshot): string {
    const color = s.established ? '#2e7d32' : '#c62828';
    return `
      <div class="stat-group" data-kind="peer-session" data-testid="stat-group-peer-session">
        <div class="peer-session-card" data-testid="peer-session-card" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; display:inline-block;">
          <span style="color:${color}; font-weight:600;">${s.established ? 'ESTABLISHED' : 'NOT ESTABLISHED'}</span>
          <span> &middot; last handshake ${formatHandshakeAge(s.lastHandshakeSeconds, s.established)}</span>
        </div>
      </div>`;
  }

  function renderExitControl(candidates: string[], activeExit: string, state: ExitControlState): string {
    if (candidates.length === 0) {
      return '';
    }
    const options = candidates
      .map((peer) => {
        const isActive = peer === activeExit;
        return `<option value="${escapeHtml(peer)}" ${isActive ? 'selected' : ''}>${escapeHtml(peer)}${isActive ? ' (active)' : ''}</option>`;
      })
      .join('');
    const pendingNote = state.pending
      ? `<span data-testid="exit-control-pending" style="color:#666;">switching&hellip;</span>`
      : '';
    const errorNote = state.error
      ? `<div class="exit-control-error" data-testid="exit-control-error" style="color:#c62828;">${escapeHtml(state.error)}</div>`
      : '';
    return `
      <div class="exit-control" data-testid="exit-control" style="border:1px solid #ccc; border-radius:6px; padding:0.5em; display:flex; align-items:center; gap:0.5em; flex-wrap:wrap;">
        <label for="exit-control-select">Active exit</label>
        <select id="exit-control-select" data-testid="exit-control-select" ${state.pending ? 'disabled' : ''}>${options}</select>
        ${pendingNote}
        ${errorNote}
      </div>`;
  }

  function renderSection(
    peerLabel: string | null,
    paths: PathSnapshot[],
    fec: FECSnapshot[],
    reseq: ReseqSnapshot[],
    aggregation: AggregationSnapshot[],
    addressingHidden: boolean,
    endpoints: EndpointSnapshot[] = [],
    peerSession?: PeerSessionSnapshot,
    isActiveExit = false,
  ): string {
    const keyPrefix = peerLabel ?? ' flat';
    const activeExitBadge = isActiveExit
      ? `<span class="active-exit-badge" data-testid="active-exit-badge" style="text-transform:uppercase; font-weight:600; color:#fff; background:#2e7d32; border-radius:4px; padding:0.1em 0.5em; margin-left:0.5em;">ACTIVE-EXIT</span>`
      : '';
    const heading =
      peerLabel !== null
        ? `<h3 class="peer-label" data-testid="peer-label">${escapeHtml(peerLabel)}${activeExitBadge}</h3>`
        : '';
    const aggregationGroup =
      aggregation.length > 0
        ? `
      <div class="stat-group" data-kind="aggregation" data-testid="stat-group-aggregation">
        <h4>Aggregation</h4>
        ${aggregation.map((a) => renderAggregationCard(a)).join('')}
      </div>`
        : '';
    // Per-concentrator session state (T259, G28/M107): each peer's OWN
    // WG-session health, sourced from MonitorSnapshot.peerSessions — distinct
    // from the connection-scoped SessionSnapshot rendered once outside every
    // section. Only present in grouped (multiPeer) mode.
    const peerSessionGroup = peerSession ? renderPeerSessionCard(peerSession) : '';
    const endpointsGroup = renderEndpointsSection(endpoints, addressingHidden);
    return `
      <section class="${peerLabel !== null ? 'dashboard-peer-section' : 'dashboard-flat-section'}"
               data-testid="${peerLabel !== null ? 'peer-section' : 'flat-section'}"
               ${peerLabel !== null ? `data-peer="${escapeHtml(peerLabel)}"` : ''}>
        ${heading}
        ${peerSessionGroup}
        <div class="stat-group" data-kind="paths" data-testid="stat-group-paths">
          <h4>Paths</h4>
          <div style="display:flex; flex-wrap:wrap; gap:0.5em;">${paths.map((p) => renderPathCard(p, addressingHidden)).join('')}</div>
        </div>
        <div class="stat-group" data-kind="fec" data-testid="stat-group-fec">
          <h4>FEC</h4>
          ${fec.map((f, i) => renderFecCard(f, `${keyPrefix} fec ${i}`)).join('')}
        </div>
        <div class="stat-group" data-kind="reseq" data-testid="stat-group-reseq">
          <h4>Resequencer</h4>
          ${reseq.map((r) => renderReseqCard(r)).join('')}
        </div>
        ${aggregationGroup}
        ${endpointsGroup}
      </section>`;
  }

  // Held outside render(): must survive the innerHTML replacement every
  // snapshot frame triggers (see the T260 header comment above).
  let lastSnapshot: MonitorSnapshot | null = null;
  const exitControlState: ExitControlState = {
    pending: false,
    optimisticActiveExit: null,
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

    let sectionsHtml: string;
    const grouped = snapshot.multiPeer && snapshot.peerNames.length > 1;
    if (grouped) {
      const pathsByPeer = groupByPeer(snapshot.paths);
      const fecByPeer = groupByPeer(snapshot.fec);
      const reseqByPeer = groupByPeer(snapshot.reseq);
      const aggByPeer = groupByPeer(snapshot.aggregation);
      const endpointsByPeer = groupByPeer(snapshot.endpoints);
      const peerSessionsByPeer = groupByPeer(snapshot.peerSessions);
      sectionsHtml = snapshot.peerNames
        .map((peer) =>
          renderSection(
            peer,
            pathsByPeer.get(peer) ?? [],
            fecByPeer.get(peer) ?? [],
            reseqByPeer.get(peer) ?? [],
            aggByPeer.get(peer) ?? [],
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
        snapshot.fec,
        snapshot.reseq,
        snapshot.aggregation,
        snapshot.addressingHidden,
      );
    }

    // Exit-switch control (T260): hidden entirely on an addressing-hidden
    // (remote/token'd) monitor or a single-peer snapshot — mirrors the
    // server's loopback-only 403 gate rather than relying on it, and there is
    // no alternate exit to switch to with only one peer bound.
    const exitControlHtml =
      !snapshot.addressingHidden && grouped ? renderExitControl(exitCapablePeers(snapshot), effectiveActiveExit, exitControlState) : '';

    root.innerHTML = `
      ${renderDaemonHeader(snapshot.daemon)}
      ${renderWgKeyLine(snapshot.wgPublicKeyFingerprint)}
      ${exitControlHtml}
      ${sectionsHtml}
      ${grouped ? '' : renderEndpointsSection(snapshot.endpoints, snapshot.addressingHidden)}
      <div class="stat-group" data-kind="session" data-testid="stat-group-session">
        <h4>WG Session</h4>
        ${renderSessionCard(snapshot.session)}
      </div>`;

    // innerHTML wipes all listeners on every render — re-attach after each one.
    const selectEl = root.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]');
    if (selectEl) {
      selectEl.addEventListener('change', () => handleExitChange(selectEl.value));
    }
  }

  function onSnapshot(snapshot: MonitorSnapshot): void {
    lastSnapshot = snapshot;
    exitControlState.optimisticActiveExit = null;
    render();
  }

  return { onSnapshot };
}
