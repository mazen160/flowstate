// Package ui prints staged pipeline status updates to a writer. When the
// writer is a TTY and colors are enabled it emits subtle ANSI color codes
// and uses \r-based redraws to animate the recording audio-level meter.
// When the writer is not a TTY (or colors are disabled), output degrades
// to one plain line per state — safe to pipe through cat or redirect to
// a file with no escape codes leaking through.
//
// The package has no external dependencies: TTY detection is done via
// os.File.Stat() and the device-mode bit, and the redraw goroutine uses
// time.Ticker + a sync.Once-guarded stop channel.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ColorMode controls whether ANSI escape codes are emitted by a Reporter.
//
// ColorAuto is the documented default: emit only when the writer is a TTY,
// the NO_COLOR environment variable is unset, and the mode hasn't been
// overridden by config or flag. ColorAlways forces emission regardless of
// destination (useful when piping into a multiplexer that doesn't appear as
// a TTY). ColorNever suppresses all escape codes (useful for plain log
// capture).
type ColorMode int

const (
	// ColorAuto emits ANSI codes only when the writer is a TTY and the
	// NO_COLOR env var is unset.
	ColorAuto ColorMode = iota
	// ColorAlways emits ANSI codes unconditionally.
	ColorAlways
	// ColorNever suppresses all ANSI codes.
	ColorNever
)

// ANSI escape codes used by the reporter. Kept short and deliberately
// subtle — we want hint-of-color, not a neon dashboard.
const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	// ansiClearLine pairs carriage-return with "erase in line, all" so a
	// redrawn meter never leaves trailing characters from a longer prior
	// frame.
	ansiClearLine = "\r\x1b[2K"
)

// meterGlyphs maps level buckets to block-glyph height. Index 0 is the
// flattest bar; index len-1 is the full height block.
var meterGlyphs = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Reporter writes staged pipeline status to a writer. Construct via
// [NewReporter]; the value should not be copied (the internal mutex and
// goroutine state are tied to the original instance).
type Reporter struct {
	w     io.Writer
	mode  ColorMode
	color bool // resolved emit decision; computed once at construction
	tty   bool // resolved TTY decision; computed once at construction
}

// NewReporter constructs a Reporter writing to w with the given color mode.
// TTY detection is performed at construction time: if w is an *os.File
// whose device-mode bit is set, the writer is treated as a TTY. Anything
// else (bytes.Buffer, *os.File pointed at a regular file, etc.) is treated
// as non-interactive.
//
// The color emit decision is also resolved here:
//   - ColorAlways → emit.
//   - ColorNever  → suppress.
//   - ColorAuto   → emit iff TTY and NO_COLOR env var is unset.
func NewReporter(w io.Writer, mode ColorMode) *Reporter {
	tty := isTerminal(w)
	emit := mode == ColorAlways ||
		(mode == ColorAuto && tty && os.Getenv("NO_COLOR") == "")
	return &Reporter{
		w:     w,
		mode:  mode,
		color: emit,
		tty:   tty,
	}
}

// isTerminal reports whether w is an interactive terminal. We check for an
// *os.File and inspect the Stat() mode bits. Anything that isn't a file —
// bytes.Buffer, *bufio.Writer over a file, etc. — is treated as
// non-interactive, which is the safe default for tests and pipelines.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi == nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorize wraps s in the given ANSI color if color emission is enabled.
// Otherwise it returns s unchanged. The reset code is unconditionally
// appended on the colorized path so callers can concatenate freely.
func (r *Reporter) colorize(code, s string) string {
	if !r.color {
		return s
	}
	return code + s + ansiReset
}

// Recording starts the recording status line. When the writer is a TTY,
// it kicks off a redraw loop that polls levelFn every ~80ms and renders a
// "● Recording — press Enter to stop  ▂▃▅▆▅▃▁" line in place using
// \r-based redraws. When the writer is not a TTY, it prints a single
// static line and returns a no-op stop function.
//
// The returned stop function:
//   - is safe to call exactly once or multiple times (guarded by sync.Once);
//   - is safe to call from any goroutine, including while the redraw
//     goroutine is mid-frame (the ticker is stopped first and we wait for
//     the draw goroutine to acknowledge before clearing the line);
//   - on the interactive path, clears the meter line and writes a final
//     newline so subsequent Step/Done output starts on a fresh row.
func (r *Reporter) Recording(levelFn func() []float64) (stop func()) {
	if !r.tty {
		// Non-interactive: one static line, no redraw goroutine. The
		// "press Enter" copy matches the pre-Reporter behavior so users
		// piping flowstate into a file see the familiar prompt.
		fmt.Fprint(r.w, "Recording — press Enter to stop\n")
		return func() {}
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var once sync.Once

	go r.drawRecordingLoop(levelFn, stopCh, doneCh)

	return func() {
		once.Do(func() {
			close(stopCh)
			<-doneCh
			// Clear the meter line and drop to a fresh row so the next
			// Step/Done message doesn't share a line with leftover meter
			// glyphs.
			fmt.Fprint(r.w, ansiClearLine)
		})
	}
}

// drawRecordingLoop polls levelFn on an 80ms ticker and rewrites the
// recording status line on each tick. Exits cleanly on stopCh close,
// signaling completion via doneCh so the stop function can wait for the
// last frame to flush before clearing the line.
func (r *Reporter) drawRecordingLoop(levelFn func() []float64, stopCh, doneCh chan struct{}) {
	defer close(doneCh)

	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	// Draw an immediate first frame so the prompt appears without waiting
	// for the first tick. Especially important for very short recordings
	// where the user might Stop before any tick fires.
	r.drawRecordingFrame(levelFn)

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			r.drawRecordingFrame(levelFn)
		}
	}
}

// drawRecordingFrame renders one frame of the recording meter to the
// underlying writer. The frame is prefixed with ansiClearLine so each
// repaint starts from a known-clean line.
func (r *Reporter) drawRecordingFrame(levelFn func() []float64) {
	var levels []float64
	if levelFn != nil {
		levels = levelFn()
	}
	bar := meterBar(levels)

	dot := r.colorize(ansiRed, "●")
	hint := r.colorize(ansiCyan, "press Enter to stop")
	// The em dash here is intentional — matches the static non-TTY
	// "Recording — press Enter to stop" form so users get a consistent
	// look across modes.
	fmt.Fprintf(r.w, "%s%s Recording — %s  %s", ansiClearLine, dot, hint, bar)
}

// meterBar renders a 7-glyph audio level meter from levels. Missing slots
// (a short or nil input slice) are rendered as the flattest glyph so the
// bar always has a stable width. Levels are clamped to [0,1] and mapped
// to len(meterGlyphs) buckets via floor(level * len).
func meterBar(levels []float64) string {
	const width = 7
	var b strings.Builder
	b.Grow(width * 4) // each block glyph is 3 bytes in UTF-8, +1 cushion
	for i := 0; i < width; i++ {
		var l float64
		if i < len(levels) {
			l = levels[i]
		}
		b.WriteRune(meterGlyph(l))
	}
	return b.String()
}

// meterGlyph maps a single level in [0,1] to a block glyph. Levels at or
// below zero map to the flattest glyph (▁); levels at or above one map to
// the full block (█).
func meterGlyph(level float64) rune {
	if level <= 0 {
		return meterGlyphs[0]
	}
	if level >= 1 {
		return meterGlyphs[len(meterGlyphs)-1]
	}
	idx := int(level * float64(len(meterGlyphs)))
	if idx >= len(meterGlyphs) {
		idx = len(meterGlyphs) - 1
	}
	return meterGlyphs[idx]
}

// Step prints a single-line status update like "● Transcribing…" in dim
// cyan. The trailing newline is included so subsequent output starts on
// a fresh row.
func (r *Reporter) Step(label string) {
	dot := r.colorize(ansiCyan, "●")
	fmt.Fprintf(r.w, "%s %s…\n", dot, label)
}

// Done prints the green success line. duration is formatted as a short
// human-friendly string ("2.3s", "850ms").
func (r *Reporter) Done(chars int, duration time.Duration) {
	check := r.colorize(ansiGreen, "✓")
	msg := fmt.Sprintf("Done. (%d characters, %s)", chars, formatDuration(duration))
	if r.color {
		msg = ansiGreen + msg + ansiReset
	}
	fmt.Fprintf(r.w, "%s %s\n", check, msg)
}

// Warning prints a yellow ⚠ warning line. Use for non-fatal soft errors
// (mute failure, "no speech detected", config warnings).
func (r *Reporter) Warning(format string, args ...any) {
	sym := r.colorize(ansiYellow, "⚠")
	body := fmt.Sprintf(format, args...)
	if r.color {
		body = ansiYellow + body + ansiReset
	}
	fmt.Fprintf(r.w, "%s %s\n", sym, body)
}

// Error prints a red ✗ error line. Use for fatal errors that will be
// followed by a non-zero exit.
func (r *Reporter) Error(format string, args ...any) {
	sym := r.colorize(ansiRed, "✗")
	body := fmt.Sprintf(format, args...)
	if r.color {
		body = ansiRed + body + ansiReset
	}
	fmt.Fprintf(r.w, "%s %s\n", sym, body)
}

// formatDuration renders a Duration as a short human string. Sub-second
// values are rendered in milliseconds ("850ms"); >= 1s uses one decimal
// place of seconds ("2.3s"); >= 60s uses Go's stock "1m2.3s" form.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	// Stock Go format: "1m2.345s". Trim the trailing decimal noise.
	mins := int(d / time.Minute)
	secs := d - time.Duration(mins)*time.Minute
	return fmt.Sprintf("%dm%.1fs", mins, secs.Seconds())
}
