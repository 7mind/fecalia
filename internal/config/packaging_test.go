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

	// Verify the file contains the required unmanaged-devices key
	scanner := bufio.NewScanner(file)
	found := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(line, "unmanaged-devices") && strings.Contains(line, "interface-name:wanbond0") {
			found = true
			break
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading NetworkManager drop-in file: %v", err)
	}

	if !found {
		t.Fatal("NetworkManager drop-in file does not contain 'unmanaged-devices=interface-name:wanbond0'")
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
