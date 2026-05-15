package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// TestReporter_NoANSIWhenWriterIsBuffer pins the safety contract:
// piping flowstate into a non-TTY destination must yield byte-identical
// output to "no ANSI codes anywhere". A bytes.Buffer is never a TTY (it's
// not even an *os.File), so the ColorAuto path must suppress emission.
func TestReporter_NoANSIWhenWriterIsBuffer(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	var buf bytes.Buffer
	r := NewReporter(&buf, ColorAuto)
	r.Step("Transcribing")
	r.Done(42, 1234*time.Millisecond)
	r.Warning("mute failed: %v", "permission denied")
	r.Error("transcribe: %v", "boom")
	// Recording on a non-TTY prints one static line; assert below.
	stop := r.Recording(func() []float64 { return nil })
	stop()

	out := buf.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("ANSI escape leaked into non-TTY output:\n%q", out)
	}
	// Non-TTY recording must use the static prompt — no meter glyphs.
	if !strings.Contains(out, "Recording — press Enter to stop") {
		t.Fatalf("non-TTY recording output missing static prompt: %q", out)
	}
	// And the static line should NOT include a meter bar.
	if strings.ContainsRune(out, '▁') || strings.ContainsRune(out, '█') {
		t.Fatalf("non-TTY recording output included meter glyphs: %q", out)
	}
}

// TestReporter_ANSIEmittedWhenColorAlways verifies the force-on override:
// even though a bytes.Buffer is not a TTY, ColorAlways must still emit
// escape codes. This is the path for users running flowstate inside
// terminal multiplexers that don't propagate the TTY mode bit.
func TestReporter_ANSIEmittedWhenColorAlways(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	var buf bytes.Buffer
	r := NewReporter(&buf, ColorAlways)
	r.Step("Cleaning up")
	r.Done(7, 850*time.Millisecond)

	out := buf.String()
	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("expected ANSI escape codes in ColorAlways output, got %q", out)
	}
}

// TestReporter_RespectsNO_COLOR verifies that setting NO_COLOR in the
// environment overrides the auto path. We can't easily fake a TTY in a
// unit test, but the env var check is independent of TTY detection so the
// observable behavior is the same: no ANSI bytes anywhere.
func TestReporter_RespectsNO_COLOR(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var buf bytes.Buffer
	r := NewReporter(&buf, ColorAuto)
	r.Step("Transcribing")
	r.Done(1, 100*time.Millisecond)
	r.Warning("warn")
	r.Error("err")

	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("NO_COLOR=1 did not suppress ANSI codes:\n%q", buf.String())
	}
}

// TestReporter_ColorNever pins that the explicit override never emits ANSI
// even when forced by env-or-auto otherwise.
func TestReporter_ColorNever(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	r.Step("step")
	r.Done(0, 0)
	r.Warning("w")
	r.Error("e")

	if strings.Contains(buf.String(), "\x1b") {
		t.Fatalf("ColorNever leaked ANSI:\n%q", buf.String())
	}
}

// TestMeterBar_LevelMapping pins the glyph endpoints. Level 0 must map to
// the flattest glyph; level 1 must map to the full block. We assert the
// length is exactly 7 glyphs (in runes, not bytes) so a future tweak to
// width is a visible failure rather than a silent regression.
func TestMeterBar_LevelMapping(t *testing.T) {
	// All zeros → 7 of the flattest glyph.
	got := meterBar([]float64{0, 0, 0, 0, 0, 0, 0})
	if got != "▁▁▁▁▁▁▁" {
		t.Errorf("meterBar(zeros) = %q; want all flats", got)
	}
	// All ones → 7 full blocks.
	got = meterBar([]float64{1, 1, 1, 1, 1, 1, 1})
	if got != "▇▇▇▇▇▇▇" && got != "███████" {
		// meterGlyph maps level == 1 to len-1 index (█); a future tweak
		// that uses meterGlyphs[len-1] for level==1 must still emit
		// full blocks across the bar.
		t.Errorf("meterBar(ones) = %q; want full blocks", got)
	}
	// Half levels should not be the flattest or full glyph.
	half := meterBar([]float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5})
	for _, r := range half {
		if r == '▁' || r == '█' {
			t.Errorf("meterBar(halves) contained extreme glyph: %q", half)
			break
		}
	}
	// Missing slots are zeros (flat) — so a single-element slice yields
	// one variable glyph followed by six flats.
	short := meterBar([]float64{1})
	if !strings.HasSuffix(short, "▁▁▁▁▁▁") {
		t.Errorf("meterBar([1]) = %q; want suffix of six flats", short)
	}
}

// TestMeterGlyph_Endpoints pins the helper function used inside meterBar.
func TestMeterGlyph_Endpoints(t *testing.T) {
	if got := meterGlyph(0); got != '▁' {
		t.Errorf("meterGlyph(0) = %q; want ▁", got)
	}
	if got := meterGlyph(-0.5); got != '▁' {
		t.Errorf("meterGlyph(-0.5) = %q; want ▁ (clamped)", got)
	}
	if got := meterGlyph(1); got != '█' {
		t.Errorf("meterGlyph(1) = %q; want █", got)
	}
	if got := meterGlyph(2); got != '█' {
		t.Errorf("meterGlyph(2) = %q; want █ (clamped)", got)
	}
}

// TestFormatDuration_Forms covers the three rendering bands (ms, s, m).
func TestFormatDuration_Forms(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0ms"},
		{850 * time.Millisecond, "850ms"},
		{2300 * time.Millisecond, "2.3s"},
		{75 * time.Second, "1m15.0s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestReporter_RecordingStop_Idempotent verifies the stop function can be
// invoked multiple times without panicking — the sync.Once guard.
func TestReporter_RecordingStop_Idempotent(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	stop := r.Recording(nil)
	stop()
	stop()
	stop()
}
