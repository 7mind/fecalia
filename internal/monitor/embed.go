package monitor

import (
	"embed"
	"io/fs"
)

// distFS embeds the Vite-built frontend bundle (internal/monitor/dist) served at
// / by the monitor server. The real assets are produced by `just web-build`
// (Vite) before `just build`/`release`; a committed dist/.gitkeep keeps this
// embed non-empty so it compiles even before a build has run (in that state the
// bundle has no index.html, so GET / 404s until the frontend is built). `all:`
// includes dotfiles so the .gitkeep placeholder is embedded.
//
//go:embed all:dist
var distFS embed.FS

// staticAssets returns the dist subtree as an fs.FS rooted at the bundle, ready
// to hand to http.FileServer.
func staticAssets() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
