import { defineConfig } from 'vitest/config';

// Standalone from vite.config.ts on purpose: that file is the go:embed build
// config (outDir outside web/); tests need no bundling output, only a DOM
// environment for ws-client.test.ts's stub WebSocket (CloseEvent/MessageEvent).
export default defineConfig({
  test: {
    environment: 'jsdom',
  },
});
