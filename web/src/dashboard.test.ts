import { beforeEach, describe, expect, it } from 'vitest';
import type { AggregationSnapshot, DaemonSnapshot, FECSnapshot, MonitorSnapshot, PathSnapshot, ReseqSnapshot } from './types';
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
});
