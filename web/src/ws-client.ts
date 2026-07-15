import type { MonitorSnapshot } from './types';

// Resilient /ws client (T166). Auth note: the token/cookie auth (T164) sets a
// SameSite=Strict cookie on the first `?token=` navigation; the browser
// attaches it automatically to same-origin requests, INCLUDING the WebSocket
// upgrade handshake. No token handling belongs in this module — the cookie
// jar does it for us.
//
// Applies the /resilient-ws-ui skill's patterns, scoped to what this
// push-only, no-application-heartbeat endpoint (internal/monitor/server.go's
// newWSHandler) actually needs:
//   - a 4-state health machine (connecting/live/reconnecting/offline)
//   - exponential backoff with full jitter and a bounded max delay
//   - clean-close (1000) vs abnormal-drop classification
//   - resumed data flow on reconnect, with the backoff counter reset the
//     moment a frame is actually received (not merely on socket "open" —
//     "open" only proves the handshake succeeded, not that data is flowing)
//
// Scope limitation (documented per the skill's "call out gaps" guidance):
// this endpoint sends no server->client protocol ping and this client sends
// none either, so a connection that goes silently half-open (no close event
// ever fires — see skill R1: Firefox's no-keepalive bug, a frozen mobile
// tab, a dead NAT mapping) will NOT be detected by this module; the
// `live`/staleness readout would keep showing the last real frame. The
// monitor endpoint is loopback-by-default and pushes every 1s, so a
// missing close event is unlikely in practice; a heartbeat/time-jump
// detector (skill R3/R8) is the fix if this ever needs hardening.

/** Connection-health state machine surfaced to the UI. */
export type ConnectionHealth = 'connecting' | 'live' | 'reconnecting' | 'offline';

/** WebSocket close code for a graceful, expected shutdown (RFC 6455 7.4.1). */
const NORMAL_CLOSURE = 1000;

const DEFAULT_BASE_DELAY_MS = 1000;
const DEFAULT_MAX_DELAY_MS = 30_000;

/** Extra detail alongside the health state, for the tooltip/log surface. */
export interface HealthDetail {
  /** Epoch ms of the last successfully parsed frame, or null before the first one. */
  lastFrameAt: number | null;
  /** Whether the most recent close carried NORMAL_CLOSURE (1000), or null pre-first-close. */
  lastCloseWasClean: boolean | null;
  /** Number of reconnect attempts made since the last time the client was live. */
  reconnectAttempt: number;
  /** Delay before the next reconnect attempt fires, while waiting (`offline`); null otherwise. */
  nextRetryDelayMs: number | null;
}

export interface ResilientWsClientOptions {
  onSnapshot: (snapshot: MonitorSnapshot) => void;
  onHealthChange: (health: ConnectionHealth, detail: HealthDetail) => void;
  /** Same-origin ws(s) URL. Defaults to `/ws` derived from window.location. */
  url?: string;
  /** Test seam: overrides `new WebSocket(url)`. */
  createSocket?: (url: string) => WebSocket;
  /** Test seam: overrides Date.now(). */
  now?: () => number;
  baseDelayMs?: number;
  maxDelayMs?: number;
}

/** Derives the same-origin `/ws` URL, deriving ws:// or wss:// from the page's own protocol. */
export function defaultWsUrl(location: Pick<Location, 'href'> = window.location): string {
  const u = new URL('/ws', location.href);
  u.protocol = u.protocol === 'https:' ? 'wss:' : 'ws:';
  return u.toString();
}

/**
 * Resilient WebSocket client for the /ws MonitorSnapshot push feed.
 * `start()` connects; `stop()` tears down permanently (no further reconnect).
 */
export class ResilientWsClient {
  private readonly url: string;
  private readonly createSocket: (url: string) => WebSocket;
  private readonly now: () => number;
  private readonly baseDelayMs: number;
  private readonly maxDelayMs: number;
  private readonly onSnapshot: (snapshot: MonitorSnapshot) => void;
  private readonly onHealthChange: (health: ConnectionHealth, detail: HealthDetail) => void;

  private socket: WebSocket | null = null;
  private health: ConnectionHealth = 'connecting';
  private lastFrameAt: number | null = null;
  private lastCloseWasClean: boolean | null = null;
  private reconnectAttempt = 0;
  private nextRetryDelayMs: number | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private destroyed = false;

  constructor(options: ResilientWsClientOptions) {
    this.url = options.url ?? defaultWsUrl();
    this.createSocket = options.createSocket ?? ((url) => new WebSocket(url));
    this.now = options.now ?? (() => Date.now());
    this.baseDelayMs = options.baseDelayMs ?? DEFAULT_BASE_DELAY_MS;
    this.maxDelayMs = options.maxDelayMs ?? DEFAULT_MAX_DELAY_MS;
    this.onSnapshot = options.onSnapshot;
    this.onHealthChange = options.onHealthChange;
  }

  /** Opens the initial connection. Throws if called after stop(). */
  start(): void {
    if (this.destroyed) {
      throw new Error('ResilientWsClient: cannot start a destroyed client');
    }
    this.emitHealth('connecting');
    this.connect();
  }

  /** Tears down permanently: closes any open socket, cancels pending reconnects. */
  stop(): void {
    this.destroyed = true;
    if (this.reconnectTimer !== null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.socket?.close(NORMAL_CLOSURE, 'client stop');
    this.socket = null;
  }

  getHealth(): ConnectionHealth {
    return this.health;
  }

  getLastFrameAt(): number | null {
    return this.lastFrameAt;
  }

  private connect(): void {
    if (this.destroyed) {
      return;
    }
    const socket = this.createSocket(this.url);
    this.socket = socket;
    socket.addEventListener('message', (event: Event) => {
      this.handleMessage((event as MessageEvent<string>).data);
    });
    socket.addEventListener('close', (event: Event) => {
      this.handleClose((event as CloseEvent).code);
    });
    // No separate handling needed: a WebSocket "error" is always followed by
    // a "close" event (browser WebSocket and every stub used in tests), so
    // all reconnect/backoff logic lives in handleClose.
  }

  private handleMessage(data: string): void {
    let snapshot: MonitorSnapshot;
    try {
      snapshot = JSON.parse(data) as MonitorSnapshot;
    } catch {
      return; // malformed frame: ignore, keep the connection open
    }
    this.reconnectAttempt = 0;
    this.lastFrameAt = this.now();
    this.emitHealth('live');
    this.onSnapshot(snapshot);
  }

  private handleClose(code: number): void {
    this.socket = null;
    if (this.destroyed) {
      return;
    }
    this.lastCloseWasClean = code === NORMAL_CLOSURE;
    this.scheduleReconnect();
  }

  private scheduleReconnect(): void {
    // Exponential backoff with full jitter (skill R5): the delay is drawn
    // uniformly from [0.5, 1.0] of the capped exponential value, so retries
    // spread out instead of forming a thundering herd, and the cap bounds
    // the worst case — this can never become a tight reconnect loop.
    const exponential = Math.min(this.baseDelayMs * 2 ** this.reconnectAttempt, this.maxDelayMs);
    const jittered = Math.round(exponential * (0.5 + Math.random() * 0.5));
    this.reconnectAttempt += 1;
    this.nextRetryDelayMs = jittered;
    this.emitHealth('offline');
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.nextRetryDelayMs = null;
      this.emitHealth('reconnecting');
      this.connect();
    }, jittered);
  }

  private emitHealth(health: ConnectionHealth): void {
    this.health = health;
    this.onHealthChange(health, {
      lastFrameAt: this.lastFrameAt,
      lastCloseWasClean: this.lastCloseWasClean,
      reconnectAttempt: this.reconnectAttempt,
      nextRetryDelayMs: this.nextRetryDelayMs,
    });
  }
}
