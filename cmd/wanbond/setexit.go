package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	setExitTimeout = 5 * time.Second
	// setExitMaxErrorBody bounds how much of a rejection body is echoed.
	setExitMaxErrorBody = 4 << 10
)

const setExitUsage = `Usage: wanbond set-exit [--config PATH] <exit-peer>|auto

Select a fixed exit peer, or return to RTT-driven auto selection, on the
running edge. The override is runtime-only: a daemon restart restores the
config's top-level ` + "`exit`" + ` setting.

`

// exitSwitchRequest and exitSwitchResponse mirror the monitor's POST /api/exit
// wire contract (monitor.exitRequest / monitor.exitResponse).
type exitSwitchRequest struct {
	Peer string `json:"peer"`
}

type exitSwitchResponse struct {
	ActiveExit string `json:"activeExit"`
	ExitMode   string `json:"exitMode"`
}

type exitSwitchError struct {
	Error string `json:"error"`
}

func runSetExit(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("wanbond set-exit", flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), setExitUsage)
		fs.PrintDefaults()
	}
	configPath := fs.String("config", "", "path to the running daemon's TOML config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("set-exit: expected exactly one argument (<exit-peer> or auto), got %d", fs.NArg())
	}
	endpoint, err := resolveMonitorEndpoint(*configPath)
	if err != nil {
		return fmt.Errorf("set-exit: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), setExitTimeout)
	defer cancel()
	return postExit(ctx, endpoint, fs.Arg(0), out)
}

// postExit issues the same POST /api/exit the web UI's exit selector sends.
func postExit(ctx context.Context, endpoint monitorEndpoint, peer string, out io.Writer) error {
	body, err := json.Marshal(exitSwitchRequest{Peer: peer})
	if err != nil {
		return fmt.Errorf("set-exit: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+endpoint.addr+"/api/exit", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("set-exit: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if endpoint.token != "" {
		req.Header.Set("Authorization", "Bearer "+endpoint.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("set-exit: connect to %s: %w", endpoint.addr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("set-exit: daemon rejected %q: %s: %s", peer, resp.Status, rejectionReason(resp.Body))
	}
	var result exitSwitchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("set-exit: decode response: %w", err)
	}
	_, err = fmt.Fprintf(out, "exit policy %s, active exit %s\n", result.ExitMode, result.ActiveExit)
	return err
}

// rejectionReason extracts the {"error": ...} message of an /api/exit
// rejection, falling back to the raw text the auth middleware writes.
func rejectionReason(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, setExitMaxErrorBody))
	if err != nil {
		return fmt.Sprintf("read response: %v", err)
	}
	var e exitSwitchError
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		return e.Error
	}
	return strings.TrimSpace(string(raw))
}
