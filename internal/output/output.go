// Package output dispatches cleaned text to the destinations the user has
// enabled via config.output_mode: stdout, clipboard, paste, or any subset.
//
// Each destination is best-effort: a failure in one (e.g., clipboard cannot
// initialize on a headless system) does not prevent the others from running.
// The FIRST non-nil error encountered is returned; subsequent errors are
// silently dropped (acceptable for v1).
//
// Paste depends on the clipboard already holding the text, because the
// per-OS paste implementation in [internal/paste] only simulates the
// Cmd+V / Ctrl+V keystroke. If the caller asks for Paste without Clipboard,
// this package still seeds the clipboard internally so the keystroke has
// something to paste.
package output

import (
	"fmt"
	"io"
	"os"
	"sync"

	"golang.design/x/clipboard"

	"github.com/mazin-ahmed/flowstate/internal/paste"
)

// Destinations is the boolean triple parsed out of the comma-separated
// `output_mode` config value by [config.Config.OutputDestinations].
type Destinations struct {
	Stdout    bool
	Clipboard bool
	Paste     bool
}

// clipboardInitOnce guards [clipboard.Init], which is a cgo init and must
// only run once per process. We cache the result so subsequent Write calls
// fail fast on headless systems instead of re-trying init each time.
var (
	clipboardInitOnce sync.Once
	clipboardInitErr  error
)

// initClipboard runs clipboard.Init() exactly once, lazily, and caches any
// error. Returns nil on every subsequent call if the first init succeeded,
// or the same error if it failed (e.g., no display server).
func initClipboard() error {
	clipboardInitOnce.Do(func() {
		clipboardInitErr = clipboard.Init()
	})
	return clipboardInitErr
}

// writeClipboard pushes text into the system clipboard, lazily initializing
// the underlying library on first use. The error is wrapped to make the
// "clipboard unavailable" condition easy to spot in user-facing output.
func writeClipboard(text string) error {
	if err := initClipboard(); err != nil {
		return fmt.Errorf("clipboard unavailable: %w", err)
	}
	clipboard.Write(clipboard.FmtText, []byte(text))
	return nil
}

// Write emits text to every destination enabled in dests. Stdout writes go
// to [os.Stdout]; use [WriteWithStdout] to substitute a different writer for
// testing.
func Write(text string, dests Destinations) error {
	return WriteWithStdout(text, dests, os.Stdout)
}

// WriteWithStdout is the testable form of [Write]: stdout is replaced by
// the given io.Writer so tests can capture bytes into a buffer.
//
// Order of operations matters: clipboard is written BEFORE paste, because
// paste is a keystroke (Cmd+V / Ctrl+V) and needs the clipboard primed.
// When the caller asks for Paste but not Clipboard, we still seed the
// clipboard — the user's clipboard is overwritten as a side-effect, but
// that's the only way the keystroke can deliver the text.
func WriteWithStdout(text string, dests Destinations, stdout io.Writer) error {
	var firstErr error

	// 1. Stdout — independent of the clipboard, runs first so users see
	//    output immediately even if downstream steps fail.
	if dests.Stdout {
		if _, err := fmt.Fprintln(stdout, text); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	// 2. Clipboard — explicit or implicit (Paste needs it). We track
	//    whether the clipboard actually has the text so we can decide
	//    whether to even attempt the paste keystroke.
	clipboardSeeded := false
	needClipboard := dests.Clipboard || dests.Paste
	if needClipboard {
		if err := writeClipboard(text); err != nil {
			if firstErr == nil && dests.Clipboard {
				// Only surface the clipboard error if the caller
				// explicitly asked for clipboard. If they only
				// asked for paste, the paste-step error below
				// will be more informative.
				firstErr = err
			} else if firstErr == nil {
				firstErr = err
			}
		} else {
			clipboardSeeded = true
		}
	}

	// 3. Paste — only attempt if the clipboard was actually seeded.
	//    Otherwise we'd paste whatever stale content was there before.
	if dests.Paste {
		if !clipboardSeeded {
			// Already reported via firstErr above; skip the keystroke.
		} else if err := paste.Paste(); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}
