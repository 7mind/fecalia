import type { MonitorSnapshot } from './types';

// Minimal placeholder proving the build+serve loop: open a same-origin
// WebSocket to /ws, parse each frame as a MonitorSnapshot, and log it.
// The resilient reconnecting client (T166) and the real dashboard (T168)
// replace this.
const wsURL = new URL('/ws', window.location.href);
wsURL.protocol = wsURL.protocol === 'https:' ? 'wss:' : 'ws:';

const socket = new WebSocket(wsURL);

socket.addEventListener('message', (event: MessageEvent<string>) => {
  const snapshot: MonitorSnapshot = JSON.parse(event.data);
  console.log(snapshot);
});
