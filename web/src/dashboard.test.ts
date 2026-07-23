import { beforeEach, describe, expect, it } from 'vitest';
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
  };
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
});
