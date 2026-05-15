//go:build linux

package mute

import (
	"errors"
	"reflect"
	"testing"
)

// stubLookPath returns a function whose answers match the given set: a
// name in `found` resolves to "/usr/bin/<name>", everything else maps
// to a "not found" error. This mirrors the helper used by the paste
// package's tests so the two stay consistent.
func stubLookPath(found map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if found[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}

// TestChooseMuteCmd_PactlPreferred verifies that when pactl is on PATH
// we pick it even if amixer is also present, because pactl handles
// PulseAudio/PipeWire which is the modern default on most distros.
func TestChooseMuteCmd_PactlPreferred(t *testing.T) {
	bin, muteArgs, unmuteArgs, err := chooseMuteCmd(
		stubLookPath(map[string]bool{"pactl": true, "amixer": true}),
	)
	if err != nil {
		t.Fatalf("chooseMuteCmd returned unexpected error: %v", err)
	}
	if bin != "pactl" {
		t.Fatalf("chooseMuteCmd returned binary=%q, want pactl", bin)
	}
	want := muteCommands["pactl"]
	if !reflect.DeepEqual(muteArgs, want.mute) {
		t.Fatalf("muteArgs = %v, want %v", muteArgs, want.mute)
	}
	if !reflect.DeepEqual(unmuteArgs, want.unmute) {
		t.Fatalf("unmuteArgs = %v, want %v", unmuteArgs, want.unmute)
	}
}

// TestChooseMuteCmd_FallbackToAmixer covers the ALSA-only host: pactl
// missing, amixer present. Should pick amixer rather than erroring.
func TestChooseMuteCmd_FallbackToAmixer(t *testing.T) {
	bin, muteArgs, unmuteArgs, err := chooseMuteCmd(
		stubLookPath(map[string]bool{"amixer": true}),
	)
	if err != nil {
		t.Fatalf("chooseMuteCmd returned unexpected error: %v", err)
	}
	if bin != "amixer" {
		t.Fatalf("chooseMuteCmd returned binary=%q, want amixer", bin)
	}
	want := muteCommands["amixer"]
	if !reflect.DeepEqual(muteArgs, want.mute) {
		t.Fatalf("muteArgs = %v, want %v", muteArgs, want.mute)
	}
	if !reflect.DeepEqual(unmuteArgs, want.unmute) {
		t.Fatalf("unmuteArgs = %v, want %v", unmuteArgs, want.unmute)
	}
}

// TestChooseMuteCmd_NeitherAvailable confirms that when neither tool
// is on PATH we return the sentinel error so the caller can log and
// swallow without triggering an exec.
func TestChooseMuteCmd_NeitherAvailable(t *testing.T) {
	bin, muteArgs, unmuteArgs, err := chooseMuteCmd(
		stubLookPath(map[string]bool{}),
	)
	if err == nil {
		t.Fatalf("chooseMuteCmd returned nil error; want errNoMuteTool")
	}
	if !errors.Is(err, errNoMuteTool) {
		t.Fatalf("chooseMuteCmd returned %v; want errNoMuteTool", err)
	}
	if bin != "" {
		t.Fatalf("expected empty binary on error path, got %q", bin)
	}
	if muteArgs != nil || unmuteArgs != nil {
		t.Fatalf("expected nil args on error path, got mute=%v unmute=%v", muteArgs, unmuteArgs)
	}
}
