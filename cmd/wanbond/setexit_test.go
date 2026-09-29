package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/monitor"
)

func TestUsageListsSubcommands(t *testing.T) {
	fs := flag.NewFlagSet("wanbond", flag.ContinueOnError)
	fs.String("config", "", "path to the TOML configuration file (mode 0600)")
	var out strings.Builder
	writeUsage(&out, fs)
	for _, want := range []string{"wanbond monitor", "wanbond set-exit", "<exit-peer>|auto", "wanbond version", "-config"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage missing %q:\n%s", want, out.String())
		}
	}
}

func TestHelpRequestsSucceed(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		if err := run(args); err != nil {
			t.Errorf("run(%q) = %v, want nil", args, err)
		}
	}
	// Subcommand help surfaces flag.ErrHelp, which main treats as success.
	for _, args := range [][]string{{"monitor", "--help"}, {"set-exit", "--help"}} {
		if err := run(args); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("run(%q) = %v, want flag.ErrHelp", args, err)
		}
	}
}

func TestSetExitRequiresOnePeer(t *testing.T) {
	for _, args := range [][]string{{}, {"a", "b"}} {
		if err := runSetExit(args, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "exactly one argument") {
			t.Errorf("runSetExit(%q) = %v, want argument-count error", args, err)
		}
	}
}

func TestPostExitSendsWebUIRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/exit" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var req exitSwitchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Peer != "o2" {
			t.Errorf("body peer = %q, err %v; want o2", req.Peer, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(exitSwitchResponse{ActiveExit: "o2", ExitMode: "o2"})
	}))
	defer srv.Close()
	var out strings.Builder
	if err := postExit(testContext(t), testEndpoint(srv, "secret"), "o2", &out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "exit policy o2, active exit o2\n"; got != want {
		t.Errorf("output %q, want %q", got, want)
	}
}

func TestPostExitAgainstMonitorServer(t *testing.T) {
	lg, err := log.New("error", &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	var selected string
	switcher := func(peer string) (string, error) {
		if peer != "auto" && peer != "o2" {
			return "", monitor.ErrUnknownExitPeer
		}
		selected = peer
		return "o2", nil
	}
	srv, err := monitor.NewServer("127.0.0.1:0", "secret", nil, monitor.Info{}, switcher, false, lg)
	if err != nil {
		t.Fatal(err)
	}
	srv.Start()
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	endpoint := monitorEndpoint{addr: srv.Addr().String(), token: "secret"}

	var out strings.Builder
	if err := postExit(testContext(t), endpoint, "auto", &out); err != nil {
		t.Fatal(err)
	}
	if selected != "auto" || out.String() != "exit policy auto, active exit o2\n" {
		t.Errorf("selected %q, output %q", selected, out.String())
	}
	if err := postExit(testContext(t), endpoint, "nope", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), `unknown or non-exit-capable peer: "nope"`) {
		t.Errorf("unknown peer: got %v", err)
	}
	endpoint.token = "wrong"
	if err := postExit(testContext(t), endpoint, "o2", &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Errorf("wrong token: got %v", err)
	}
}

func TestPostExitReportsRejection(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
		status                        int
	}{
		{name: "unknown peer", status: http.StatusBadRequest, contentType: "application/json",
			body: `{"error":"unknown or non-exit-capable peer: \"nope\""}`, want: `400 Bad Request: unknown or non-exit-capable peer: "nope"`},
		{name: "bad token", status: http.StatusUnauthorized, contentType: "text/plain", body: "unauthorized\n", want: "401 Unauthorized: unauthorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			err := postExit(testContext(t), testEndpoint(srv, ""), "nope", &strings.Builder{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func testEndpoint(srv *httptest.Server, token string) monitorEndpoint {
	return monitorEndpoint{addr: strings.TrimPrefix(srv.URL, "http://"), token: token}
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
