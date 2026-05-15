package paste

import (
	"errors"
	"testing"
)

// TestPasteFunctionExists is a build-tag-agnostic smoke test: the package
// must compile with a Paste symbol on every supported platform. We never
// invoke Paste() in tests — it would synthesize a Cmd+V/Ctrl+V keystroke
// into whatever window happens to have focus when the test runner runs,
// which is at best disruptive and at worst destructive.
func TestPasteFunctionExists(t *testing.T) {
	// Take the address of the function to prove it resolves at link time.
	// If Paste weren't declared on this platform, this file wouldn't
	// compile.
	var fn = Paste
	if fn == nil {
		t.Fatal("Paste should be a non-nil function value")
	}
}

// stubLookPath returns a function whose answers match the given set: a
// command name maps to "found at /usr/bin/<name>", everything else maps to
// exec.ErrNotFound.
func stubLookPath(found map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if found[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
}
