package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	ln  net.Listener
	srv *http.Server
	log log.Logger
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

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+rootPath, handleRoot)
	mux.HandleFunc("GET "+wsPath, newWSHandler(src, logger.Component("monitor")))

	return &Server{
		ln: ln,
		srv: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
		},
		log: logger.Component("monitor"),
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
	shutdownErr := s.srv.Shutdown(ctx)
	if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		if shutdownErr != nil {
			return shutdownErr
		}
		return err
	}
	return shutdownErr
}

// handleRoot is the placeholder document handler. The real static assets (the
// built frontend, served via //go:embed) land in T167; for the skeleton this
// returns a minimal plaintext body so the route is wired and testable.
func handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("wanbond monitor\n"))
}

// newWSHandler returns the /ws upgrade handler. For the skeleton it accepts the
// WebSocket, sends exactly ONE MonitorSnapshot JSON frame built from src, then
// closes normally. The 1s push loop is T165; auth/Origin handling is T164 —
// this handler adds neither.
func newWSHandler(src metrics.Source, logger log.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			logger.Error("monitor ws accept failed", "err", err.Error())
			return
		}
		// CloseNow closes the underlying connection immediately WITHOUT sending a
		// close frame or status code; it is idempotent, so on the normal path
		// (the explicit StatusNormalClosure Close below) this deferred call is a
		// no-op, and on an early bail it just tears the connection down.
		defer func() { _ = c.CloseNow() }()

		payload, err := json.Marshal(BuildSnapshot(src))
		if err != nil {
			logger.Error("monitor ws marshal snapshot failed", "err", err.Error())
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), writeTimeout)
		defer cancel()
		if err := c.Write(ctx, websocket.MessageText, payload); err != nil {
			logger.Error("monitor ws write failed", "err", err.Error())
			return
		}

		_ = c.Close(websocket.StatusNormalClosure, "")
	}
}

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
