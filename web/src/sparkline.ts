// Client-side-only rolling sparkline buffers (T168, Q50): ~5 minutes of 1Hz
// samples held in browser memory. No server-side history exists — the
// buffer is empty again after a page reload, by design.

/** ~5 minutes of samples at the monitor endpoint's 1Hz push rate. */
export const SPARKLINE_MAX_POINTS = 300;

/**
 * Appends `value` to `buffer` in place, dropping the oldest sample once the
 * buffer exceeds `max` — a simple rolling window, no ring-buffer indexing
 * needed at this size (300 elements, one shift per second at most).
 */
export function pushSample(buffer: number[], value: number, max: number = SPARKLINE_MAX_POINTS): void {
  buffer.push(value);
  if (buffer.length > max) {
    buffer.shift();
  }
}

/** Maps `values` onto an SVG polyline's `points` attribute, autoscaled to [0, width] x [0, height]. */
export function sparklinePoints(values: number[], width: number, height: number): string {
  if (values.length === 0) {
    return '';
  }
  const min = Math.min(...values);
  const max = Math.max(...values);
  const range = max - min || 1; // avoid a divide-by-zero on a flat series
  const stepX = values.length > 1 ? width / (values.length - 1) : 0;
  return values
    .map((v, i) => `${(i * stepX).toFixed(1)},${(height - ((v - min) / range) * height).toFixed(1)}`)
    .join(' ');
}

/** Renders `values` as a minimal inline SVG sparkline — no charting library. */
export function renderSparklineSvg(values: number[], width = 96, height = 20): string {
  const points = sparklinePoints(values, width, height);
  return `<svg class="sparkline" width="${width}" height="${height}" viewBox="0 0 ${width} ${height}" role="img" aria-label="trend sparkline"><polyline points="${points}" fill="none" stroke="currentColor" stroke-width="1.5" /></svg>`;
}
