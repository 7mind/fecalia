import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type {
  AggregationSnapshot,
  DaemonSnapshot,
  EndpointSnapshot,
  FECSnapshot,
  MonitorSnapshot,
  PathSnapshot,
  PeerSessionSnapshot,
  ReseqSnapshot,
} from './types';
import { mountDashboard } from './dashboard';
import { SPARKLINE_MAX_POINTS } from './sparkline';

function path(overrides: Partial<PathSnapshot> = {}): PathSnapshot {
  return {
    name: 'wan0',
    peer: '',
    txBytes: 1000,
    rxBytes: 2000,
    throughputBps: 5000,
    rttSeconds: 0.02,
    jitterSeconds: 0.001,
    loss: 0.01,
    up: true,
    bindMode: 'auto',
    boundDevice: '',
    linkBandwidthBps: 0,
    linkRttSeconds: 0,
    ...overrides,
  };
}

function daemon(overrides: Partial<DaemonSnapshot> = {}): DaemonSnapshot {
  return {
    role: 'edge',
    version: 'test',
    uptimeSeconds: 1,
    ...overrides,
  };
}

function fec(overrides: Partial<FECSnapshot> = {}): FECSnapshot {
  return {
    peer: '',
    dataPackets: 100,
    repairPackets: 10,
    recoveredPackets: 2,
    unrecoverablePackets: 0,
    dataBytes: 10000,
    repairBytes: 1000,
    residualLossRatio: 0.001,
    ...overrides,
  };
}

function reseq(overrides: Partial<ReseqSnapshot> = {}): ReseqSnapshot {
  return {
    peer: '',
    released: 100,
    droppedDup: 1,
    droppedOld: 0,
    droppedSuspect: 0,
    skipped: 0,
    resyncs: 0,
    rebaselines: 0,
    holds: 0,
    holdNanos: 0,
    immediateReleases: 0,
    ...overrides,
  };
}

function aggregation(overrides: Partial<AggregationSnapshot> = {}): AggregationSnapshot {
  return {
    peer: '',
    aggregating: true,
    offeredLoadFps: 10,
    engageThresholdFps: 5,
    disengageThresholdFps: 2,
    ...overrides,
  };
}

function endpoint(overrides: Partial<EndpointSnapshot> = {}): EndpointSnapshot {
  return {
    peer: '',
    address: '',
    active: false,
    ...overrides,
  };
}

function peerSession(overrides: Partial<PeerSessionSnapshot> = {}): PeerSessionSnapshot {
  return {
    peer: '',
    established: true,
    lastHandshakeSeconds: 5,
    ...overrides,
  };
}

function multiPeerSnapshot(): MonitorSnapshot {
  return {
    paths: [path({ name: 'wan0', peer: 'peerA' }), path({ name: 'wan1', peer: 'peerB' })],
    fec: [fec({ peer: 'peerA' }), fec({ peer: 'peerB' })],
    reseq: [reseq({ peer: 'peerA' }), reseq({ peer: 'peerB' })],
    aggregation: [aggregation({ peer: 'peerA' }), aggregation({ peer: 'peerB' })],
    session: { established: true, lastHandshakeSeconds: 3 },
    peerNames: ['peerA', 'peerB'],
    multiPeer: true,
    daemon: daemon(),
    endpoints: [],
    peerSessions: [],
    activeExit: '',
    wgPublicKeyFingerprint: '',
    addressingHidden: true,
    exitControlAvailable: false,
  };
}

function singlePeerSnapshot(): MonitorSnapshot {
  return {
    paths: [path({ name: 'wan0', peer: '' })],
    fec: [fec({ peer: '' })],
    reseq: [reseq({ peer: '' })],
    aggregation: [],
    session: { established: true, lastHandshakeSeconds: 1 },
    peerNames: [],
    multiPeer: false,
    daemon: daemon(),
    endpoints: [],
    peerSessions: [],
    activeExit: '',
    wgPublicKeyFingerprint: '',
    addressingHidden: true,
    exitControlAvailable: false,
  };
}

/**
 * T259/G28/M107: a 2-peer concentrator stream carrying per-peer endpoints,
 * peerSessions, and activeExit — the shape the T259 per-concentrator
 * grouping renders.
 */
function twoPeerConcentratorSnapshot(): MonitorSnapshot {
  return {
    paths: [path({ name: 'wan0', peer: 'peerA' }), path({ name: 'wan1', peer: 'peerB' })],
    fec: [fec({ peer: 'peerA' }), fec({ peer: 'peerB' })],
    reseq: [reseq({ peer: 'peerA' }), reseq({ peer: 'peerB' })],
    aggregation: [aggregation({ peer: 'peerA' }), aggregation({ peer: 'peerB' })],
    session: { established: true, lastHandshakeSeconds: 3 },
    peerNames: ['peerA', 'peerB'],
    multiPeer: true,
    daemon: daemon({ role: 'concentrator' }),
    endpoints: [
      endpoint({ peer: 'peerA', address: 'hub-a1:51820', active: true }),
      endpoint({ peer: 'peerA', address: 'hub-a2:51820', active: false }),
      endpoint({ peer: 'peerB', address: 'hub-b1:51820', active: true }),
    ],
    peerSessions: [peerSession({ peer: 'peerA', established: true, lastHandshakeSeconds: 7 }), peerSession({ peer: 'peerB', established: false, lastHandshakeSeconds: 0 })],
    activeExit: 'peerB',
    wgPublicKeyFingerprint: 'ConcFp01',
    addressingHidden: false,
    exitControlAvailable: true,
  };
}

/**
 * T260 round 2: isolates the single-peer exit-control hide from the
 * exitControlAvailable gate (re-keyed T280/G32; was addressingHidden). Unlike
 * singlePeerSnapshot() (exitControlAvailable:false, peerNames:[]), this
 * fixture sets exitControlAvailable:true and gives exitCapablePeers(...) a
 * non-empty result (a named endpoint + peerSession for 'peerA'), so the ONLY
 * thing that can suppress the control is the `grouped` (peerNames.length > 1)
 * gate itself.
 */
function singlePeerNamedSnapshot(): MonitorSnapshot {
  return {
    paths: [path({ name: 'wan0', peer: 'peerA' })],
    fec: [fec({ peer: 'peerA' })],
    reseq: [reseq({ peer: 'peerA' })],
    aggregation: [aggregation({ peer: 'peerA' })],
    session: { established: true, lastHandshakeSeconds: 3 },
    peerNames: ['peerA'],
    multiPeer: false,
    daemon: daemon({ role: 'concentrator' }),
    endpoints: [endpoint({ peer: 'peerA', address: 'hub-a1:51820', active: true })],
    peerSessions: [peerSession({ peer: 'peerA', established: true, lastHandshakeSeconds: 7 })],
    activeExit: 'peerA',
    wgPublicKeyFingerprint: 'ConcFp01',
    addressingHidden: false,
    exitControlAvailable: true,
  };
}

/** Flushes the microtask queue enough times for a fetch().then(async ...).finally() chain to settle. */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

/** Builds a minimal fetch Response stub carrying a JSON body. */
function jsonResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as Response;
}

describe('mountDashboard', () => {
  let container: HTMLElement;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
  });

  it('renders one per-peer section per peer with all named stat groups, for a multi-peer stream', () => {
    const dashboard = mountDashboard(container);
    dashboard.onSnapshot(multiPeerSnapshot());

    const sections = container.querySelectorAll('[data-testid="peer-section"]');
    expect(sections.length).toBe(2);
    expect(container.querySelectorAll('[data-testid="flat-section"]').length).toBe(0);

    const peerLabels = Array.from(container.querySelectorAll('[data-testid="peer-label"]')).map((el) => el.textContent);
    expect(peerLabels).toEqual(['peerA', 'peerB']);

    for (const section of Array.from(sections)) {
      expect(section.querySelector('[data-testid="stat-group-paths"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="stat-group-fec"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="stat-group-reseq"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="stat-group-aggregation"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="path-card"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="fec-card"]')).not.toBeNull();
      expect(section.querySelector('[data-testid="reseq-card"]')).not.toBeNull();
    }

    // Session is connection-scoped (not per-peer): rendered once, outside the sections.
    expect(container.querySelectorAll('[data-testid="session-card"]').length).toBe(1);
  });

  it('renders one flat section with no empty peer label, for a single-peer stream', () => {
    const dashboard = mountDashboard(container);
    dashboard.onSnapshot(singlePeerSnapshot());

    const flatSections = container.querySelectorAll('[data-testid="flat-section"]');
    expect(flatSections.length).toBe(1);
    expect(container.querySelectorAll('[data-testid="peer-section"]').length).toBe(0);
    // No peer-label element at all (not even an empty one) in flat mode.
    expect(container.querySelectorAll('[data-testid="peer-label"]').length).toBe(0);

    expect(flatSections[0].querySelector('[data-testid="stat-group-paths"]')).not.toBeNull();
    expect(flatSections[0].querySelector('[data-testid="stat-group-fec"]')).not.toBeNull();
    expect(flatSections[0].querySelector('[data-testid="stat-group-reseq"]')).not.toBeNull();
    // No aggregation entries in this snapshot -> group omitted entirely.
    expect(flatSections[0].querySelector('[data-testid="stat-group-aggregation"]')).toBeNull();
  });

  it('accumulates sparkline samples across frames and caps the buffer at SPARKLINE_MAX_POINTS', () => {
    const dashboard = mountDashboard(container);

    for (let i = 0; i < SPARKLINE_MAX_POINTS + 50; i++) {
      dashboard.onSnapshot(singlePeerSnapshot());
    }

    const pathCard = container.querySelector('[data-testid="path-card"]');
    expect(pathCard).not.toBeNull();
    const polylines = pathCard!.querySelectorAll('svg.sparkline polyline');
    expect(polylines.length).toBeGreaterThan(0);
    for (const polyline of Array.from(polylines)) {
      const pointsAttr = polyline.getAttribute('points') ?? '';
      const pointCount = pointsAttr.trim().length === 0 ? 0 : pointsAttr.trim().split(/\s+/).length;
      expect(pointCount).toBe(SPARKLINE_MAX_POINTS);
    }
  });

  it('re-renders on each snapshot without leaking duplicate top-level sections', () => {
    const dashboard = mountDashboard(container);
    dashboard.onSnapshot(singlePeerSnapshot());
    dashboard.onSnapshot(singlePeerSnapshot());
    dashboard.onSnapshot(singlePeerSnapshot());

    expect(container.querySelectorAll('[data-testid="flat-section"]').length).toBe(1);
  });

  it('renders the daemon header, bind/link path columns, populated addressing, and an ordered endpoint list on a full edge snapshot', () => {
    const dashboard = mountDashboard(container);
    const snapshot: MonitorSnapshot = {
      paths: [
        path({
          name: 'wan0',
          peer: '',
          bindMode: 'device',
          boundDevice: 'eth0',
          linkBandwidthBps: 1048576,
          linkRttSeconds: 0.025,
          addressing: { source: '10.0.0.5', remote: '203.0.113.9:51820' },
        }),
      ],
      fec: [fec({ peer: '' })],
      reseq: [reseq({ peer: '' })],
      aggregation: [],
      session: { established: true, lastHandshakeSeconds: 1 },
      peerNames: [],
      multiPeer: false,
      daemon: daemon({ role: 'edge', version: '1.2.3', uptimeSeconds: 90061 }),
      endpoints: [endpoint({ address: '198.51.100.1:51820', active: true }), endpoint({ address: '198.51.100.2:51820', active: false })],
      peerSessions: [],
      activeExit: '',
      wgPublicKeyFingerprint: 'AbCd1234',
      addressingHidden: false,
      exitControlAvailable: true,
    };
    dashboard.onSnapshot(snapshot);

    const header = container.querySelector('[data-testid="daemon-header"]');
    expect(header).not.toBeNull();
    expect(container.querySelector('[data-testid="role-badge"]')!.textContent).toBe('edge');
    expect(container.querySelector('[data-testid="daemon-version"]')!.textContent).toContain('1.2.3');
    expect(container.querySelector('[data-testid="daemon-uptime"]')!.textContent).toContain('1d 1h 1m 1s');

    expect(container.querySelector('[data-testid="path-bind"]')!.textContent).toContain('device');
    expect(container.querySelector('[data-testid="path-bind"]')!.textContent).toContain('eth0');
    expect(container.querySelector('[data-testid="path-link"]')!.textContent).toContain('1.0MB/s');
    expect(container.querySelector('[data-testid="path-link"]')!.textContent).toContain('25.0ms');

    const addressing = container.querySelector('[data-testid="addressing"]');
    expect(addressing).not.toBeNull();
    expect(container.textContent).toContain('10.0.0.5');
    expect(container.textContent).toContain('203.0.113.9:51820');
    expect(container.querySelector('[data-testid="addressing-hidden"]')).toBeNull();

    const endpointRows = container.querySelectorAll('[data-testid="endpoint-row"]');
    expect(endpointRows.length).toBe(2);
    expect(container.textContent).toContain('198.51.100.1:51820');
    expect(container.textContent).toContain('198.51.100.2:51820');

    expect(container.querySelector('[data-testid="wg-key-line"]')!.textContent).toContain('AbCd1234');
  });

  it('shows the addressing-hidden placeholder and never renders source/remote address text on a redacted snapshot, while still showing the fingerprint', () => {
    const dashboard = mountDashboard(container);
    const snapshot = singlePeerSnapshot();
    snapshot.wgPublicKeyFingerprint = 'RedactedFp99';
    dashboard.onSnapshot(snapshot);

    const placeholder = container.querySelector('[data-testid="addressing-hidden"]');
    expect(placeholder).not.toBeNull();
    expect(placeholder!.textContent).toContain('addressing hidden on non-loopback binding');
    expect(container.querySelector('[data-testid="addressing"]')).toBeNull();

    expect(container.querySelector('[data-testid="wg-key-line"]')!.textContent).toContain('RedactedFp99');
  });

  it('marks the active endpoint among an ordered endpoint list on an edge snapshot', () => {
    const dashboard = mountDashboard(container);
    const snapshot = singlePeerSnapshot();
    snapshot.endpoints = [endpoint({ address: 'hub-a:51820', active: false }), endpoint({ address: 'hub-b:51820', active: true })];
    dashboard.onSnapshot(snapshot);

    const rows = container.querySelectorAll('[data-testid="endpoint-row"]');
    expect(rows.length).toBe(2);
    expect(rows[0].getAttribute('data-active')).toBe('false');
    expect(rows[0].textContent).toContain('standby');
    expect(rows[1].getAttribute('data-active')).toBe('true');
    expect(rows[1].textContent).toContain('ACTIVE');
    expect(rows[1].textContent).toContain('hub-b:51820');
  });

  it('omits the endpoint section entirely when the endpoint list is empty, on a concentrator snapshot', () => {
    const dashboard = mountDashboard(container);
    const snapshot = multiPeerSnapshot();
    snapshot.daemon = daemon({ role: 'concentrator' });
    dashboard.onSnapshot(snapshot);

    expect(snapshot.endpoints).toEqual([]);
    expect(container.querySelector('[data-testid="stat-group-endpoints"]')).toBeNull();
    expect(container.querySelector('[data-testid="endpoint-row"]')).toBeNull();
    expect(container.querySelector('[data-testid="role-badge"]')!.textContent).toBe('concentrator');
  });

  it('T259/G28/M107: groups endpoints and per-peer session state under each peer section, badging the activeExit peer and omitting the top-level endpoints section', () => {
    const dashboard = mountDashboard(container);
    dashboard.onSnapshot(twoPeerConcentratorSnapshot());

    const sections = container.querySelectorAll('[data-testid="peer-section"]');
    expect(sections.length).toBe(2);
    const [sectionA, sectionB] = Array.from(sections);

    // Per-peer endpoints group, in configured order, nested inside that
    // peer's own section — not duplicated at the top level.
    expect(container.querySelectorAll('[data-testid="stat-group-endpoints"]').length).toBe(2);
    const rowsA = sectionA.querySelectorAll('[data-testid="endpoint-row"]');
    expect(rowsA.length).toBe(2);
    expect(rowsA[0].textContent).toContain('hub-a1:51820');
    expect(rowsA[1].textContent).toContain('hub-a2:51820');
    const rowsB = sectionB.querySelectorAll('[data-testid="endpoint-row"]');
    expect(rowsB.length).toBe(1);
    expect(rowsB[0].textContent).toContain('hub-b1:51820');

    // Per-peer session state (distinct from the connection-scoped session
    // rendered once outside the sections) with each peer's own handshake age.
    expect(sectionA.querySelector('[data-testid="peer-session-card"]')!.textContent).toContain('ESTABLISHED');
    expect(sectionA.querySelector('[data-testid="peer-session-card"]')!.textContent).toContain('7s ago');
    expect(sectionB.querySelector('[data-testid="peer-session-card"]')!.textContent).toContain('NOT ESTABLISHED');
    expect(sectionB.querySelector('[data-testid="peer-session-card"]')!.textContent).toContain('never');

    // ACTIVE-EXIT badge on peerB only (snapshot.activeExit === 'peerB').
    expect(sectionA.querySelector('[data-testid="active-exit-badge"]')).toBeNull();
    expect(sectionB.querySelector('[data-testid="active-exit-badge"]')).not.toBeNull();
    expect(sectionB.querySelector('[data-testid="peer-label"]')!.textContent).toContain('ACTIVE-EXIT');

    // The flat top-level endpoints section (pre-T259 shape) is suppressed in
    // grouped mode — every endpoint lives inside its own peer section.
    const allEndpointRows = container.querySelectorAll('[data-testid="endpoint-row"]');
    expect(allEndpointRows.length).toBe(3);
  });

  it('T259: renders the blanked-address redaction placeholder for a grouped (per-peer) endpoint on a non-loopback binding', () => {
    const dashboard = mountDashboard(container);
    const snapshot = twoPeerConcentratorSnapshot();
    snapshot.addressingHidden = true;
    snapshot.endpoints = [
      endpoint({ peer: 'peerA', address: '', active: true }),
      endpoint({ peer: 'peerB', address: '', active: true }),
    ];
    dashboard.onSnapshot(snapshot);

    const placeholders = container.querySelectorAll('[data-testid="endpoint-address-hidden"]');
    expect(placeholders.length).toBe(2);
    for (const placeholder of Array.from(placeholders)) {
      expect(placeholder.textContent).toContain('hidden on non-loopback binding');
    }
    expect(container.textContent).not.toContain('hub-a1:51820');
  });

  it('T259: a single-peer stream renders no per-peer grouping artifacts (no peer-session card, no active-exit badge)', () => {
    const dashboard = mountDashboard(container);
    const snapshot = singlePeerSnapshot();
    snapshot.endpoints = [endpoint({ address: 'hub-a:51820', active: true })];
    snapshot.peerSessions = [peerSession({ established: true, lastHandshakeSeconds: 9 })];
    snapshot.activeExit = '';
    dashboard.onSnapshot(snapshot);

    // Flat-mode output is exactly as before T259: one top-level endpoints
    // section, no per-peer session card, no active-exit badge.
    expect(container.querySelectorAll('[data-testid="stat-group-endpoints"]').length).toBe(1);
    expect(container.querySelector('[data-testid="peer-session-card"]')).toBeNull();
    expect(container.querySelector('[data-testid="active-exit-badge"]')).toBeNull();
    expect(container.querySelectorAll('[data-testid="endpoint-row"]').length).toBe(1);
  });

  describe('T260/G28/M107: exit-switch control', () => {
    afterEach(() => {
      vi.unstubAllGlobals();
    });

    // T280/G32: exit-widget visibility is now keyed on exitControlAvailable,
    // NOT addressingHidden — flipping addressingHidden alone must not affect
    // it, since twoPeerConcentratorSnapshot() has exitControlAvailable:true.
    it('renders for an exitControlAvailable fixture even when addressingHidden is (independently) true', () => {
      const dashboard = mountDashboard(container);
      const snapshot = twoPeerConcentratorSnapshot();
      snapshot.addressingHidden = true;
      dashboard.onSnapshot(snapshot);

      expect(container.querySelector('[data-testid="exit-control"]')).not.toBeNull();
    });

    // T280/G32: the remote-reveal case — a reveal_addressing opt-in can unhide
    // addressing on a non-loopback bind (addressingHidden:false) WITHOUT
    // enabling the mutating POST /api/exit control server-side
    // (exitControlAvailable:false), so the widget must stay hidden. This is
    // the case the old `!snapshot.addressingHidden && grouped` gate got wrong.
    // MUTATION-VERIFY: temporarily reverting the dashboard.ts gate back to
    // `!snapshot.addressingHidden && grouped` makes this exact assertion fail
    // (the control renders, since addressingHidden is false here) — confirmed
    // by hand before reverting; see task T281 summary.
    it('does not render for exitControlAvailable=false, even when addressingHidden=false (the remote-reveal case)', () => {
      const dashboard = mountDashboard(container);
      const snapshot = twoPeerConcentratorSnapshot();
      snapshot.exitControlAvailable = false;
      dashboard.onSnapshot(snapshot);

      expect(container.querySelector('[data-testid="exit-control"]')).toBeNull();
    });

    it('does not render for a single-peer fixture', () => {
      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(singlePeerSnapshot());

      expect(container.querySelector('[data-testid="exit-control"]')).toBeNull();
    });

    // T260 round 2: singlePeerSnapshot() above also has
    // exitControlAvailable:false and peerNames:[], so it passes even if the
    // `grouped` gate were dropped from the exitControlHtml condition — those
    // two other conditions mask it. This fixture isolates the `grouped`
    // (peerNames.length > 1) gate: exitControlAvailable is true and
    // exitCapablePeers(...) is non-empty (a named endpoint + peerSession), so
    // `grouped` is the only remaining reason the control can be absent.
    // Confirmed: reverting `grouped` out of
    // `snapshot.exitControlAvailable && grouped` in dashboard.ts's render()
    // makes this assertion fail (the control renders) while the above test
    // still passes vacuously.
    it('does not render for a single-peer fixture, even when exitControlAvailable is true and exit-capable peers exist', () => {
      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(singlePeerNamedSnapshot());

      expect(container.querySelector('[data-testid="exit-control"]')).toBeNull();
    });

    it('renders for a multi-peer, exitControlAvailable fixture, listing every exit-capable peer', () => {
      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const control = container.querySelector('[data-testid="exit-control"]');
      expect(control).not.toBeNull();
      const select = control!.querySelector('select')!;
      const values = Array.from(select.options).map((o) => o.value);
      expect(values).toEqual(['peerA', 'peerB']);
      expect(select.value).toBe('peerB'); // marks the current activeExit
    });

    it('selecting a standby concentrator issues POST /api/exit with the exact JSON body', async () => {
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { activeExit: 'peerA' }));
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flush();

      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe('/api/exit');
      expect(init.method).toBe('POST');
      expect(init.body).toBe(JSON.stringify({ peer: 'peerA' }));
    });

    it('a 200 response updates the badge (optimistically, ahead of the next snapshot)', async () => {
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { activeExit: 'peerA' }));
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flush();

      const sections = container.querySelectorAll('[data-testid="peer-section"]');
      const [sectionA, sectionB] = Array.from(sections);
      expect(sectionA.querySelector('[data-testid="active-exit-badge"]')).not.toBeNull();
      expect(sectionB.querySelector('[data-testid="active-exit-badge"]')).toBeNull();
      expect(container.querySelector('[data-testid="exit-control-error"]')).toBeNull();
    });

    it('a 403 response shows the error notice and leaves the badge unchanged', async () => {
      const fetchMock = vi.fn().mockResolvedValue(
        jsonResponse(403, { error: 'exit control is available only on a loopback-bound monitor' }),
      );
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flush();

      const errorNotice = container.querySelector('[data-testid="exit-control-error"]');
      expect(errorNotice).not.toBeNull();
      expect(errorNotice!.textContent).toContain('exit control is available only on a loopback-bound monitor');

      // Badge reverts to the ORIGINAL activeExit (peerB) — the optimistic
      // update never happened for a non-2xx response.
      const sections = container.querySelectorAll('[data-testid="peer-section"]');
      const [sectionA, sectionB] = Array.from(sections);
      expect(sectionA.querySelector('[data-testid="active-exit-badge"]')).toBeNull();
      expect(sectionB.querySelector('[data-testid="active-exit-badge"]')).not.toBeNull();
    });

    it('a network error also shows the error notice and leaves the badge unchanged', async () => {
      const fetchMock = vi.fn().mockRejectedValue(new TypeError('network error'));
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flush();

      expect(container.querySelector('[data-testid="exit-control-error"]')).not.toBeNull();
      const sections = container.querySelectorAll('[data-testid="peer-section"]');
      const [, sectionB] = Array.from(sections);
      expect(sectionB.querySelector('[data-testid="active-exit-badge"]')).not.toBeNull();
    });

    it('the next snapshot frame reconciles the optimistic update with the server truth', async () => {
      const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { activeExit: 'peerA' }));
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));
      await flush();

      // Next real frame still reports the old activeExit (server hasn't
      // caught up yet in this simulation) — it must win over the client's
      // optimistic guess.
      const stale = twoPeerConcentratorSnapshot();
      dashboard.onSnapshot(stale);

      const sections = container.querySelectorAll('[data-testid="peer-section"]');
      const [sectionA, sectionB] = Array.from(sections);
      expect(sectionA.querySelector('[data-testid="active-exit-badge"]')).toBeNull();
      expect(sectionB.querySelector('[data-testid="active-exit-badge"]')).not.toBeNull();
    });

    // T260 round 2: docs claim the select is "disabled while a switch is in
    // flight" but no prior test observed the mid-flight DOM — every fetch
    // mock above resolves/rejects immediately under `await flush()`, so the
    // pending render was never asserted. Uses an unresolved fetch promise to
    // freeze mid-flight and inspect the DOM before settling it. Confirmed:
    // dropping the `state.pending ? 'disabled' : ''` conditional in
    // renderExitControl (dashboard.ts) makes this assertion fail.
    it('disables the select and shows a pending note while a switch is in flight, then restores normal state on resolution', async () => {
      let resolveFetch!: (response: Response) => void;
      const fetchMock = vi.fn().mockReturnValue(
        new Promise<Response>((resolve) => {
          resolveFetch = resolve;
        }),
      );
      vi.stubGlobal('fetch', fetchMock);

      const dashboard = mountDashboard(container);
      dashboard.onSnapshot(twoPeerConcentratorSnapshot());

      const select = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      select.value = 'peerA';
      select.dispatchEvent(new Event('change', { bubbles: true }));

      // Mid-flight, before the fetch settles: select disabled, pending note shown.
      const pendingSelect = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      expect(pendingSelect.disabled).toBe(true);
      expect(container.querySelector('[data-testid="exit-control-pending"]')).not.toBeNull();

      resolveFetch(jsonResponse(200, { activeExit: 'peerA' }));
      await flush();

      const settledSelect = container.querySelector<HTMLSelectElement>('[data-testid="exit-control-select"]')!;
      expect(settledSelect.disabled).toBe(false);
      expect(container.querySelector('[data-testid="exit-control-pending"]')).toBeNull();
    });
  });
});
