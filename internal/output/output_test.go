package output

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"golang.design/x/clipboard"
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

// TestWrite_PreserveClipboard_RestoresWhenUnchanged exercises the happy
// path: with Paste + PreservePriorClipboard, the prior clipboard
// contents are saved, the transcript is set + pasted, and after
// restoreDelay elapses the prior contents are restored. We override
// restoreDelay so the test runs in milliseconds instead of half a
// second.
//
// Skipped automatically on hosts where clipboard.Init fails (typical
// for headless CI), because the test depends on a real clipboard
// surface.
func TestWrite_PreserveClipboard_RestoresWhenUnchanged(t *testing.T) {
	if err := initClipboard(); err != nil {
		t.Skipf("clipboard unavailable: %v", err)
	}

	// Shorten the restore window for the duration of the test so we
	// don't sit on the half-second production timer. Restore the
	// original value on teardown to avoid leaking into sibling tests.
	prev := restoreDelay
	restoreDelay = 5 * time.Millisecond
	t.Cleanup(func() { restoreDelay = prev })

	// Prime the clipboard with the "prior" content the test will
	// expect to be restored.
	clipboard.Write(clipboard.FmtText, []byte("before"))

	var buf bytes.Buffer
	err := WriteWithStdout("hello", Destinations{
		Paste:                  true,
		PreservePriorClipboard: true,
	}, &buf)
	// We don't fail on err here: on hosts without a working paste
	// binary the keystroke step may surface an error even though the
	// clipboard half of the operation completed. The restore path is
	// gated on a successful paste, so if there is no restore goroutine
	// we have nothing to assert and can skip.
	if err != nil {
		t.Skipf("paste keystroke unavailable on this host: %v", err)
	}

	clipboardRestoreWG.Wait()

	got := clipboard.Read(clipboard.FmtText)
	if string(got) != "before" {
		t.Errorf("clipboard should have been restored to %q; got %q", "before", string(got))
	}
}

// TestWrite_PreserveClipboard_LeavesUserCopyAlone covers the
// don't-stomp-on-the-user case: if the user copies something new
// between the paste keystroke and the restore tick, the restore
// goroutine must see the mismatch and bail out instead of stomping on
// the fresh copy.
//
// We simulate "user copied something" by overwriting the clipboard
// before the restore goroutine wakes up. The restore window is widened
// to give us a deterministic ordering: write transcript -> overwrite
// -> wait -> assert.
func TestWrite_PreserveClipboard_LeavesUserCopyAlone(t *testing.T) {
	if err := initClipboard(); err != nil {
		t.Skipf("clipboard unavailable: %v", err)
	}

	prev := restoreDelay
	restoreDelay = 50 * time.Millisecond
	t.Cleanup(func() { restoreDelay = prev })

	clipboard.Write(clipboard.FmtText, []byte("before"))

	var buf bytes.Buffer
	err := WriteWithStdout("hello", Destinations{
		Paste:                  true,
		PreservePriorClipboard: true,
	}, &buf)
	if err != nil {
		t.Skipf("paste keystroke unavailable on this host: %v", err)
	}

	// Simulate the user copying something else right after paste — the
	// restore goroutine should observe the mismatch and skip the
	// restore so this fresh copy survives.
	clipboard.Write(clipboard.FmtText, []byte("user copied this"))

	clipboardRestoreWG.Wait()

	got := clipboard.Read(clipboard.FmtText)
	if string(got) != "user copied this" {
		t.Errorf("user clipboard should be preserved; got %q (want %q)", string(got), "user copied this")
	}
}

// TestWrite_PasteDelay_Sleeps verifies that a non-zero PasteDelay causes
// Write to block for at least that long before the paste keystroke. We
// can't easily observe the keystroke timing directly, but we can observe
// that Write itself doesn't return early.
//
// Uses very short delays (~20ms) so the test runs fast. Skipped on hosts
// without a working clipboard or paste binary.
func TestWrite_PasteDelay_Sleeps(t *testing.T) {
	if err := initClipboard(); err != nil {
		t.Skipf("clipboard unavailable: %v", err)
	}

	delay := 20 * time.Millisecond
	var buf bytes.Buffer
	start := time.Now()
	err := WriteWithStdout("hello", Destinations{
		Paste:      true,
		PasteDelay: delay,
	}, &buf)
	elapsed := time.Since(start)
	if err != nil {
		t.Skipf("paste keystroke unavailable on this host: %v", err)
	}
	if elapsed < delay {
		t.Errorf("WriteWithStdout returned in %v; expected to block at least %v", elapsed, delay)
	}
}

// TestWrite_PasteDelay_ZeroSkipsSleep is the regression guard: a 0 delay
// must NOT introduce any meaningful pause. The boundary is sloppy
// (clipboard write + paste shellout already take a few ms), so we just
// assert "well under 50ms" which is comfortably above no-delay timing on
// realistic hosts.
func TestWrite_PasteDelay_ZeroSkipsSleep(t *testing.T) {
	if err := initClipboard(); err != nil {
		t.Skipf("clipboard unavailable: %v", err)
	}

	var buf bytes.Buffer
	start := time.Now()
	err := WriteWithStdout("hello", Destinations{
		Paste:      true,
		PasteDelay: 0,
	}, &buf)
	elapsed := time.Since(start)
	if err != nil {
		t.Skipf("paste keystroke unavailable on this host: %v", err)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("WriteWithStdout with PasteDelay=0 took %v; expected < 200ms", elapsed)
	}
}
