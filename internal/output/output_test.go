package output

import (
	"bytes"
	"strings"
	"testing"
)

// TestWrite_StdoutOnly verifies the simplest path: with only Stdout
// enabled, the cleaned text appears on the substituted writer followed
// by a newline, and no error is returned.
func TestWrite_StdoutOnly(t *testing.T) {
	var buf bytes.Buffer
	err := WriteWithStdout("hello", Destinations{Stdout: true}, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := buf.String()
	if got != "hello\n" {
		t.Errorf("expected %q, got %q", "hello\n", got)
	}
}

// TestWrite_ClipboardOnly_FailsGracefully: the clipboard library needs
// cgo + a display server to initialize, neither of which the test
// runner reliably has. We accept either outcome: success (nil) or a
// wrapped "clipboard unavailable" error. The point is that the call
// returns without panicking and surfaces a recognizable error format.
func TestWrite_ClipboardOnly_FailsGracefully(t *testing.T) {
	var buf bytes.Buffer
	err := WriteWithStdout("hello", Destinations{Clipboard: true}, &buf)
	if err != nil && !strings.Contains(err.Error(), "clipboard unavailable") {
		t.Errorf(
			"expected nil or an error containing %q, got: %v",
			"clipboard unavailable", err,
		)
	}
	// Stdout was disabled, so the buffer must be untouched. This catches
	// accidental cross-wiring between destinations.
	if buf.Len() != 0 {
		t.Errorf("stdout should be untouched, but buffer contained %q", buf.String())
	}
}

// TestWrite_NothingEnabled: an all-false Destinations is a legal config
// (e.g., the user disabled every output and is just stress-testing the
// pipeline). Should be a clean no-op.
func TestWrite_NothingEnabled(t *testing.T) {
	var buf bytes.Buffer
	err := WriteWithStdout("hello", Destinations{}, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("buffer should be empty, got %q", buf.String())
	}
}

// TestWrite_StdoutAndOther_StdoutStillWrites is the critical contract
// for best-effort dispatch: a clipboard or paste failure must NOT
// suppress the stdout write. Even if the returned error is non-nil
// (because clipboard couldn't init), the user still sees their text on
// stdout.
func TestWrite_StdoutAndOther_StdoutStillWrites(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteWithStdout(
		"hello",
		Destinations{Stdout: true, Clipboard: true},
		&buf,
	)
	// We deliberately ignore the returned error: on some test hosts the
	// clipboard initializes fine and the call succeeds; on others it
	// fails. Either way, stdout must have received the text.
	if buf.String() != "hello\n" {
		t.Errorf(
			"stdout must receive text regardless of clipboard outcome; got %q",
			buf.String(),
		)
	}
}
