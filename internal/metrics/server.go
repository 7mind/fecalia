package metrics

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/7mind/wanbond/internal/log"
)

// metricsPath is the sole route served by the endpoint.
const metricsPath = "/metrics"

// readHeaderTimeout bounds how long a client may take to send request headers,
// closing the Slowloris hole that a bare http.Server leaves open (bodyclose /
// gosec hygiene). The endpoint is loopback-only, but the bound is cheap.
const readHeaderTimeout = 5 * time.Second

// ErrNonLoopbackBind is returned when the requested listen address is not
// loopback. The metrics endpoint exposes per-path operational data (endpoints,
// loss, liveness) and must never be reachable off-host by default; binding it to
// a routable or wildcard address is refused at construction (fail fast).
var ErrNonLoopbackBind = errors.New("metrics: listen address must be loopback (127.0.0.0/8 or ::1)")

// Server is the localhost-bound Prometheus /metrics endpoint. It owns a private
// registry fed by a Source; it never touches the global default registry.
type Server struct {
	ln  net.Listener
	srv *http.Server
	log log.Logger
}

// NewServer validates that addr is loopback, binds a TCP listener, and wires the
// /metrics handler over a private registry fed by src. It returns
// ErrNonLoopbackBind WITHOUT binding if addr is not loopback. addr is a
// host:port string; a ":0" or "127.0.0.1:0" port yields an OS-assigned port
// readable via Addr after construction.
func NewServer(addr string, src Source, logger log.Logger) (*Server, error) {
	if err := requireLoopback(addr); err != nil {
		return nil, err
	}

	reg := prometheus.NewRegistry()
	if err := reg.Register(NewCollector(src)); err != nil {
		return nil, fmt.Errorf("metrics: register collector: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("metrics: listen on %q: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.Handle(metricsPath, promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorHandling: promhttp.HTTPErrorOnError,
	}))

	return &Server{
		ln: ln,
		srv: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
		},
		log: logger.Component("metrics"),
	}, nil
}

// Addr returns the actual bound listen address (with the resolved port).
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// URL returns the scrape URL for the endpoint, suitable for the Fetch helper.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s%s", s.ln.Addr().String(), metricsPath)
}

// Start serves the endpoint in a background goroutine and returns immediately.
// A Serve error other than the expected http.ErrServerClosed (from Close) is
// logged; the listener is already bound, so Start itself cannot fail.
func (s *Server) Start() {
	s.log.Info("metrics endpoint listening", "addr", s.ln.Addr().String(), "path", metricsPath)
	go func() {
		if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("metrics endpoint serve error", "err", err.Error())
		}
	}()
}

// Close gracefully shuts the endpoint down, bounded by ctx.
func (s *Server) Close(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// requireLoopback enforces that addr's host is a loopback address. An IP literal
// must satisfy IsLoopback; a hostname (e.g. "localhost") is accepted only if it
// resolves to at least one address and EVERY resolved address is loopback — a
// hostname that maps to any routable address is refused. An empty host (":9095")
// binds every interface and is refused.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("metrics: invalid listen address %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("%w: empty host in %q binds all interfaces", ErrNonLoopbackBind, addr)
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: got %q", ErrNonLoopbackBind, addr)
		}
		return nil
	}

	// Hostname: resolve and require every resolved address to be loopback.
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("metrics: cannot resolve listen host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("metrics: listen host %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return fmt.Errorf("%w: host %q resolves to non-loopback %s", ErrNonLoopbackBind, host, ip)
		}
	}
	return nil
}
