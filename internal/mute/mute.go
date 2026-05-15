// Package mute toggles the system audio output mute as a best-effort
// side effect of recording. Recording should never abort because mute
// failed; failures are logged to stderr and swallowed.
//
// Per-OS strategies live in build-tagged files:
//
//   - macOS:   osascript -e "set volume output muted true/false"
//   - Linux:   pactl set-sink-mute @DEFAULT_SINK@ 1/0, falling back to
//     amixer -q set Master mute/unmute.
//   - Windows: PowerShell + WScript.Shell sending VK_VOLUME_MUTE (173),
//     which is a toggle, so we track our own state.
//
// The package keeps a single bookkeeping flag, mutedByUs, so Unmute only
// flips the system back when this process was the one that muted it.
// The flag is updated even when the underlying tool call fails or is
// unavailable, because the wrapper is best-effort: we treat "we asked
// the OS to mute" as having muted from the package's point of view.
package mute

import (
	"sync"
)

var (
	muteMu    sync.Mutex
	mutedByUs bool
)

// Mute mutes the system audio output. Best-effort: if no supported tool
// is available or the underlying tool fails, a warning is logged to
// stderr and nil is returned. The bookkeeping flag is updated regardless
// so that a paired Unmute call can still attempt to flip the system
// back.
//
// Mute is idempotent: a second call while already muted is a no-op.
func Mute() error {
	muteMu.Lock()
	defer muteMu.Unlock()
	if mutedByUs {
		return nil
	}
	// doMute is best-effort — it logs and returns nil on
	// missing-tool / tool-failure paths. We always flip the flag so
	// the caller's later Unmute() reaches the platform code.
	_ = doMute()
	mutedByUs = true
	return nil
}

// Unmute reverses a prior Mute call. No-op if Mute was never called or
// the package has already been unmuted. Like Mute, this is best-effort
// and never propagates an error.
func Unmute() error {
	muteMu.Lock()
	defer muteMu.Unlock()
	if !mutedByUs {
		return nil
	}
	_ = doUnmute()
	mutedByUs = false
	return nil
}

// IsMutedByUs reports whether the package currently believes it has
// muted the system. Primarily used in tests.
func IsMutedByUs() bool {
	muteMu.Lock()
	defer muteMu.Unlock()
	return mutedByUs
}
