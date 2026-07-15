package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg
}

// TestServerWSPushesSnapshots asserts the end-to-end push path: NewServer binds
// loopback, Start() serves, a coder/websocket client dials /ws and receives a
// first well-formed MonitorSnapshot JSON frame followed by a SECOND pushed frame
// at the ~1Hz cadence (T165), and Close(ctx) shuts down cleanly with no leaked
// goroutine.
func TestServerWSPushesSnapshots(t *testing.T) {
	defer goleak.VerifyNone(t)

	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{Name: "starlink", TxBytes: 1000, RxBytes: 2000, State: telemetry.StateUp},
		},
		session:   metrics.SessionSnapshot{Established: true, LastHandshakeAge: 30 * time.Second},
		peerNames: []string{""},
	}

	srv, err := NewServer("127.0.0.1:0", "", src, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	url := fmt.Sprintf("ws://%s/ws", srv.Addr().String())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("websocket.Dial(%q): %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	typ, data, err := c.Read(readCtx)
	if err != nil {
		t.Fatalf("Read first frame: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("frame type = %v, want MessageText", typ)
	}

	var snap MonitorSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
	}
	if len(snap.Paths) != 1 || snap.Paths[0].Name != "starlink" {
		t.Fatalf("snapshot paths = %#v, want a single starlink path", snap.Paths)
	}
	if !snap.Session.Established || snap.Session.LastHandshakeSeconds != 30 {
		t.Fatalf("snapshot session = %#v, want established=true lastHandshakeSeconds=30", snap.Session)
	}

	// T165: the handler PUSHES snapshots at monitorPushInterval, so the next
	// Read must observe ANOTHER frame (not a close) within ~1 interval + slack,
	// verifying the ~1Hz cadence.
	read2Ctx, read2Cancel := context.WithTimeout(context.Background(), monitorPushInterval+2*time.Second)
	defer read2Cancel()
	typ2, data2, err := c.Read(read2Ctx)
	if err != nil {
		t.Fatalf("Read second (pushed) frame within ~%v: %v", monitorPushInterval+2*time.Second, err)
	}
	if typ2 != websocket.MessageText {
		t.Fatalf("second frame type = %v, want MessageText", typ2)
	}
	if err := json.Unmarshal(data2, new(MonitorSnapshot)); err != nil {
		t.Fatalf("unmarshal second MonitorSnapshot: %v (payload=%s)", err, data2)
	}
}

// TestServerWSCloseStopsPush asserts prompt, leak-free teardown: with a client
// connected and NOT reading further, Close(ctx) cancels the push handler
// promptly (via the server context) and returns without hanging, and goleak
// confirms the push goroutine did not outlive Close. This exercises the
// stalled-client shutdown path (a blocked write unblocked by srvCtx).
func TestServerWSCloseStopsPush(t *testing.T) {
	defer goleak.VerifyNone(t)

	srv, err := NewServer("127.0.0.1:0", "", fakeSource{peerNames: []string{""}}, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()

	url := fmt.Sprintf("ws://%s/ws", srv.Addr().String())
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()

	// Read the first frame, then STOP reading (a stalled client). The push loop
	// keeps running — parked on the ticker between frames, or eventually blocked
	// on a write once the connection buffer fills — and Close must cancel it
	// promptly either way (via the server context) rather than waiting out the
	// per-write timeout.
	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	if _, _, err := c.Read(readCtx); err != nil {
		t.Fatalf("Read first frame: %v", err)
	}

	// Close must return PROMPTLY (well under the 5s write timeout) because the
	// server context cancels the blocked write.
	start := time.Now()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer closeCancel()
	if err := srv.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Close took %v, want prompt (<2s) shutdown of a stalled-client push", elapsed)
	}
}

// TestNonLoopbackWithoutTokenRefused asserts a non-loopback (or wildcard) bind
// with an empty token is refused with ErrNonLoopbackBind and opens no listener.
func TestNonLoopbackWithoutTokenRefused(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"wildcard v4", "0.0.0.0:0"},
		{"wildcard empty host", ":9099"},
		{"routable literal", "192.0.2.10:9099"},
		{"wildcard v6", "[::]:0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, err := NewServer(tc.addr, "", fakeSource{}, testLogger(t))
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = srv.Close(ctx)
				t.Fatalf("NewServer(%q, \"\") succeeded, want ErrNonLoopbackBind", tc.addr)
			}
			if !errors.Is(err, ErrNonLoopbackBind) {
				t.Fatalf("NewServer(%q, \"\") err = %v, want ErrNonLoopbackBind", tc.addr, err)
			}
			if srv != nil {
				t.Fatalf("NewServer(%q, \"\") returned a non-nil server on refusal", tc.addr)
			}
		})
	}
}

// TestLoopbackBindAccepted asserts loopback literals bind successfully with no
// token, and Close is clean.
func TestLoopbackBindAccepted(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0"} {
		t.Run(addr, func(t *testing.T) {
			srv, err := NewServer(addr, "", fakeSource{}, testLogger(t))
			if err != nil {
				t.Fatalf("NewServer(%q): %v", addr, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Close(ctx); err != nil {
				t.Errorf("Close: %v", err)
			}
		})
	}
}

// TestCloseReleasesPortWithoutStart is the regression guard for the listener
// leak: Close on a NewServer that was never Start()ed must release the bound
// socket, not merely Shutdown the (never-Serve'd) http.Server. Proven by
// re-binding the SAME port after Close: with the leak, the second NewServer
// fails EADDRINUSE. The first server binds 127.0.0.1:0, its OS-assigned port is
// read back from Addr(), and the re-bind targets that exact port.
func TestCloseReleasesPortWithoutStart(t *testing.T) {
	srv, err := NewServer("127.0.0.1:0", "", fakeSource{}, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	addr := srv.Addr().String() // 127.0.0.1:<assigned port>

	// Close WITHOUT ever calling Start(): only the explicit s.ln.Close in Close
	// can release the port here, since Shutdown owns no Serve'd listener.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := srv.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	srv2, err := NewServer(addr, "", fakeSource{}, testLogger(t))
	if err != nil {
		t.Fatalf("re-bind on %q after Close failed (leaked listener): %v", addr, err)
	}
	reCloseCtx, reCloseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reCloseCancel()
	if err := srv2.Close(reCloseCtx); err != nil {
		t.Errorf("Close (re-bound server): %v", err)
	}
}

// TestNonLoopbackWithTokenAccepted locks the token-authorized branch: a
// non-loopback (wildcard) bind with a NON-EMPTY token must SUCCEED — the token
// is the explicit opt-in that permits off-host exposure. This guards against a
// regression that inverts the token check and refuses authorized binds (or,
// worse, accepts unauthenticated ones). Close must be clean.
func TestNonLoopbackWithTokenAccepted(t *testing.T) {
	srv, err := NewServer("0.0.0.0:0", "secret-token", fakeSource{}, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer(\"0.0.0.0:0\", token): %v", err)
	}
	if srv == nil {
		t.Fatal("NewServer returned nil server with nil error")
	}
	if srv.Addr() == nil {
		t.Fatal("authorized non-loopback bind produced no listen address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// startAuthTestServer starts a monitor Server on loopback with the given token
// and returns it, its base http URL, and a client that does NOT auto-follow
// redirects (so the ?token= bootstrap 302 is observable).
func startAuthTestServer(t *testing.T, token string) (*Server, string) {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", token, fakeSource{}, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()
	return srv, "http://" + srv.Addr().String()
}

func noRedirectClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func closeMonitor(t *testing.T, srv *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestAuthHostOrigin asserts the unconditional (no-secret) Host/Origin defense:
// a foreign Origin and a foreign Host (DNS-rebinding) are both 403; a no-Origin
// request with a loopback Host is allowed.
func TestAuthHostOrigin(t *testing.T) {
	defer goleak.VerifyNone(t)
	srv, base := startAuthTestServer(t, "")
	defer closeMonitor(t, srv)
	client := noRedirectClient()

	// Foreign DOMAIN Origin => 403 (cross-origin defense).
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("foreign-Origin request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Origin => %d, want 403", resp.StatusCode)
	}

	// Foreign IP-LITERAL Origin => 403. An attacker page served from a bare
	// public IP is fully attacker-controlled; the Host-header DNS-rebinding IP
	// pass must NOT extend to the Origin gate (regression guard for the reused-
	// hostAllowed cross-origin bypass).
	reqIP, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	reqIP.Header.Set("Origin", "http://198.51.100.7")
	respIP, err := client.Do(reqIP)
	if err != nil {
		t.Fatalf("foreign-IP-Origin request: %v", err)
	}
	_ = respIP.Body.Close()
	if respIP.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign IP-literal Origin => %d, want 403", respIP.StatusCode)
	}

	// Foreign Host (DNS-rebinding) => 403.
	reqH, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	reqH.Host = "evil.example.com"
	respH, err := client.Do(reqH)
	if err != nil {
		t.Fatalf("foreign-Host request: %v", err)
	}
	_ = respH.Body.Close()
	if respH.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host => %d, want 403", respH.StatusCode)
	}

	// No Origin, loopback Host: auth ALLOWS the request through to the static
	// handler. The / body depends on whether the frontend bundle is built, so
	// assert the middleware did not reject it (not a specific 200).
	resp2, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("no-Origin request: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode == http.StatusForbidden || resp2.StatusCode == http.StatusUnauthorized {
		t.Fatalf("no-Origin loopback rejected by auth => %d, want allowed through", resp2.StatusCode)
	}
}

// TestAuthTokenFlow asserts the static-token gate + ?token= bootstrap-cookie
// flow: no credential 401; Bearer 200; wrong token 401; ?token= sets a
// SameSite=Strict HttpOnly cookie + 302 redirect stripping the query; the
// cookie then authorizes subsequent requests.
func TestAuthTokenFlow(t *testing.T) {
	defer goleak.VerifyNone(t)
	const token = "s3cr3t-monitor-token"
	srv, base := startAuthTestServer(t, token)
	defer closeMonitor(t, srv)
	client := noRedirectClient()

	// No credential => 401.
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("no-credential request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential => %d, want 401", resp.StatusCode)
	}

	// Authorization: Bearer <token> => 200.
	reqB, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	reqB.Header.Set("Authorization", "Bearer "+token)
	respB, err := client.Do(reqB)
	if err != nil {
		t.Fatalf("bearer request: %v", err)
	}
	_ = respB.Body.Close()
	if respB.StatusCode == http.StatusUnauthorized || respB.StatusCode == http.StatusForbidden {
		t.Fatalf("valid Bearer rejected => %d, want authorized through", respB.StatusCode)
	}

	// Wrong token => 401.
	reqW, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	reqW.Header.Set("Authorization", "Bearer not-the-token")
	respW, err := client.Do(reqW)
	if err != nil {
		t.Fatalf("wrong-token request: %v", err)
	}
	_ = respW.Body.Close()
	if respW.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token => %d, want 401", respW.StatusCode)
	}

	// GET /?token=<token> => 302 + Set-Cookie SameSite=Strict HttpOnly, query stripped.
	respT, err := client.Get(base + "/?token=" + token)
	if err != nil {
		t.Fatalf("bootstrap ?token= request: %v", err)
	}
	_ = respT.Body.Close()
	if respT.StatusCode != http.StatusFound {
		t.Fatalf("?token= => %d, want 302", respT.StatusCode)
	}
	if loc := respT.Header.Get("Location"); loc != "/" {
		t.Fatalf("?token= redirect Location = %q, want %q (query must be stripped)", loc, "/")
	}
	var cookie *http.Cookie
	for _, c := range respT.Cookies() {
		if c.Name == monitorTokenCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("?token= did not set the monitor token cookie")
	}
	if !cookie.HttpOnly {
		t.Error("token cookie is not HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("token cookie SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Value != token {
		t.Errorf("token cookie value = %q, want the token", cookie.Value)
	}

	// The cookie alone authorizes a subsequent request => 200.
	reqC, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	reqC.AddCookie(&http.Cookie{Name: monitorTokenCookie, Value: token})
	respC, err := client.Do(reqC)
	if err != nil {
		t.Fatalf("cookie request: %v", err)
	}
	_ = respC.Body.Close()
	if respC.StatusCode == http.StatusUnauthorized || respC.StatusCode == http.StatusForbidden {
		t.Fatalf("cookie-authorized rejected => %d, want authorized through", respC.StatusCode)
	}
}

// TestHostAllowed unit-checks the DNS-rebinding host classifier directly: allowlisted
// names and any IP literal pass; an arbitrary domain is rejected.
func TestHostAllowed(t *testing.T) {
	allowed := allowedHosts("127.0.0.1:9101")
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:9101", true},
		{"localhost:9101", true},
		{"[::1]:9101", true},
		{"::1", true},
		{"192.0.2.10:9101", true}, // IP literal: not DNS-rebindable
		{"192.0.2.10", true},
		{"evil.example.com", false},
		{"evil.example.com:9101", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := hostAllowed(tc.host, allowed); got != tc.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

// TestStaticHandler verifies the embedded-bundle handler (T167) with a synthetic
// fs.FS (decoupled from the real build-time embed): / serves index.html with a
// no-cache header, a hashed asset serves with an immutable long-lived cache, and
// a missing file 404s. Content-Type is set by http.FileServer from the
// extension.
func TestStaticHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":             {Data: []byte("<!doctype html><title>wanbond monitor</title>")},
		"assets/index-abc123.js": {Data: []byte("console.log('x')")},
	}
	h := staticHandler(fsys)

	// GET / serves index.html with no-cache (unhashed entrypoint).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / => %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET / Cache-Control = %q, want no-cache", cc)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html*", ct)
	}

	// A content-hashed asset is served with an immutable, long-lived cache.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/assets/index-abc123.js", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET asset => %d, want 200", rec2.Code)
	}
	if cc := rec2.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q, want immutable long-lived", cc)
	}

	// A missing file 404s (no SPA fallback — the monitor UI is a single page).
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/nope.js", nil))
	if rec3.Code != http.StatusNotFound {
		t.Fatalf("GET missing => %d, want 404", rec3.Code)
	}
}

// TestOriginAllowed unit-checks the Origin classifier: exact same-origin and
// allowlisted hosts pass; a foreign IP literal is REJECTED (unlike the Host
// classifier, the Origin is attacker-controlled so IP literals get no pass).
func TestOriginAllowed(t *testing.T) {
	allowed := allowedHosts("127.0.0.1:9101")
	const reqHost = "127.0.0.1:9101"
	cases := []struct {
		origin string
		want   bool
	}{
		{"127.0.0.1:9101", true}, // exact same-origin
		{"localhost:9101", true}, // allowlisted host (any port)
		{"[::1]:9101", true},     // allowlisted host
		{"198.51.100.7", false},  // foreign bare IP — the cross-origin bypass
		{"198.51.100.7:80", false},
		{"evil.example.com", false},
		{"evil.example.com:9101", false},
	}
	for _, tc := range cases {
		if got := originAllowed(tc.origin, reqHost, allowed); got != tc.want {
			t.Errorf("originAllowed(%q, %q) = %v, want %v", tc.origin, reqHost, got, tc.want)
		}
	}
}

// TestAuthForeignOriginRejectedOnWS asserts the /ws UPGRADE is refused for a
// foreign (bare-IP) Origin. The middleware Origin gate is the sole CSRF control
// on the WebSocket upgrade (SOP/CORS do not protect it), so this is the
// security-critical path for the Origin fix.
func TestAuthForeignOriginRejectedOnWS(t *testing.T) {
	defer goleak.VerifyNone(t)
	srv, _ := startAuthTestServer(t, "")
	defer closeMonitor(t, srv)

	wsURL := "ws://" + srv.Addr().String() + "/ws"
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://198.51.100.7"}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		if c != nil {
			_ = c.CloseNow()
		}
		t.Fatal("WS dial with a foreign-IP Origin succeeded, want rejection")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("WS foreign-IP Origin handshake status = %d, want 403", resp.StatusCode)
	}
}
