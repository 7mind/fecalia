import type { MonitorSnapshot } from './types';
import { ResilientWsClient } from './ws-client';
import { mountHealthIndicator } from './health-indicator';
import { mountDashboard } from './dashboard';

// Wires the resilient /ws client (T166) to the health indicator (T166) and
// the read-only stat dashboard (T168), sharing the one #app mount point.
const app = document.getElementById('app');
if (app === null) {
  throw new Error('main.ts: #app element missing from index.html');
}

const indicator = mountHealthIndicator(app);
const dashboard = mountDashboard(app);

const client = new ResilientWsClient({
  onSnapshot: (snapshot: MonitorSnapshot) => {
    dashboard.onSnapshot(snapshot);
  },
  onHealthChange: indicator.onHealthChange,
});

client.start();
