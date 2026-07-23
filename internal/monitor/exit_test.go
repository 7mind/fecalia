package monitor

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingSwitcher is a fake monitor.ExitSwitcher: it records every peer name
// the POST /api/exit handler passes and returns monitor.ErrUnknownExitPeer for a
// name outside its known set (mimicking the device adapter's translation of the
// selector's typed *unknownExitError), otherwise recording the name as the new
// active exit and returning it (the idempotent same-name success returns the same
// name).
type recordingSwitcher struct {
	mu     sync.Mutex
	calls  []string
	known  map[string]bool
	active string
}

func (s *recordingSwitcher) fn(peer string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, peer)
	if !s.known[peer] {
		return "", fmt.Errorf("%w: %q", ErrUnknownExitPeer, peer)
	}
	s.active = peer
	return s.active, nil
}

func (s *recordingSwitcher) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// startExitServer starts a monitor Server on addr with the given token and exit
// switcher, and returns it plus a loopback base URL (dialed via 127.0.0.1 even on
// a 0.0.0.0 bind, so a non-loopback-bound server is still reachable for the
// loopback-gate test).
func startExitServer(t *testing.T, addr, token string, sw ExitSwitcher) (*Server, string) {
	t.Helper()
	srv, err := NewServer(addr, token, fakeSource{}, Info{}, sw, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer(%q): %v", addr, err)
	}
	srv.Start()
	base := "http://127.0.0.1:" + strconv.Itoa(srv.Addr().(*net.TCPAddr).Port)
	return srv, base
}

func exitClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

// postExit issues a POST /api/exit with the given raw body, applying mut to the
// request (headers/cookies) before sending. The caller closes the response body.
func postExit(t *testing.T, client *http.Client, base, body string, mut func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, base+exitPath, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if mut != nil {
		mut(req)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", exitPath, err)
	}
	return resp
}

func decodeActiveExit(t *testing.T, resp *http.Response) string {
	t.Helper()
	var out exitResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode exitResponse: %v", err)
	}
	return out.ActiveExit
}

// TestExit_LoopbackCookie_Switches asserts the primary path: a loopback-bound
// monitor with a valid SameSite=Strict cookie, on a same-origin POST, switches the
// active exit and returns 200 {"activeExit": "<name>"}. This is the explicit
// proof that the HttpOnly cookie carries the POST (a same-origin fetch does).
func TestExit_LoopbackCookie_Switches(t *testing.T) {
	const token = "cookie-tok"
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", token, sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: monitorTokenCookie, Value: token})
		r.Header.Set("Origin", base) // same-origin fetch
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback + cookie POST => %d, want 200", resp.StatusCode)
	}
	if got := decodeActiveExit(t, resp); got != "tokyo" {
		t.Fatalf("activeExit = %q, want %q", got, "tokyo")
	}
	if sw.callCount() != 1 {
		t.Fatalf("selector call count = %d, want 1", sw.callCount())
	}
}

// TestExit_LoopbackBearer_Switches asserts the Bearer-token path: loopback bind +
// Authorization: Bearer <token> switches and returns 200.
func TestExit_LoopbackBearer_Switches(t *testing.T) {
	const token = "bearer-tok"
	sw := &recordingSwitcher{known: map[string]bool{"osaka": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", token, sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"osaka"}`, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback + Bearer POST => %d, want 200", resp.StatusCode)
	}
	if got := decodeActiveExit(t, resp); got != "osaka" {
		t.Fatalf("activeExit = %q, want %q", got, "osaka")
	}
	if sw.callCount() != 1 {
		t.Fatalf("selector call count = %d, want 1", sw.callCount())
	}
}

// TestExit_NonLoopbackWithToken_Forbidden is the HARD loopback gate: a
// token-authorized NON-loopback bind refuses the mutating control with 403 EVEN
// with a valid Bearer token, and the selector is NEVER called — a remote/exposed
// monitor stays strictly read-only.
func TestExit_NonLoopbackWithToken_Forbidden(t *testing.T) {
	const token = "remote-tok"
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "0.0.0.0:0", token, sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Origin", base) // same-origin, so the middleware admits it
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-loopback + valid token POST => %d, want 403", resp.StatusCode)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called %d times on a forbidden non-loopback POST, want 0", sw.callCount())
	}
}

// TestExit_CrossOrigin_Forbidden asserts the auth middleware covers the new route:
// a foreign Origin is rejected with 403 before the handler runs, so the selector
// is not called.
func TestExit_CrossOrigin_Forbidden(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example.com")
	})
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST => %d, want 403", resp.StatusCode)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called %d times on a cross-origin POST, want 0", sw.callCount())
	}
}

// TestExit_NoCredential_Unauthorized asserts the token gate applies to the new
// route: with a token configured and no credential presented, the POST is 401.
func TestExit_NoCredential_Unauthorized(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "need-a-tok", sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-credential POST (token configured) => %d, want 401", resp.StatusCode)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called %d times on an unauthorized POST, want 0", sw.callCount())
	}
}

// TestExit_GetMethod_NotAllowed asserts a non-POST method on the route is 405.
// NewServer registers the route under BOTH method-verbed patterns
// ("POST /api/exit" and "GET /api/exit") bound to the same handler, so a GET is
// routed to the exit handler (rather than falling through to the static "/"
// subtree and 404ing) and the handler's own method check returns 405 with an
// Allow: POST header.
func TestExit_GetMethod_NotAllowed(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	resp, err := exitClient().Get(base + exitPath)
	if err != nil {
		t.Fatalf("GET %s: %v", exitPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET %s => %d, want 405", exitPath, resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Errorf("405 Allow header = %q, want %q", allow, http.MethodPost)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called on a GET, want 0")
	}
}

// TestExit_UnknownPeer_BadRequest asserts an unknown / non-exit-capable peer maps
// to 400 (the selector's typed *unknownExitError adapted to ErrUnknownExitPeer),
// echoing ONLY the caller-supplied name.
func TestExit_UnknownPeer_BadRequest(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"nowhere"}`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown-peer POST => %d, want 400", resp.StatusCode)
	}
	var e exitError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode exitError: %v", err)
	}
	if !strings.Contains(e.Error, "nowhere") {
		t.Errorf("400 body = %q, want it to name the caller-supplied peer", e.Error)
	}
	if strings.Contains(e.Error, "exitselector") {
		t.Errorf("400 body leaked selector internals: %q", e.Error)
	}
}

// TestExit_MalformedJSON_BadRequest asserts a non-JSON / malformed body is 400 and
// the selector is not called.
func TestExit_MalformedJSON_BadRequest(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `not-json`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed-JSON POST => %d, want 400", resp.StatusCode)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called %d times on malformed JSON, want 0", sw.callCount())
	}
}

// TestExit_NilSwitcher_BadRequest asserts the nil-ExitSwitcher branch: a
// loopback-bound monitor wired with NO switcher (a role with no multi-exit
// selector, or the read-only path) rejects every POST with 400 "no exit-capable
// peer configured" — the loopback gate passes, so this exercises the nil branch
// specifically rather than the 403 loopback refusal.
func TestExit_NilSwitcher_BadRequest(t *testing.T) {
	srv, base := startExitServer(t, "127.0.0.1:0", "", nil)
	defer closeMonitor(t, srv)

	resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("nil-switcher POST => %d, want 400", resp.StatusCode)
	}
	var e exitError
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatalf("decode exitError: %v", err)
	}
	if !strings.Contains(e.Error, "no exit-capable peer configured") {
		t.Errorf("400 body = %q, want it to name the nil-switcher reason", e.Error)
	}
}

// TestExit_OversizedBody_BadRequest asserts the http.MaxBytesReader bound on the
// sole body-accepting route: a body exceeding maxExitBodyBytes makes the decode
// error and maps to 400, and the selector is never called.
func TestExit_OversizedBody_BadRequest(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	// A syntactically valid JSON object whose peer value alone exceeds the bound,
	// so the decode fails on the MaxBytesReader limit rather than on JSON syntax.
	oversized := `{"peer":"` + strings.Repeat("a", maxExitBodyBytes+1) + `"}`
	resp := postExit(t, exitClient(), base, oversized, nil)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized-body POST => %d, want 400", resp.StatusCode)
	}
	if sw.callCount() != 0 {
		t.Fatalf("selector called %d times on an oversized body, want 0", sw.callCount())
	}
}

// TestExit_IdempotentSameName asserts a same-name switch is 200 both times (an
// idempotent no-op still returns the active exit).
func TestExit_IdempotentSameName(t *testing.T) {
	sw := &recordingSwitcher{known: map[string]bool{"tokyo": true}}
	srv, base := startExitServer(t, "127.0.0.1:0", "", sw.fn)
	defer closeMonitor(t, srv)

	for i := 0; i < 2; i++ {
		resp := postExit(t, exitClient(), base, `{"peer":"tokyo"}`, nil)
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			t.Fatalf("same-name switch #%d => %d, want 200", i, resp.StatusCode)
		}
		if got := decodeActiveExit(t, resp); got != "tokyo" {
			_ = resp.Body.Close()
			t.Fatalf("same-name switch #%d activeExit = %q, want tokyo", i, got)
		}
		_ = resp.Body.Close()
	}
}
