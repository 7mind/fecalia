import { defineConfig } from 'vite';

// Build config for the wanbond monitoring dashboard (W2).
//
// outDir points OUTSIDE web/ at internal/monitor/dist — that is the exact
// directory T167's `//go:embed dist` (in internal/monitor) will consume, so
// `go build` picks up a self-contained static bundle with no separate asset
// server. `base: './'` makes every emitted asset reference relative, since
// the bundle is served from an arbitrary path under the monitor HTTP
// endpoint rather than from a domain root.
//
// internal/monitor/dist is built by `npm run build` (or the future
// `just web-build`, wired in T167) and is gitignored — it is a build
// artifact, not source, and must be regenerated before `go build`/release.
export default defineConfig({
  base: './',
  build: {
    outDir: '../internal/monitor/dist',
    emptyOutDir: true,
  },
});
