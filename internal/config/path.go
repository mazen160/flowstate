package config

import "os"

// EnvVar is the environment variable that, when set, overrides [DefaultPath]
// but is itself overridden by an explicit CLI flag.
const EnvVar = "FLOWSTATE_CONFIG"

// ResolvePath returns the config file path using the spec's priority order:
//
//  1. explicit (non-empty) — typically passed via `--config`.
//  2. $FLOWSTATE_CONFIG.
//  3. DefaultPath() — platform-specific.
func ResolvePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv(EnvVar); env != "" {
		return env
	}
	return DefaultPath()
}
