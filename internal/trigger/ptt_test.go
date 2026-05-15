package trigger

import (
	"strings"
	"testing"
)

// TestResolveKeyName is a table-driven test of the user-facing key-name
// normalization. The Flowstate config field ptt_key is free-form text, so
// we accept common casings and surrounding whitespace and normalize to the
// lowercase token gohook expects.
func TestResolveKeyName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"space", "space"},
		{"Space", "space"},
		{"SPACE", "space"},
		{" space ", "space"},
		{"\tSpace\n", "space"},
		{"f12", "f12"},
		{"F12", "f12"},
		{"a", "a"},
		{"A", "a"},
		{"fn", "fn"}, // passes through; not in gohook's table
		{"", ""},
		{"   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := resolveKeyName(tc.in); got != tc.want {
				t.Fatalf("resolveKeyName(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsKnownKey verifies the construction-time validation rejects names
// that don't appear in gohook's keycode table. The exact mapping comes from
// upstream (github.com/vcaesar/keycode), so we limit this test to a few
// stable entries.
func TestIsKnownKey(t *testing.T) {
	known := []string{"space", "Space", "f12", "a", "enter", "shift"}
	for _, k := range known {
		if !isKnownKey(k) {
			t.Errorf("isKnownKey(%q) = false; want true", k)
		}
	}

	unknown := []string{"", "fn", "definitely-not-a-key", "f99"}
	for _, k := range unknown {
		if isKnownKey(k) {
			t.Errorf("isKnownKey(%q) = true; want false", k)
		}
	}
}

// TestNewPushToTalkTrigger_RejectsUnknownKey verifies the friendly error
// path. The message must reference the trigger=enter fallback so users
// can recover without hunting through the codebase.
func TestNewPushToTalkTrigger_RejectsUnknownKey(t *testing.T) {
	_, err := NewPushToTalkTrigger("definitely-not-a-key")
	if err == nil {
		t.Fatalf("NewPushToTalkTrigger returned nil error for unknown key; want error")
	}
	if !strings.Contains(err.Error(), `trigger="enter"`) {
		t.Fatalf("error %q does not mention the trigger=\"enter\" fallback", err.Error())
	}
}

// TestNewPushToTalkTrigger_AcceptsKnownKey verifies construction succeeds
// without registering a global hook (registration is deferred to Run). We
// don't call Run because driving real key events from a test would require
// privileges and a graphical session; the construction path is the only
// piece we can exercise hermetically.
func TestNewPushToTalkTrigger_AcceptsKnownKey(t *testing.T) {
	tr, err := NewPushToTalkTrigger("space")
	if err != nil {
		t.Fatalf("NewPushToTalkTrigger(space) returned %v; want nil", err)
	}
	if tr == nil {
		t.Fatalf("NewPushToTalkTrigger(space) returned nil trigger")
	}

	// Close is safe to call on a never-Run trigger because it short-
	// circuits on the closed flag (which is set by either Run's deferred
	// cleanup or an explicit Close).
	if err := tr.Close(); err != nil {
		t.Fatalf("Close returned %v; want nil", err)
	}
}
