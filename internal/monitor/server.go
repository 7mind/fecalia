package monitor

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/netutil"
)

// rootPath is the placeholder document route; the real static assets land in
// T167 (a //go:embed of the built frontend). wsPath is the WebSocket upgrade
// route the frontend subscribes to for MonitorSnapshot frames.
const (
	rootPath = "/"
	wsPath   = "/ws"
)

// readHeaderTimeout bounds how long a client may take to send request headers,
// closing the Slowloris hole a bare http.Server leaves open (gosec G112). The
// endpoint is loopback-only by default, but the bound is cheap.
const readHeaderTimeout = 5 * time.Second

// ErrNonLoopbackBind is returned when the requested listen address is not
// loopback AND no token is set. The monitoring endpoint mirrors the config
// invariant (config.Monitor.validate / ErrMonitorNonLoopbackWithoutAuth) as
// defense-in-depth: an off-host bind is permitted only as an explicit
// authenticated opt-in (token != ""); an unauthenticated non-loopback bind is
// refused at construction WITHOUT opening a listener.
var ErrNonLoopbackBind = errors.New("monitor: non-loopback listen address requires a token")

// Server is the monitoring-UI HTTP endpoint for the [monitor] surface. It owns
// a private mux serving a placeholder document at / and a WebSocket upgrade at
// /ws that streams MonitorSnapshot frames built from a metrics.Source. It never
// touches the global http.DefaultServeMux.
type Server struct {
	ln     net.Listener
	srv    *http.Server
	log    log.Logger
	cancel context.CancelFunc // cancels the push-handler context on Close
}

// NewServer validates the bind, opens a TCP listener, and wires the monitoring
// routes over a private mux fed by src. The bind rule mirrors config validation
// as defense-in-depth: a loopback address is always allowed; a non-loopback
// address is allowed ONLY when token != "". A refused bind returns
// ErrNonLoopbackBind WITHOUT opening a listener. addr is a host:port string; a
// ":0"/"127.0.0.1:0" port yields an OS-assigned port readable via Addr after
// construction.
//
// The loopback case keeps metrics.Server's act-then-verify discipline: because
// net.Listen performs its OWN address resolution independent of the pre-check,
// the kernel-bound Addr is asserted loopback and the bind fails closed on any
// mismatch (TOCTOU defense). When a token is set, a verified non-loopback bind
// is permitted.
func NewServer(addr, token string, src metrics.Source, logger log.Logger) (*Server, error) {
	loopback, err := netutil.IsLoopbackHost(addr)
	if err != nil {
		return nil, fmt.Errorf("monitor: %w", err)
	}
	if !loopback && token == "" {
		return nil, fmt.Errorf("%w: got %q", ErrNonLoopbackBind, addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("monitor: listen on %q: %w", addr, err)
	}
	// Act-then-verify: net.Listen resolves the listen address itself, a second
	// lookup independent of IsLoopbackHost's. When no token authorizes an
	// off-host bind, assert the kernel actually bound a loopback interface and
	// fail closed otherwise, removing any dependence on resolver consistency.
	if token == "" {
		if err := verifyLoopbackBind(ln.Addr()); err != nil {
			_ = ln.Close()
			return nil, err
		}
	}

	// srvCtx is cancelled by Close so the /ws push handlers (T165) stop promptly
	// on shutdown — http.Server.Shutdown does NOT cancel a hijacked WebSocket
	// connection's context, so without this a push loop would outlive Close and
	// leak.
	srvCtx, cancel := context.WithCancel(context.Background())

	static, aerr := staticAssets()
	if aerr != nil {
		_ = ln.Close()
		cancel()
		return nil, fmt.Errorf("monitor: embedded assets: %w", aerr)
	}

	mux := http.NewServeMux()
	mux.Handle("GET "+rootPath, staticHandler(static))
	mux.HandleFunc("GET "+wsPath, newWSHandler(srvCtx, src, logger.Component("monitor")))

	// Wrap the mux with the auth layer (T164): unconditional Host/Origin
	// validation on EVERY route + the /ws upgrade, plus optional static-token
	// gating when a token is configured. The allowed-host set is derived from
	// the configured listen host plus the loopback aliases.
	auth := &authConfig{token: token, allowed: allowedHosts(addr)}

	return &Server{
		ln: ln,
		srv: &http.Server{
			Handler:           auth.middleware(mux),
			ReadHeaderTimeout: readHeaderTimeout,
		},
		log:    logger.Component("monitor"),
		cancel: cancel,
	}, nil
}

// Addr returns the actual bound listen address (with the resolved port).
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Start serves the endpoint in a background goroutine and returns immediately.
// A Serve error other than the expected http.ErrServerClosed (from Close) is
// logged; the listener is already bound, so Start itself cannot fail.
func (s *Server) Start() {
	s.log.Info("monitor endpoint listening", "addr", s.ln.Addr().String())
	go func() {
		if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("monitor endpoint serve error", "err", err.Error())
		}
	}()
}

// Close gracefully shuts the endpoint down, bounded by ctx. http.Server.Shutdown
// only closes listeners it took ownership of via Serve, so a Close on a Server
// that was never Start()ed would leak the bound socket s.ln (the port stays
// held, EADDRINUSE on re-listen). Close s.ln explicitly to release the port in
// both paths, tolerating net.ErrClosed for the Start->Close path where Serve
// already closed it. The Shutdown error takes precedence; a meaningful
// listener-close error surfaces only when Shutdown itself succeeded.
func (s *Server) Close(ctx context.Context) error {
	// Signal the /ws push handlers to stop BEFORE Shutdown waits on them:
	// Shutdown does not cancel a hijacked WebSocket connection, so a running
	// push loop must observe this cancellation to return.
	s.cancel()
	shutdownErr := s.srv.Shutdown(ctx)
	if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		if shutdownErr != nil {
			return shutdownErr
		}
		return err
	}
	return shutdownErr
}

// staticHandler serves the embedded frontend bundle (fsys) via http.FileServer,
// with cache headers tuned to Vite's content-hashing: hashed asset files are
// immutable and cached for a year; the unhashed index.html entrypoint is
// no-cache so a redeploy is picked up immediately. http.FileServer sets
// Content-Type from the file extension. Taking fsys as a parameter keeps the
// handler unit-testable with a synthetic fs.FS, independent of the real (build-
// time) embed.
func staticHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// newWSHandler returns the /ws upgrade handler: it accepts the WebSocket and
// PUSHES a fresh MonitorSnapshot — an immediate first frame, then one every
// monitorPushInterval (Q50) — until the client disconnects OR srvCtx is
// cancelled (server Close/shutdown). Each write is bounded by writeTimeout so a
// stuck client cannot wedge the push goroutine. The read side is drained by
// CloseRead so the peer's control frames (close/ping) are processed and a client
// disconnect is detected promptly; the endpoint is push-only, so no application
// message is expected.
//
// INVARIANT (grounding): src MUST be the monitor's OWN metrics.Source instance,
// NOT the one the Prometheus scraper uses — metricsSource.Paths() derives
// throughput from cross-call byte deltas under shared last-sample state, so two
// independent readers on one instance corrupt each other's rates. This handler
// only consumes whatever Source it was given; the dedicated-instance wiring is
// enforced at construction (T169 device wiring).
func newWSHandler(srvCtx context.Context, src metrics.Source, logger log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			logger.Error("monitor ws accept failed", "err", err.Error())
			return
		}
		// CloseNow backstops every exit path (immediate underlying-conn close,
		// no close frame, idempotent). The graceful teardown below sends a
		// StatusNormalClosure first on the shutdown path.
		defer func() { _ = c.CloseNow() }()

		// connCtx cancels when the client closes (CloseRead drains the peer's
		// control frames in the background — this is a push-only endpoint, so any
		// application message the client sends is discarded). loopCtx ALSO cancels
		// on server shutdown (srvCtx via AfterFunc), so even a write blocked on a
		// stalled client unblocks promptly on Close.
		connCtx := c.CloseRead(r.Context())
		loopCtx, stop := context.WithCancel(connCtx)
		defer stop()
		defer context.AfterFunc(srvCtx, stop)()

		ticker := time.NewTicker(monitorPushInterval)
		defer ticker.Stop()

		for {
			if err := writeSnapshot(loopCtx, c, src); err != nil {
				// A context cancellation (client close or shutdown) is an
				// expected teardown, not an application error.
				if loopCtx.Err() == nil {
					logger.Error("monitor ws write failed", "err", err.Error())
				}
				return
			}
			select {
			case <-loopCtx.Done():
				// On server shutdown send a graceful close frame; on a
				// client-initiated close the peer is gone and CloseNow suffices.
				if srvCtx.Err() != nil {
					_ = c.Close(websocket.StatusNormalClosure, "")
				}
				return
			case <-ticker.C:
			}
		}
	}
}

// writeSnapshot marshals one MonitorSnapshot and writes it as a text frame,
// bounded by writeTimeout so a slow/stuck client reader cannot wedge the push
// goroutine.
func writeSnapshot(ctx context.Context, c *websocket.Conn, src metrics.Source) error {
	// T214 fail-closed default: a zero Info and revealAddressing=false, so the
	// monitor compiles and REDACTS all addressing by default. T219 threads the
	// real monitor.Info and the kernel-bound loopback verdict through NewServer →
	// newWSHandler → here, at which point a loopback binding reveals addressing.
	payload, err := json.Marshal(BuildSnapshot(src, Info{}, false))
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.Write(writeCtx, websocket.MessageText, payload)
}

// monitorPushInterval is the cadence at which the /ws handler pushes a fresh
// MonitorSnapshot to each connected client (Q50: 1 Hz).
const monitorPushInterval = 1 * time.Second

// writeTimeout bounds a single snapshot-frame write to a slow client.
const writeTimeout = 5 * time.Second

// verifyLoopbackBind is the fail-closed, act-then-verify half of the loopback
// guard: it asserts that the address the kernel ACTUALLY bound (ln.Addr()) is a
// loopback TCP address, independent of any DNS resolution. It is applied only
// on the tokenless path (where off-host exposure is not authorized). A non-TCP
// or non-loopback bound address yields ErrNonLoopbackBind so the caller can
// close the listener and refuse.
func verifyLoopbackBind(a net.Addr) error {
	tcp, ok := a.(*net.TCPAddr)
	if !ok {
		return fmt.Errorf("%w: bound to non-TCP address %v", ErrNonLoopbackBind, a)
	}
	if !tcp.IP.IsLoopback() {
		return fmt.Errorf("%w: bound to non-loopback %s", ErrNonLoopbackBind, tcp.IP)
	}
	return nil
}

// monitorTokenCookie names the SameSite=Strict, HttpOnly cookie the bootstrap
// flow sets after a valid ?token= navigation, so subsequent requests (including
// the WebSocket upgrade the browser issues automatically) carry the token
// without it reappearing in the URL.
const monitorTokenCookie = "wanbond_monitor_token"

// authConfig is the monitor endpoint's auth policy: the optional static bearer
// token and the set of Host/Origin host values the endpoint accepts.
type authConfig struct {
	token   string
	allowed map[string]struct{}
}

// allowedHosts is the Host/Origin allowlist (host without port): the loopback
// aliases plus the configured listen host. It backs the DNS-rebinding defense —
// a rebinding attack presents an attacker-owned DOMAIN as Host, which is absent
// from this set (see hostAllowed).
func allowedHosts(listenAddr string) map[string]struct{} {
	set := map[string]struct{}{
		"localhost": {},
		"127.0.0.1": {},
		"::1":       {},
	}
	if host, _, err := net.SplitHostPort(listenAddr); err == nil && host != "" {
		set[host] = struct{}{}
	}
	return set
}

// hostOnly returns the host portion of a host:port value, stripping the port
// and any v6 brackets. A value with no port is returned as-is (minus brackets).
func hostOnly(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}

// hostAllowed reports whether a HOST header value is permitted (the
// DNS-rebinding defense). A value in the allowlist is accepted; an IP literal is
// accepted because DNS rebinding requires a resolvable DOMAIN and cannot target
// a raw IP; any other (domain) name is rejected. IMPORTANT: the IP-literal pass
// is valid ONLY for the Host header (the address the client connected to) — the
// Origin header is attacker-controlled and uses originAllowed instead.
func hostAllowed(hostport string, allowed map[string]struct{}) bool {
	h := hostOnly(hostport)
	if _, ok := allowed[h]; ok {
		return true
	}
	// An IP literal cannot be DNS-rebound (no name resolution); only domain
	// names are a rebinding vector, so a raw IP Host is always safe.
	return net.ParseIP(h) != nil
}

// originAllowed reports whether an ORIGIN header host is permitted (the
// cross-origin / CSRF defense — the SOLE CSRF control on the /ws upgrade, which
// SOP/CORS do not gate). Unlike hostAllowed it grants NO blanket IP-literal
// pass: the Origin is the CLIENT PAGE's own origin, fully attacker-controlled
// and serveable from any raw public IP, so an attacker page at http://<any-ip>/
// must NOT be trusted. An Origin is accepted only when it is EXACTLY same-origin
// with the request Host (the legitimate loopback/LAN-access case) or its host is
// an explicit allowlist entry (loopback aliases + the configured listen host).
func originAllowed(originHostport, reqHostport string, allowed map[string]struct{}) bool {
	if originHostport == reqHostport {
		return true // exact same-origin (host:port match)
	}
	_, ok := allowed[hostOnly(originHostport)]
	return ok
}

// middleware wraps next with the monitor auth layer: UNCONDITIONAL Host and
// Origin validation (DNS-rebinding + cross-origin defense, needs no secret),
// then — when a token is configured — static-token gating with a ?token=
// bootstrap-cookie flow. Applied to the whole mux so BOTH / and /ws are covered.
func (a *authConfig) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host, a.allowed) {
			http.Error(w, "forbidden: host not allowed", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host == "" || !originAllowed(u.Host, r.Host, a.allowed) {
				http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
				return
			}
		}
		if a.token != "" && !a.authorize(w, r) {
			return // authorize wrote the 401 or the bootstrap redirect
		}
		next.ServeHTTP(w, r)
	})
}

// authorize reports whether the request carries a valid token via
// `Authorization: Bearer <token>` or the session cookie. On a first GET
// navigation with a matching ?token= query it sets the SameSite=Strict HttpOnly
// cookie and 302-redirects to the same path WITHOUT the query (returning false —
// the redirect IS the response). A missing/invalid credential yields 401. All
// token comparisons are constant-time.
func (a *authConfig) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if a.tokenMatches(strings.TrimPrefix(h, "Bearer ")) {
			return true
		}
	}
	if ck, err := r.Cookie(monitorTokenCookie); err == nil && a.tokenMatches(ck.Value) {
		return true
	}
	if r.Method == http.MethodGet {
		if q := r.URL.Query().Get("token"); q != "" && a.tokenMatches(q) {
			http.SetCookie(w, &http.Cookie{
				Name:     monitorTokenCookie,
				Value:    a.token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				// Secure is intentionally false: the monitor serves plain HTTP
				// (loopback by default; a non-loopback bind still has no TLS in
				// v1 — the accepted residual risk, Q58 answer (a)).
				Secure: false,
			})
			stripped := *r.URL
			q2 := stripped.Query()
			q2.Del("token")
			stripped.RawQuery = q2.Encode()
			http.Redirect(w, r, stripped.RequestURI(), http.StatusFound)
			return false
		}
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

// tokenMatches is a constant-time comparison of a presented token against the
// configured one, so a timing side-channel cannot leak the token byte by byte.
func (a *authConfig) tokenMatches(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1
}
