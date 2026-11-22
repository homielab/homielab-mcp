// Package appdir locates the configuration directory shared by every service:
// $MCP_HOMIELAB_CONFIG_DIR, or <user config dir>/mcp-homielab.
package appdir

import (
	"os"
	"path/filepath"
)

// Name is the binary and config dir name.
const Name = "mcp-homielab"

// EnvConfigDir overrides the config dir.
const EnvConfigDir = "MCP_HOMIELAB_CONFIG_DIR"

// Base returns the config dir that holds one subdirectory per service.
func Base() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, Name), nil
}
