import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MonitorSnapshot } from './types';
import { ResilientWsClient, type ConnectionHealth, type HealthDetail } from './ws-client';

// Minimal stub WebSocket standing in for a real server: real network / real
// browser WebSocket are out of scope for a unit test, so this fake gives the
// test FULL, deterministic control over open/message/close/error timing —
// exactly what "server drop" / "server restart" need to be reproducible.
class StubSocket extends EventTarget {
  static instances: StubSocket[] = [];
  readyState = 0; // CONNECTING

  constructor(public readonly url: string) {
    super();
    StubSocket.instances.push(this);
  }

  serverOpen(): void {
    this.readyState = 1; // OPEN
  }

  serverSend(snapshot: MonitorSnapshot): void {
    this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(snapshot) }));
  }

  /** Simulates the server (or network) dropping the connection abnormally — no close frame. */
  serverDropAbnormally(): void {
    this.readyState = 3; // CLOSED
    this.dispatchEvent(new CloseEvent('close', { code: 1006, wasClean: false }));
  }

  /** Simulates the client's own close() call being acknowledged. */
  close(code = 1000, _reason?: string): void {
    this.readyState = 3;
    this.dispatchEvent(new CloseEvent('close', { code, wasClean: code === 1000 }));
  }
}

function makeSnapshot(): MonitorSnapshot {
  return {
    paths: [],
    fec: [],
    reseq: [],
    aggregation: [],
    session: { established: true, lastHandshakeSeconds: 1 },
    peerNames: [],
    multiPeer: false,
    daemon: { role: 'edge', version: 'test', uptimeSeconds: 1 },
    endpoints: [],
    wgPublicKeyFingerprint: '',
    addressingHidden: true,
  };
}

describe('ResilientWsClient', () => {
  let now: number;
  const healthLog: Array<{ health: ConnectionHealth; detail: HealthDetail }> = [];
  let client: ResilientWsClient;

  beforeEach(() => {
    vi.useFakeTimers();
    now = 1_000_000;
    healthLog.length = 0;
    StubSocket.instances = [];
    client = new ResilientWsClient({
      url: 'ws://stub/ws',
      createSocket: (url) => new StubSocket(url) as unknown as WebSocket,
      now: () => now,
      onSnapshot: () => {},
      onHealthChange: (health, detail) => healthLog.push({ health, detail }),
    });
  });

  afterEach(() => {
    client.stop();
    vi.useRealTimers();
  });

  function currentSocket(): StubSocket {
    return StubSocket.instances[StubSocket.instances.length - 1];
  }

  it('starts in connecting, then transitions to live on the first frame', () => {
    client.start();
    expect(client.getHealth()).toBe('connecting');

    currentSocket().serverOpen();
    currentSocket().serverSend(makeSnapshot());

    expect(client.getHealth()).toBe('live');
    expect(client.getLastFrameAt()).toBe(now);
  });

  it('on an abnormal drop, moves to offline (backoff wait) then reconnecting, and resumes live data on restart', () => {
    client.start();
    currentSocket().serverOpen();
    currentSocket().serverSend(makeSnapshot());
    expect(client.getHealth()).toBe('live');

    // --- server drop ---
    currentSocket().serverDropAbnormally();
    // Within one interval the health indicator must have left "live".
    expect(['offline', 'reconnecting']).toContain(client.getHealth());
    expect(client.getHealth()).toBe('offline');
    const detail = healthLog[healthLog.length - 1].detail;
    expect(detail.lastCloseWasClean).toBe(false);
    expect(detail.nextRetryDelayMs).not.toBeNull();
    expect(detail.nextRetryDelayMs!).toBeLessThanOrEqual(1000); // base delay cap on attempt 0

    // Staleness readout climbs: lastFrameAt is unchanged while time passes.
    const frameAtDrop = client.getLastFrameAt();
    now += 5000;
    expect(client.getLastFrameAt()).toBe(frameAtDrop);

    // Backoff timer fires -> reconnecting -> new socket opens.
    vi.advanceTimersByTime(2000);
    expect(client.getHealth()).toBe('reconnecting');

    // --- server restart: new connection resumes data ---
    currentSocket().serverOpen();
    currentSocket().serverSend(makeSnapshot());
    expect(client.getHealth()).toBe('live');
    expect(client.getLastFrameAt()).toBe(now);
  });

  it('backoff delay is bounded by maxDelayMs across repeated drops (no tight loop)', () => {
    const boundedClient = new ResilientWsClient({
      url: 'ws://stub/ws',
      createSocket: (url) => new StubSocket(url) as unknown as WebSocket,
      now: () => now,
      baseDelayMs: 1000,
      maxDelayMs: 4000,
      onSnapshot: () => {},
      onHealthChange: (health, detail) => healthLog.push({ health, detail }),
    });
    boundedClient.start();

    const observedDelays: number[] = [];
    for (let i = 0; i < 6; i++) {
      // Each attempted socket fails to even open before dropping.
      const sock = StubSocket.instances[StubSocket.instances.length - 1];
      sock.serverDropAbnormally();
      const d = healthLog[healthLog.length - 1].detail.nextRetryDelayMs;
      expect(d).not.toBeNull();
      observedDelays.push(d!);
      vi.advanceTimersByTime(d! + 1); // let the retry timer fire, opening the next socket
    }

    for (const d of observedDelays) {
      expect(d).toBeLessThanOrEqual(4000); // never exceeds the configured cap
      expect(d).toBeGreaterThan(0); // never a zero/tight-loop delay
    }
    // Confirms genuine exponential growth before the cap takes over.
    expect(observedDelays[0]).toBeLessThanOrEqual(1000);
    expect(Math.max(...observedDelays)).toBeGreaterThan(observedDelays[0]);

    boundedClient.stop();
  });

  it('stop() cancels pending reconnects permanently', () => {
    client.start();
    currentSocket().serverOpen();
    currentSocket().serverSend(makeSnapshot());
    currentSocket().serverDropAbnormally();
    expect(client.getHealth()).toBe('offline');

    client.stop();
    const socketCountAtStop = StubSocket.instances.length;
    vi.advanceTimersByTime(60_000);
    // No new socket was opened after stop(), even though a reconnect was pending.
    expect(StubSocket.instances.length).toBe(socketCountAtStop);
  });

  it('distinguishes a clean server close (1000) from an abnormal drop', () => {
    client.start();
    currentSocket().serverOpen();
    currentSocket().serverSend(makeSnapshot());

    currentSocket().dispatchEvent(new CloseEvent('close', { code: 1000, wasClean: true }));
    const detail = healthLog[healthLog.length - 1].detail;
    expect(detail.lastCloseWasClean).toBe(true);
  });
});
