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
			Paths: []monitor.PathSnapshot{{Peer: "raspi5l", Name: "starlink", Up: true, RTTSeconds: 0.08, ThroughputBps: 8192}},
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
	if err := streamMonitor(ctx, strings.TrimPrefix(srv.URL, "http://"), "secret", true, false, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"edge", "Exit       raspi5l    Policy auto", "raspi5l / starlink", "80.0ms", "rate 1.0KiB/s"} {
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
	}, time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), true, false)
	for _, want := range []string{"concentrator", "pi-mo", "wan0", "Ctrl+C to quit"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Policy") {
		t.Errorf("concentrator has no exit policy:\n%s", output)
	}
}

func TestMonitorColorSelection(t *testing.T) {
	for _, tc := range []struct {
		name, term                              string
		terminal, noColorFlag, noColorEnv, want bool
	}{
		{name: "terminal", term: "xterm-256color", terminal: true, want: true},
		{name: "redirected", term: "xterm-256color"},
		{name: "dumb terminal", term: "dumb", terminal: true},
		{name: "missing term", terminal: true},
		{name: "flag", term: "xterm-256color", terminal: true, noColorFlag: true},
		{name: "environment", term: "xterm-256color", terminal: true, noColorEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := monitorColorEnabled(tc.terminal, tc.term, tc.noColorFlag, tc.noColorEnv); got != tc.want {
				t.Fatalf("monitorColorEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMonitorRenderColorsStatesWithoutChangingPlainOutput(t *testing.T) {
	snapshot := monitor.MonitorSnapshot{
		Daemon:  monitor.DaemonSnapshot{Role: "edge", Version: "test"},
		Session: monitor.SessionSnapshot{Established: true},
		Paths: []monitor.PathSnapshot{
			{Peer: "pi-mo", Name: "wan0", Up: true},
			{Peer: "pi-mo", Name: "wan1"},
		},
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	plain := renderMonitor(snapshot, now, true, false)
	colored := renderMonitor(snapshot, now, true, true)
	if strings.Contains(plain, "\x1b[") {
		t.Fatalf("plain output contains color controls: %q", plain)
	}
	for _, want := range []string{"\x1b[32mUP", "\x1b[31mDOWN", "\x1b[1;36mPATHS\x1b[0m"} {
		if !strings.Contains(colored, want) {
			t.Errorf("colored output missing %q:\n%s", want, colored)
		}
	}
	if got := stripMonitorColor(colored); got != plain {
		t.Errorf("color changed plain content:\n got %q\nwant %q", got, plain)
	}
}

func stripMonitorColor(s string) string {
	for _, sequence := range []string{"\x1b[1;36m", "\x1b[32m", "\x1b[31m", "\x1b[33m", "\x1b[0m"} {
		s = strings.ReplaceAll(s, sequence, "")
	}
	return s
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

func TestMonitorRendersLanesAndTransportQueue(t *testing.T) {
	output := renderMonitor(monitor.MonitorSnapshot{
		Daemon: monitor.DaemonSnapshot{Role: "edge", Version: "test"},
		Lanes: []monitor.LaneSnapshot{
			{Peer: "hub", Path: "5g", RemotePath: 0, Lane: 256, Up: true, TargetBps: 1000000, SendBps: 800000, DeliveryBps: 720000, CapacityBps: 1040000,
				RTTSeconds: 0.06, QueueDelaySeconds: 0.037, ThresholdSeconds: 0.03, InFlightBytes: 5000, WindowBytes: 30000, Repairs: 12,
				DelaySignals: 21, LossSignals: 22, Pulses: 27, PulseWins: 28, PulseLosses: 29, CapacityRemeasured: 25, CapacityDecays: 26},
			{Path: "starlink", RemotePath: 1, Up: true, Discovering: true, TargetBps: 1000000},
			{Path: "lte", Up: false},
		},
		Transport: []monitor.TransportSnapshot{{Peer: "hub", QueueDrops: 9, AdmissionDrops: 1, AQMDrops: 2, InteractiveDrops: 3, Expired: 5, Duplicates: 6}},
	}, time.Unix(0, 0), false, false)
	for _, want := range []string{
		"LANES",
		"hub / 5g #0                 HOLD    122.1KiB/s  97.7KiB/s   127.0KiB/s",
		"delivered 87.9KiB/s   queue 37ms of 30ms  rtt 60ms  in flight 4.9KiB of 29.3KiB",
		"signals delay 21 loss 22  probes 27 won 28 lost 29  estimate remeasured 25 decayed 26  repairs 12",
		"starlink #1                 PROBING 122.1KiB/s  0B/s        unknown",
		"lte #0                      DOWN",
		"TRANSPORT QUEUE",
		"hub                dropped 9 (full 1, aqm 2, small 3)  expired 5  duplicates 6",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("monitor output lacks %q:\n%s", want, output)
		}
	}
}
