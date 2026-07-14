package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
