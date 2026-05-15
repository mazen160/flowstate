//go:build !windows

package config

import "os"

// applyConfigPerms tightens permissions on the freshly-written config file.
// On Unix the config holds a plaintext API key, so we restrict it to 0600
// (owner read/write only).
func applyConfigPerms(path string) error {
	return os.Chmod(path, 0o600)
}
