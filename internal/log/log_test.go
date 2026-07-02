package log

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// lastRecord parses the final JSON log line written to buf.
func lastRecord(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		t.Fatalf("no log output")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &rec); err != nil {
		t.Fatalf("log line is not JSON: %v (line=%q)", err, lines[len(lines)-1])
	}
	return rec
}

func TestComponentAndPathFields(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New("info", &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Component("scheduler").Path("starlink").Info("path down", "loss", 0.12)

	rec := lastRecord(t, &buf)
	if rec[FieldComponent] != "scheduler" {
		t.Errorf("%s = %v, want scheduler", FieldComponent, rec[FieldComponent])
	}
	if rec[FieldPath] != "starlink" {
		t.Errorf("%s = %v, want starlink", FieldPath, rec[FieldPath])
	}
	if rec["msg"] != "path down" {
		t.Errorf("msg = %v, want 'path down'", rec["msg"])
	}
	if rec["loss"] != 0.12 {
		t.Errorf("loss = %v, want 0.12", rec["loss"])
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New("warn", &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("suppressed")
	if buf.Len() != 0 {
		t.Fatalf("info record emitted at warn level: %q", buf.String())
	}
	lg.Warn("kept")
	if rec := lastRecord(t, &buf); rec["msg"] != "kept" {
		t.Errorf("msg = %v, want kept", rec["msg"])
	}
}

func TestUnknownLevelRejected(t *testing.T) {
	if _, err := New("verbose", &bytes.Buffer{}); err == nil {
		t.Fatal("expected error for unknown level, got nil")
	}
}

func TestEmptyLevelDefaultsInfo(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New("", &buf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	lg.Info("hello")
	if rec := lastRecord(t, &buf); rec["msg"] != "hello" {
		t.Errorf("empty level should default to info and emit; got %q", buf.String())
	}
}
