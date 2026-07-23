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
	// exitPath is the SOLE mutating control route (T258, G28/M106): POST switches
	// the active exit-capable peer. The handler is registered for both GET and
	// POST (see NewServer) so a non-POST returns a clean 405 instead of falling
	// through to the "/" static subtree; the handler also enforces the hard
	// loopback-only gate, independent of the auth layer.
	exitPath = "/api/exit"
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
//
// revealOptIn is the operator's [monitor].reveal_addressing config flag (T280):
// it widens the addressing-reveal verdict to cover an authenticated non-loopback
// bind, but it does NOT widen the RAW loopbackBound verdict — the mutating exit
// control's hard loopback-only gate is unaffected by it.
func NewServer(addr, token string, src metrics.Source, info Info, switchExit ExitSwitcher, revealOptIn bool, logger log.Logger) (*Server, error) {
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
	// Two DISTINCT verdicts are computed here and threaded separately (T280):
	//
	//   - loopbackBound is the RAW kernel-bound act-then-verify verdict: true ONLY
	//     when the kernel ACTUALLY bound a loopback interface (the same
	//     act-then-verify check as the tokenless guard above, but computed
	//     unconditionally). It is the HARD gate for the mutating POST /api/exit
	//     control (T258) and feeds MonitorSnapshot.ExitControlAvailable — a
	//     reveal_addressing opt-in must NOT widen it.
	//   - revealAddressing is the Q62/Q64 addressing-reveal gate: loopbackBound OR
	//     the operator reveal_addressing opt-in. A token-authorized NON-loopback
	//     bind still redacts per Q62 UNLESS the operator opted in. BuildSnapshot
	//     performs the server-side redaction when this is false.
	loopbackBound := verifyLoopbackBind(ln.Addr()) == nil
	revealAddressing := loopbackBound || revealOptIn

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
	mux.HandleFunc("GET "+wsPath, newWSHandler(srvCtx, src, info, revealAddressing, loopbackBound, logger.Component("monitor")))
	// POST /api/exit — the sole mutating route (T258). It is gated by the RAW
	// loopbackBound verdict, NOT revealAddressing: a non-loopback bind refuses the
	// control REGARDLESS of a valid token OR a reveal_addressing opt-in, so a
	// remote/exposed monitor stays strictly read-only even when it reveals
	// addressing (T280). The auth middleware (Host/Origin + token) wraps it exactly
	// like every other route. The handler is registered for BOTH GET and POST: POST
	// is the real control; GET is shadowed here (rather than falling through to the
	// "/" static subtree and 404ing) so a non-POST method returns a clean 405 from
	// the handler's own method check. Any other method (PUT/DELETE/…) matches
	// neither /api/exit pattern and the mux returns 405 for it directly.
	exitHandler := newExitHandler(loopbackBound, switchExit, logger.Component("monitor"))
	mux.HandleFunc("POST "+exitPath, exitHandler)
	mux.HandleFunc("GET "+exitPath, exitHandler)

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
func newWSHandler(srvCtx context.Context, src metrics.Source, info Info, revealAddressing, loopbackBound bool, logger log.Logger) http.HandlerFunc {
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
			if err := writeSnapshot(loopCtx, c, src, info, revealAddressing, loopbackBound); err != nil {
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
func writeSnapshot(ctx context.Context, c *websocket.Conn, src metrics.Source, info Info, revealAddressing, loopbackBound bool) error {
	// info + both verdicts are threaded from NewServer: revealAddressing (loopback
	// OR reveal opt-in) drives the Q62/Q64 server-side redaction, while the raw
	// loopbackBound drives ExitControlAvailable. info is a placeholder zero value
	// until the device layer supplies the real identity/endpoint seam (T222).
	payload, err := json.Marshal(BuildSnapshot(src, info, revealAddressing, loopbackBound))
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
	if scheme, credential, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		if a.tokenMatches(strings.TrimSpace(credential)) {
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

// ErrUnknownExitPeer is the sentinel an ExitSwitcher returns (wrapped) when the
// requested peer is not a configured exit-capable peer. It lets the device adapt
// the exit selector's typed internal error across the package boundary WITHOUT
// the monitor importing internal/device: the POST /api/exit handler maps any
// error wrapping it to 400, echoing only the caller-supplied name.
var ErrUnknownExitPeer = errors.New("monitor: unknown or non-exit-capable exit peer")

// ExitSwitcher is the mutating control seam the POST /api/exit handler invokes to
// repoint default-route ownership onto the named exit-capable peer, returning the
// resulting active exit name on success. The device layer supplies it (adapting
// exitSelector.Switch); the monitor package never imports internal/device — the
// provider is injected exactly like the Info read-seam closures. A nil switcher
// (a role with no multi-exit selector, or the test/read-only path) makes every
// request a 400. The implementation MUST return an error wrapping
// ErrUnknownExitPeer when peer is not a configured exit-capable peer, and MUST
// NOT leak any selector internals beyond the caller-supplied name.
type ExitSwitcher func(peer string) (activeExit string, err error)

// maxExitBodyBytes bounds the POST /api/exit request body. The only accepted
// payload is a single short peer name ({"peer":"<name>"}), so a few KiB is
// generous; the bound closes an unbounded-read DoS on the sole body-accepting
// route (an over-limit body is mapped to the same 400 as malformed JSON via
// http.MaxBytesReader).
const maxExitBodyBytes = 4 << 10 // 4 KiB

// exitRequest is the POST /api/exit JSON body: the name of the exit-capable peer
// to make active.
type exitRequest struct {
	Peer string `json:"peer"`
}

// exitResponse is the 200 body: the resulting active exit name (the requested
// peer, whether an actual switch occurred or an idempotent same-name no-op).
type exitResponse struct {
	ActiveExit string `json:"activeExit"`
}

// exitError is the stable JSON error body every non-200 exit response carries.
type exitError struct {
	Error string `json:"error"`
}

// newExitHandler builds the POST /api/exit control handler. Check order: method
// (405 for non-POST) → HARD loopback gate (403 on any non-loopback bind, before
// any state is read, regardless of a valid token) → decode (400 on malformed
// JSON) → switchExit (400 on an unknown/non-exit-capable peer, 500 on an engine
// failure, 200 with the active exit otherwise, including an idempotent same-name
// switch). loopbackBound is the RAW kernel-bound act-then-verify verdict,
// DISTINCT from the widened revealAddressing gate (T280): revealAddressing is
// loopbackBound OR the reveal_addressing opt-in, so on a reveal-override
// non-loopback bind addressing is revealed while this gate still refuses. It is
// captured at construction — the bound address does not change over the
// listener's life.
func newExitHandler(loopbackBound bool, switchExit ExitSwitcher, logger log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeExitError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// HARD loopback gate: the mutating control surface exists ONLY on a monitor
		// the kernel actually bound to loopback — the RAW loopbackBound verdict,
		// NOT the widened revealAddressing/AddressingHidden gate (T280). A
		// remote/exposed (token-authorized non-loopback) monitor stays strictly
		// read-only, so this 403 fires even with a valid token AND even when a
		// reveal_addressing opt-in has unhidden addressing (AddressingHidden=false).
		if !loopbackBound {
			writeExitError(w, http.StatusForbidden, "exit control is available only on a loopback-bound monitor")
			return
		}
		if switchExit == nil {
			writeExitError(w, http.StatusBadRequest, "no exit-capable peer configured")
			return
		}
		// Bound the body before decoding: the only valid payload is a short peer
		// name, so cap the read at maxExitBodyBytes. An over-limit body makes the
		// decoder error, mapped to the same 400 as malformed JSON.
		var req exitRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxExitBodyBytes)).Decode(&req); err != nil {
			writeExitError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		active, err := switchExit(req.Peer)
		if err != nil {
			if errors.Is(err, ErrUnknownExitPeer) {
				// Echo ONLY the caller-supplied name — never selector internals.
				writeExitError(w, http.StatusBadRequest, fmt.Sprintf("unknown or non-exit-capable peer: %q", req.Peer))
				return
			}
			logger.Error("monitor exit switch failed", "peer", req.Peer, "err", err.Error())
			writeExitError(w, http.StatusInternalServerError, "exit switch failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(exitResponse{ActiveExit: active}); err != nil {
			logger.Error("monitor exit response encode failed", "err", err.Error())
		}
	}
}

// writeExitError writes the stable {"error": msg} JSON body with the given status.
func writeExitError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(exitError{Error: msg})
}
