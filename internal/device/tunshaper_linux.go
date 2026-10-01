//go:build linux

package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const tcCommandTimeout = 3 * time.Second

func findTCBinary() (string, error) {
	if path, err := exec.LookPath("tc"); err == nil {
		return path, nil
	}
	for _, path := range []string{"/usr/sbin/tc", "/sbin/tc"} {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", errors.New("device: tc binary is required to inspect the TUN queue discipline")
}

func runTC(tc string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tcCommandTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, tc, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("device: tc %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// removeObsoleteTUNShaper deletes the rate cap an earlier wanbond installed on
// the TUN: an HTB root 1: with a bfifo 10: leaf under class 1:1. A persistent
// interface keeps its queue discipline across daemon restarts and upgrades, so
// the cap would outlive the pacing that needed it. Any other queue discipline
// is the operator's and is left alone.
func (t *Tunnel) removeObsoleteTUNShaper() error {
	tc, err := findTCBinary()
	if err != nil {
		return err
	}
	raw, err := runTC(tc, "-j", "qdisc", "show", "dev", t.name)
	if err != nil {
		return fmt.Errorf("device: remove obsolete TUN shaper: %w", err)
	}
	var qdiscs []struct {
		Kind   string `json:"kind"`
		Handle string `json:"handle"`
		Parent string `json:"parent"`
		Root   bool   `json:"root"`
	}
	if err := json.Unmarshal(raw, &qdiscs); err != nil {
		return fmt.Errorf("device: read previous TUN shaper: %w", err)
	}
	root, leaf := false, false
	for _, qdisc := range qdiscs {
		root = root || (qdisc.Root && qdisc.Kind == "htb" && qdisc.Handle == "1:")
		leaf = leaf || (qdisc.Parent == "1:1" && qdisc.Kind == "bfifo" && qdisc.Handle == "10:")
	}
	if !root || !leaf {
		return nil
	}
	if _, err := runTC(tc, "qdisc", "delete", "dev", t.name, "root"); err != nil {
		return fmt.Errorf("device: remove obsolete TUN shaper: %w", err)
	}
	t.log.Info("removed obsolete TUN shaper", "interface", t.name)
	return nil
}
