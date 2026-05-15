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
	r.Done(DoneStats{Chars: 42, Words: 7, Tokens: 11, ProcessTime: 1234 * time.Millisecond})
	r.Warning("mute failed: %v", "permission denied")
	r.Error("transcribe: %v", "boom")
	// Recording on a non-TTY prints one static line; assert below.
	stop := r.Recording(func() []float64 { return nil }, "")
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
	r.Done(DoneStats{Chars: 7, ProcessTime: 850 * time.Millisecond})

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
	r.Done(DoneStats{Chars: 1, ProcessTime: 100 * time.Millisecond})
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
	r.Done(DoneStats{})
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
	stop := r.Recording(nil, "")
	stop()
	stop()
	stop()
}

// TestReporter_Recording_HintOverridesDefault verifies that a non-empty
// hint replaces the default "press Enter to stop" copy on the non-TTY
// path.
func TestReporter_Recording_HintOverridesDefault(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	stop := r.Recording(nil, "auto-stop in 5s (or press Enter)")
	stop()
	out := buf.String()
	if !strings.Contains(out, "auto-stop in 5s") {
		t.Fatalf("expected custom hint in output, got: %q", out)
	}
	if strings.Contains(out, "press Enter to stop") {
		t.Fatalf("default hint should not appear when custom hint is set, got: %q", out)
	}
}

// TestReporter_Recording_EmptyHintFallsBack verifies that passing "" for
// hint reproduces the documented default "press Enter to stop" copy.
func TestReporter_Recording_EmptyHintFallsBack(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	stop := r.Recording(nil, "")
	stop()
	out := buf.String()
	if !strings.Contains(out, "press Enter to stop") {
		t.Fatalf("expected default hint, got: %q", out)
	}
}

// TestDone_FullStats renders the headline + timing breakdown when every
// field is populated. Pins each labeled segment so a copy-tweak surfaces
// the failure with a clear diff.
func TestDone_FullStats(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	r.Done(DoneStats{
		Chars:       47,
		Words:       9,
		Tokens:      12,
		RecordTime:  3200 * time.Millisecond,
		ProcessTime: 1800 * time.Millisecond,
	})
	out := buf.String()
	for _, want := range []string{"47 chars", "9 words", "~12 tokens", "recording 3.2s", "processing 1.8s", "total 5.0s"} {
		if !strings.Contains(out, want) {
			t.Errorf("Done full-stats output missing %q:\n%s", want, out)
		}
	}
}

// TestDone_NoTimingOmitsSecondLine: when both timing fields are zero, the
// "rec/proc/total" line is suppressed entirely so the output stays a single
// line.
func TestDone_NoTimingOmitsSecondLine(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	r.Done(DoneStats{Chars: 5, Words: 1, Tokens: 2})
	out := buf.String()
	lineCount := strings.Count(out, "\n")
	if lineCount != 1 {
		t.Errorf("expected single-line output with no timing; got %d lines:\n%s", lineCount, out)
	}
	for _, banned := range []string{"recording ", "processing ", "total "} {
		if strings.Contains(out, banned) {
			t.Errorf("Done with zero timing should not mention %q:\n%s", banned, out)
		}
	}
}

// TestDone_PartialContentOmitsSegments: words=0 / tokens=0 must drop those
// segments so callers that don't track them produce clean output.
func TestDone_PartialContentOmitsSegments(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	r.Done(DoneStats{Chars: 12})
	out := buf.String()
	if !strings.Contains(out, "12 chars") {
		t.Errorf("expected '12 chars' in output:\n%s", out)
	}
	if strings.Contains(out, "words") || strings.Contains(out, "tokens") {
		t.Errorf("zero words/tokens should be omitted:\n%s", out)
	}
}

// TestDone_SingleTimingFieldDropsTotal: when only one of rec/proc is set,
// the synthetic "total" segment must be suppressed (it would just duplicate
// the single value).
func TestDone_SingleTimingFieldDropsTotal(t *testing.T) {
	var buf bytes.Buffer
	r := NewReporter(&buf, ColorNever)
	r.Done(DoneStats{Chars: 5, ProcessTime: 2 * time.Second})
	out := buf.String()
	if !strings.Contains(out, "processing 2.0s") {
		t.Errorf("expected 'processing 2.0s' in output:\n%s", out)
	}
	if strings.Contains(out, "total ") {
		t.Errorf("total should not appear with only one timing field:\n%s", out)
	}
}

// TestCountWords covers the empty / whitespace-only / multi-whitespace
// cases that the orchestrator can produce after sanitization.
func TestCountWords(t *testing.T) {
	cases := map[string]int{
		"":                   0,
		"   ":                0,
		"hello":              1,
		"hello world":        2,
		"  hello   world  ":  2,
		"one two three four": 4,
		"line1\nline2":       2,
	}
	for input, want := range cases {
		if got := CountWords(input); got != want {
			t.Errorf("CountWords(%q) = %d; want %d", input, got, want)
		}
	}
}

// TestEstimateTokens pins the ceil-div-by-4 heuristic for a few inputs.
// The exact ratio is approximate by design (real tokenization is BPE),
// but the formula must be stable across builds.
func TestEstimateTokens(t *testing.T) {
	cases := map[string]int{
		"":                       0,
		"a":                      1, // 1/4 → ceil = 1
		"ab":                     1,
		"abc":                    1,
		"abcd":                   1,
		"abcde":                  2,
		strings.Repeat("x", 16):  4,
		strings.Repeat("x", 100): 25,
	}
	for input, want := range cases {
		if got := EstimateTokens(input); got != want {
			t.Errorf("EstimateTokens(%q) = %d; want %d", input, got, want)
		}
	}
}
