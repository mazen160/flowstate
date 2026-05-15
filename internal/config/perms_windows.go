//go:build windows

package config

// applyConfigPerms is a no-op on Windows. The Unix-style 0600 chmod doesn't
// map cleanly to NTFS ACLs, and adding cgo just to set per-user ACLs is more
// risk than reward for v1. See the Config Specification doc, "Secrets".
func applyConfigPerms(path string) error {
	_ = path
	return nil
}
