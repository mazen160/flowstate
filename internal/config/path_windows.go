//go:build windows

package config

import (
	"os"
	"path/filepath"
)

// DefaultPath returns the Windows default config path: %APPDATA%\flowstate\config.toml.
// If %APPDATA% is unset, falls back to the user's home directory.
func DefaultPath() string {
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		return filepath.Join(appdata, "flowstate", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "AppData", "Roaming", "flowstate", "config.toml")
	}
	return filepath.Join("flowstate", "config.toml")
}
