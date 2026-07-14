package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
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

// TestServerWSDeliversOneSnapshot asserts the end-to-end skeleton: NewServer
// binds loopback, Start() serves, a coder/websocket client dials /ws and
// receives exactly one well-formed MonitorSnapshot JSON frame, and Close(ctx)
// shuts down cleanly with no leaked goroutine.
func TestServerWSDeliversOneSnapshot(t *testing.T) {
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

	// The skeleton sends exactly one frame then closes normally: the next Read
	// must observe a normal-closure, not a second frame.
	read2Ctx, read2Cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer read2Cancel()
	if _, _, err := c.Read(read2Ctx); err == nil {
		t.Fatal("second Read succeeded, want a close after exactly one frame")
	} else if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("second Read close status = %v, want StatusNormalClosure", websocket.CloseStatus(err))
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

	// Foreign Origin => 403 (cross-origin defense).
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

	// No Origin, loopback Host => 200 (a curl-style request is allowed).
	resp2, err := client.Get(base + "/")
	if err != nil {
		t.Fatalf("no-Origin request: %v", err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("no-Origin loopback => %d, want 200", resp2.StatusCode)
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
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("valid Bearer => %d, want 200", respB.StatusCode)
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
	if respC.StatusCode != http.StatusOK {
		t.Fatalf("cookie-authorized => %d, want 200", respC.StatusCode)
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
