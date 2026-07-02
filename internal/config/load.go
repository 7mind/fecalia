package config

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// requiredMode is the exact permission bits the config file must carry. The file
// holds the WireGuard private key and the outer-control PSK, so it must not be
// readable by group or others.
const requiredMode fs.FileMode = 0o600

// Load reads, validates, and returns the configuration at path. It fails fast:
// the file must have mode 0600 exactly, must be valid TOML, and must satisfy the
// required-field invariants.
func Load(path string) (*Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if perm := info.Mode().Perm(); perm != requiredMode {
		return nil, fmt.Errorf("config %s: insecure permissions %#o, must be %#o (holds private key and PSK)", path, perm, requiredMode)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	var c Config
	if err := toml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}
