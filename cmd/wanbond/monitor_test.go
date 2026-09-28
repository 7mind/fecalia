package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/7mind/wanbond/internal/monitor"
)

func TestMonitorOnceReadsAuthenticatedSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" || r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer conn.CloseNow()
		body, err := json.Marshal(monitor.MonitorSnapshot{
			Daemon:   monitor.DaemonSnapshot{Role: "edge", Version: "test"},
			Session:  monitor.SessionSnapshot{Established: true},
			ExitMode: "auto", ActiveExit: "raspi5l", ExitCapablePeers: []string{"raspi5l", "o2"},
			Paths: []monitor.PathSnapshot{{Peer: "raspi5l", Name: "starlink", Up: true, RTTSeconds: 0.08}},
		})
		if err != nil {
			t.Errorf("marshal: %v", err)
			return
		}
		if err := conn.Write(r.Context(), websocket.MessageText, body); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()
	var out strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := streamMonitor(ctx, strings.TrimPrefix(srv.URL, "http://"), "secret", true, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edge", "Exit       raspi5l    Policy auto", "raspi5l / starlink", "80.0ms"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("--once output contains terminal controls: %q", out.String())
	}
}

func TestConcentratorViewDoesNotShowExitPolicy(t *testing.T) {
	output := renderMonitor(monitor.MonitorSnapshot{
		Daemon:       monitor.DaemonSnapshot{Role: "concentrator", Version: "test"},
		PeerSessions: []monitor.PeerSessionSnapshot{{Peer: "pi-mo", Established: true}},
		Paths:        []monitor.PathSnapshot{{Peer: "pi-mo", Name: "wan0", Up: true}},
	}, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), true)
	for _, want := range []string{"concentrator", "pi-mo", "wan0", "Ctrl+C to quit"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Policy") {
		t.Errorf("concentrator has no exit policy:\n%s", output)
	}
}

func TestFindMonitorConfigRejectsAmbiguousHosts(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "edge.toml")
	b := filepath.Join(dir, "concentrator.toml")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := findMonitorConfig([]string{a, b}); err == nil || !strings.Contains(err.Error(), "multiple configs") {
		t.Fatalf("got %v, want ambiguity error", err)
	}
}
