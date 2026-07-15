import type { MonitorSnapshot } from './types';
import { ResilientWsClient } from './ws-client';
import { mountHealthIndicator } from './health-indicator';

// Wires the resilient /ws client (T166) to a visible health indicator.
// Data rendering stays minimal here on purpose — the full stat-card
// dashboard is T168; this file's job is proving the resilient-client +
// health-surfacing contract end to end.
const app = document.getElementById('app');
if (app === null) {
  throw new Error('main.ts: #app element missing from index.html');
}

const indicator = mountHealthIndicator(app);

const latest = document.createElement('pre');
latest.style.cssText = 'font-family:monospace; font-size:0.8em; white-space:pre-wrap;';
app.appendChild(latest);

const client = new ResilientWsClient({
  onSnapshot: (snapshot: MonitorSnapshot) => {
    latest.textContent = JSON.stringify(snapshot, null, 2);
  },
  onHealthChange: indicator.onHealthChange,
});

client.start();
