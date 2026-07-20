import { beforeEach, describe, expect, it } from 'vitest';
import type {
  AggregationSnapshot,
  DaemonSnapshot,
  EndpointSnapshot,
  FECSnapshot,
  MonitorSnapshot,
  PathSnapshot,
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
    address: '',
    active: false,
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
    wgPublicKeyFingerprint: '',
    addressingHidden: true,
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
});
