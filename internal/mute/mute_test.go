package mute

import (
	"testing"
)

// resetState forces the package-level flag back to false so each test
// starts from a known position. We touch mutedByUs directly (under the
// same mutex the production code uses) rather than calling Unmute(),
// because Unmute() also tries to invoke the platform tool — which may
// not exist in the test environment.
func resetState(t *testing.T) {
	t.Helper()
	muteMu.Lock()
	mutedByUs = false
	muteMu.Unlock()
}

// TestIsMutedByUs_StartsFalse confirms that before any Mute call the
// package reports it has not muted the system. This guards against
// accidental package-init side effects.
func TestIsMutedByUs_StartsFalse(t *testing.T) {
	resetState(t)
	if IsMutedByUs() {
		t.Fatalf("IsMutedByUs() = true before any Mute; want false")
	}
}

// TestMute_SetsFlag exercises the round trip Mute → IsMutedByUs → Unmute.
// We assert on the bookkeeping flag, NOT on the actual system mute
// state, because the underlying tool may legitimately fail in the test
// environment (e.g. osascript denied Accessibility, PowerShell absent
// inside a container, neither pactl nor amixer installed). The mute
// wrapper is defined to update the flag regardless of the tool's exit
// status, and that contract is what this test pins down.
func TestMute_SetsFlag(t *testing.T) {
	resetState(t)

	if err := Mute(); err != nil {
		t.Fatalf("Mute() returned error: %v (best-effort wrapper must swallow tool failures)", err)
	}
	if !IsMutedByUs() {
		t.Fatalf("after Mute() IsMutedByUs() = false; want true even if tool failed")
	}

	if err := Unmute(); err != nil {
		t.Fatalf("Unmute() returned error: %v (best-effort wrapper must swallow tool failures)", err)
	}
	if IsMutedByUs() {
		t.Fatalf("after Unmute() IsMutedByUs() = true; want false")
	}
}

// TestMute_Idempotent ensures consecutive Mute calls do not re-invoke
// the tool path in a way that produces an error, and that the flag
// remains true throughout.
func TestMute_Idempotent(t *testing.T) {
	resetState(t)

	if err := Mute(); err != nil {
		t.Fatalf("first Mute() returned error: %v", err)
	}
	if err := Mute(); err != nil {
		t.Fatalf("second Mute() returned error: %v", err)
	}
	if !IsMutedByUs() {
		t.Fatalf("after two Mute() calls IsMutedByUs() = false; want true")
	}

	// Clean up so other tests aren't affected by lingering state.
	_ = Unmute()
}

// TestUnmute_WithoutMute_NoOp checks that calling Unmute when the
// package has not muted is harmless. This matters on Windows in
// particular: Unmute toggles a single key, so a stray invocation
// without a matching Mute would flip whatever the user currently has.
// The wrapper guards against that by skipping doUnmute when the flag
// is already false.
func TestUnmute_WithoutMute_NoOp(t *testing.T) {
	resetState(t)

	if err := Unmute(); err != nil {
		t.Fatalf("Unmute() before Mute() returned error: %v", err)
	}
	if IsMutedByUs() {
		t.Fatalf("Unmute() before Mute() flipped IsMutedByUs() to true; want false")
	}
}
