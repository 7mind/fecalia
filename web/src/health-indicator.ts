import type { ConnectionHealth, HealthDetail } from './ws-client';

// Visible connection-health surface (skill Part 2). Scoped down from the
// skill's full widget (no RTT/loss/event-log pool view — T168 owns the real
// dashboard chrome): a colored+labelled dot (V3: two channels, not color
// alone) plus a "last update Ns ago" staleness readout that keeps counting
// up independent of the health state (V4-adjacent: the reader can see
// staleness grow even while the client is still nominally "live").

const LABELS: Record<ConnectionHealth, string> = {
  connecting: 'Connecting…',
  live: 'Live',
  reconnecting: 'Reconnecting…',
  offline: 'Offline',
};

// Two-channel encoding (V3): color AND shape/motion (dot vs ring) AND text,
// so the indicator survives colorblindness and screenshots.
const DOT_STYLES: Record<ConnectionHealth, string> = {
  connecting: 'background:#999999;',
  live: 'background:#2e7d32;',
  reconnecting: 'background:#f9a825; animation: wanbond-pulse 1s ease-in-out infinite;',
  offline: 'background:#c62828;',
};

const STALENESS_TICK_MS = 1000;

/**
 * Renders (and keeps live) a compact health indicator + staleness readout
 * into `container`. Returns a handle with the two ws-client callbacks to
 * wire up, plus a `stop()` to cancel the staleness ticker.
 */
export function mountHealthIndicator(container: HTMLElement): {
  onHealthChange: (health: ConnectionHealth, detail: HealthDetail) => void;
  stop: () => void;
} {
  const style = document.createElement('style');
  style.textContent = `
    @keyframes wanbond-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
  `;
  document.head.appendChild(style);

  const wrapper = document.createElement('div');
  wrapper.style.cssText = 'display:flex; align-items:center; gap:0.5em; font-family:system-ui,sans-serif; font-size:0.9em;';

  const dot = document.createElement('span');
  dot.style.cssText = 'display:inline-block; width:0.75em; height:0.75em; border-radius:50%;';
  dot.setAttribute('aria-hidden', 'true');

  const label = document.createElement('span');
  label.setAttribute('role', 'status');
  label.setAttribute('aria-label', 'WebSocket connection health');

  const staleness = document.createElement('span');
  staleness.style.cssText = 'color:#666666;';

  wrapper.append(dot, label, staleness);
  container.appendChild(wrapper);

  let lastFrameAt: number | null = null;

  const renderStaleness = (): void => {
    if (lastFrameAt === null) {
      staleness.textContent = '(no data yet)';
      return;
    }
    const seconds = Math.max(0, Math.round((Date.now() - lastFrameAt) / 1000));
    staleness.textContent = `last update ${seconds}s ago`;
  };

  const tickId = setInterval(renderStaleness, STALENESS_TICK_MS);

  const onHealthChange = (health: ConnectionHealth, detail: HealthDetail): void => {
    dot.style.cssText = `display:inline-block; width:0.75em; height:0.75em; border-radius:50%; ${DOT_STYLES[health]}`;
    label.textContent = LABELS[health];
    lastFrameAt = detail.lastFrameAt;
    renderStaleness();
  };

  renderStaleness();

  return {
    onHealthChange,
    stop: () => clearInterval(tickId),
  };
}
