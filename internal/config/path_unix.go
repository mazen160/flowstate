//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

// DefaultPath returns the macOS/Linux default config path. It follows the
// XDG Base Directory spec: $XDG_CONFIG_HOME/flowstate/config.toml, falling
// back to $HOME/.config/flowstate/config.toml when XDG is unset.
//
// If neither $XDG_CONFIG_HOME nor $HOME is set (e.g. exotic init scripts),
// DefaultPath returns a relative path under .config/. This is intentional:
// returning an empty string would let later callers create a file in the
// process CWD, which is worse.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "flowstate", "config.toml")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "flowstate", "config.toml")
	}
	return filepath.Join(".config", "flowstate", "config.toml")
}
