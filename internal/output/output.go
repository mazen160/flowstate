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
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.design/x/clipboard"

	"github.com/mazin-ahmed/flowstate/internal/paste"
)

// Destinations is the boolean triple parsed out of the comma-separated
// `output_mode` config value by [config.Config.OutputDestinations], plus
// the optional preserve-clipboard knob driven by the
// `preserve_clipboard_after_paste` config field.
type Destinations struct {
	Stdout    bool
	Clipboard bool
	Paste     bool

	// PreservePriorClipboard, when true and Paste is also true, makes
	// Write:
	//  1. Read the current clipboard contents.
	//  2. Set the transcript on the clipboard.
	//  3. Trigger paste.
	//  4. After ~500ms, restore the saved clipboard IFF the current
	//     clipboard still equals the transcript (i.e. the user did not
	//     copy something else in between). If they did, the fresh copy
	//     is left alone.
	//
	// No-op when Paste is false.
	PreservePriorClipboard bool

	// PasteDelay, when > 0 and Paste is also true, inserts a sleep between
	// the clipboard-seed write and the Cmd+V / Ctrl+V keystroke. Lets the
	// user switch to the destination window before the paste fires. The
	// preserve-clipboard restore timer starts AFTER the paste keystroke
	// (so the total wall-clock time is roughly PasteDelay + restoreDelay).
	// No-op when Paste is false or PasteDelay <= 0.
	PasteDelay time.Duration
}

// clipboardInitOnce guards [clipboard.Init], which is a cgo init and must
// only run once per process. We cache the result so subsequent Write calls
// fail fast on headless systems instead of re-trying init each time.
var (
	clipboardInitOnce sync.Once
	clipboardInitErr  error
)

// restoreDelay is how long the preserve-clipboard goroutine waits after
// the paste keystroke before checking whether to restore the prior
// clipboard. Exposed as a package var (unexported) so tests can shorten
// it; production callers should never override.
var restoreDelay = 500 * time.Millisecond

// clipboardRestoreWG is exposed to tests in the same package so they can
// wait on the fire-and-forget restore goroutine. Not part of the public
// API; production callers never observe it.
var clipboardRestoreWG sync.WaitGroup

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
//
// When dests.Paste && dests.PreservePriorClipboard, the clipboard is
// snapshotted BEFORE the seed write, and a background goroutine restores
// the snapshot after restoreDelay — but only if the clipboard still
// contains the transcript we wrote (so an unrelated user copy isn't
// stomped on).
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

	// 2. Snapshot prior clipboard contents BEFORE we overwrite, so the
	//    restore-after-paste path has something to restore. We only do
	//    this when both Paste and PreservePriorClipboard are enabled —
	//    snapshotting otherwise would be pointless and would surface
	//    clipboard-read errors on hosts where the user did not even ask
	//    for paste behavior.
	var (
		priorClipboard      []byte
		priorClipboardValid bool
	)
	if dests.Paste && dests.PreservePriorClipboard {
		if err := initClipboard(); err == nil {
			priorClipboard = clipboard.Read(clipboard.FmtText)
			priorClipboardValid = true
		}
		// initClipboard failure here is silent on purpose: the next
		// clipboard write will surface a "clipboard unavailable"
		// error if appropriate, and we don't want to double-report.
	}

	// 3. Clipboard — explicit or implicit (Paste needs it). We track
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

	// 4. Paste — only attempt if the clipboard was actually seeded.
	//    Otherwise we'd paste whatever stale content was there before.
	if dests.Paste {
		if !clipboardSeeded {
			// Already reported via firstErr above; skip the keystroke.
		} else {
			// Optional pre-paste delay: gives the user time to switch
			// windows after the clipboard is seeded but before the
			// Cmd+V/Ctrl+V keystroke fires. Sleep is blocking — Write
			// is documented as synchronous on the caller's goroutine.
			if dests.PasteDelay > 0 {
				time.Sleep(dests.PasteDelay)
			}
			if err := paste.Paste(); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			} else if dests.PreservePriorClipboard && priorClipboardValid {
				// Paste keystroke fired successfully; schedule a
				// best-effort restore of the prior clipboard. The
				// goroutine is fire-and-forget so callers don't have to
				// wait restoreDelay before continuing. Tests sync via
				// clipboardRestoreWG.
				clipboardRestoreWG.Add(1)
				go func(prior []byte, transcript []byte) {
					defer clipboardRestoreWG.Done()
					time.Sleep(restoreDelay)
					// Bail if reading the clipboard fails for any
					// reason — we never want to panic the host process
					// from a background goroutine.
					current := clipboard.Read(clipboard.FmtText)
					if current == nil {
						fmt.Fprintln(os.Stderr, "warning: clipboard restore skipped (read failed)")
						return
					}
					// Only restore if the clipboard still holds the
					// transcript we just set. If the user copied
					// something else, leave that fresh copy alone.
					if !bytes.Equal(current, transcript) {
						return
					}
					clipboard.Write(clipboard.FmtText, prior)
				}(priorClipboard, []byte(text))
			}
		}
	}

	return firstErr
}
