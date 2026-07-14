package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNetworkManagerDropIn verifies that the shipped NetworkManager drop-in
// configuration file exists and contains the required unmanaged-devices directive
// for wanbond0 interface.
func TestNetworkManagerDropIn(t *testing.T) {
	// Find the repo root by walking up from this file's location
	repoRoot := findRepoRoot(t)

	nmPath := filepath.Join(repoRoot, "packaging", "networkmanager", "99-wanbond-unmanaged.conf")

	// Verify the file exists
	file, err := os.Open(nmPath)
	if err != nil {
		t.Fatalf("NetworkManager drop-in file not found at %s: %v", nmPath, err)
	}
	defer file.Close()

	// Verify the file contains the required [keyfile] section and unmanaged-devices directive
	scanner := bufio.NewScanner(file)
	foundKeyfile := false
	foundDirective := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "[keyfile]" {
			foundKeyfile = true
		}
		if line == "unmanaged-devices=interface-name:wanbond0" {
			foundDirective = true
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading NetworkManager drop-in file: %v", err)
	}

	if !foundKeyfile {
		t.Fatal("NetworkManager drop-in file does not contain '[keyfile]' section")
	}
	if !foundDirective {
		t.Fatal("NetworkManager drop-in file does not contain exact directive 'unmanaged-devices=interface-name:wanbond0'")
	}
}

// TestWanbondAddressingOneshotUnit verifies that the shipped templated
// addressing oneshot unit exists and orders itself after interface
// EXISTENCE, not merely after execve() returning (the R27 lesson: a plain
// ExecStartPost under Type=exec races wanbond0's creation). See
// docs/install.md §4 for the persistence recipe this unit implements.
func TestWanbondAddressingOneshotUnit(t *testing.T) {
	repoRoot := findRepoRoot(t)

	unitPath := filepath.Join(repoRoot, "packaging", "systemd", "wanbond-addressing@.service")

	data, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("wanbond-addressing@.service not found at %s: %v", unitPath, err)
	}
	content := string(data)

	// Templated instance = role, coupled to wanbond-<role>.service.
	for _, want := range []string{
		"PartOf=wanbond-%i.service",
		"After=wanbond-%i.service",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("wanbond-addressing@.service missing %q", want)
		}
	}

	// The unit must NOT rely on unit-start ordering alone (After= only
	// orders after execve() returns under Type=exec, per R27) — it must
	// actively wait for the wanbond0 interface to exist before running.
	if !strings.Contains(content, "ExecStartPre=") {
		t.Fatal("wanbond-addressing@.service has no ExecStartPre= interface-wait guard")
	}
	if !strings.Contains(content, "/sys/class/net/wanbond0") {
		t.Error("wanbond-addressing@.service does not poll for wanbond0's existence (/sys/class/net/wanbond0)")
	}

	// A bare ExecStartPost directive (the exact race R27 fixed) must not
	// reappear as an active unit key — only check actual key=value lines,
	// not the file's own explanatory comments about the race.
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "ExecStartPost=") {
			t.Error("wanbond-addressing@.service sets ExecStartPost=, which races tun creation under Type=exec (R27) — use the ExecStartPre interface-wait guard instead")
		}
	}

	if !strings.Contains(content, "Type=oneshot") {
		t.Error("wanbond-addressing@.service is not Type=oneshot")
	}
	if !strings.Contains(content, "RemainAfterExit=yes") {
		t.Error("wanbond-addressing@.service is missing RemainAfterExit=yes")
	}
}

// findRepoRoot walks up the directory tree to find the repository root
// (the directory containing a go.mod file).
func findRepoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Cannot get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding go.mod
			t.Fatal("Could not find repository root (no go.mod found)")
		}
		dir = parent
	}
}
