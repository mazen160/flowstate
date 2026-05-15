package audio

import (
	"os"
	"strings"
	"testing"
)

// TestNewRecorder_DefaultDevice verifies that constructing a recorder with an
// empty device UID never errors — opening the actual device is deferred to
// Start, so headless CI environments without a microphone still pass this.
func TestNewRecorder_DefaultDevice(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder(\"\"): %v", err)
	}
	if r == nil {
		t.Fatal("NewRecorder returned nil recorder")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close on unstarted recorder: %v", err)
	}
}

func TestNewRecorder_NamedDevice(t *testing.T) {
	// Construction never touches malgo, so a bogus UID is fine here.
	r, err := NewRecorder("bogus-device-uid")
	if err != nil {
		t.Fatalf("NewRecorder(bogus): %v", err)
	}
	if r == nil {
		t.Fatal("NewRecorder returned nil recorder")
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close on unstarted recorder: %v", err)
	}
}

// TestRecorder_StopWithoutStart confirms that Stop on a fresh recorder
// returns an error rather than silently succeeding — the caller relied on
// Start to allocate the capture pipeline, so there's nothing to flush.
func TestRecorder_StopWithoutStart(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	path, err := r.Stop()
	if err == nil {
		t.Fatalf("Stop without Start returned nil error (path=%q)", path)
	}
	if path != "" {
		t.Fatalf("Stop without Start returned path=%q, want empty", path)
	}
	if !strings.Contains(err.Error(), "stop called before start") {
		t.Fatalf("unexpected error from Stop without Start: %v", err)
	}
}

// TestRecorder_StopIdempotent records briefly from the default device, then
// confirms the second Stop returns ("", nil). It skips cleanly when no audio
// device is available (e.g., headless CI).
func TestRecorder_StopIdempotent(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	if err := r.Start(); err != nil {
		t.Skipf("no audio device available in this environment: %v", err)
	}

	path1, err := r.Stop()
	if err != nil {
		// If Start succeeded but no buffers arrived (no real mic input),
		// Stop legitimately errors with "no audio captured". Treat that
		// the same way as a missing device for CI purposes.
		if strings.Contains(err.Error(), "no audio captured") {
			t.Skipf("audio device opened but produced no samples: %v", err)
		}
		t.Fatalf("first Stop: %v", err)
	}
	defer func() {
		if path1 != "" {
			_ = os.Remove(path1)
		}
	}()
	if path1 == "" {
		t.Fatal("first Stop returned empty path with nil error")
	}

	path2, err := r.Stop()
	if err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if path2 != "" {
		t.Fatalf("second Stop returned path=%q, want empty (idempotent)", path2)
	}
}

func TestRecorder_StartTwice(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	if err := r.Start(); err != nil {
		t.Skipf("no audio device available in this environment: %v", err)
	}
	defer func() {
		_, _ = r.Stop()
	}()

	if err := r.Start(); err == nil {
		t.Fatal("second Start returned nil error, expected already-started")
	}
}

func TestRecorder_CloseIdempotent(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestPeakHistory_LengthAlways7 pins the contract that PeakHistory always
// returns exactly 7 floats, even on a freshly-constructed recorder that has
// never seen audio data — the UI redraw goroutine relies on this so it can
// blindly index into the slice without bounds checks.
func TestPeakHistory_LengthAlways7(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	peaks := r.PeakHistory()
	if len(peaks) != 7 {
		t.Fatalf("PeakHistory length = %d; want 7", len(peaks))
	}
	for i, v := range peaks {
		if v != 0 {
			t.Errorf("PeakHistory[%d] = %v on fresh recorder; want 0", i, v)
		}
	}
}

// TestComputePeak_Endpoints pins the normalization. The peak helper is the
// pure-function half of the onSamples callback; exercising it directly lets
// us assert the math without needing a live audio device.
func TestComputePeak_Endpoints(t *testing.T) {
	// Empty input → zero peak. Otherwise we'd divide by zero or return NaN
	// and the meter would render garbage on the first frame.
	if got := computePeak(nil); got != 0 {
		t.Errorf("computePeak(nil) = %v; want 0", got)
	}
	if got := computePeak([]byte{}); got != 0 {
		t.Errorf("computePeak(empty) = %v; want 0", got)
	}

	// All zeros → zero peak.
	zeros := make([]byte, 16) // 8 int16 samples
	if got := computePeak(zeros); got != 0 {
		t.Errorf("computePeak(zeros) = %v; want 0", got)
	}

	// Single max-positive int16 (32767) → ≈ 0.99997.
	maxPos := []byte{0xff, 0x7f}
	if got := computePeak(maxPos); got < 0.99 || got > 1.0 {
		t.Errorf("computePeak(maxPos) = %v; want ~1.0", got)
	}

	// Single min-negative int16 (-32768) → 1.0 exactly.
	minNeg := []byte{0x00, 0x80}
	if got := computePeak(minNeg); got != 1.0 {
		t.Errorf("computePeak(minNeg) = %v; want 1.0", got)
	}

	// Mixed buffer: a quiet 1024 sample (about 0.031) and a louder 16384
	// sample (0.5). The helper must return the louder one.
	mixed := []byte{
		0x00, 0x04, // +1024
		0x00, 0x40, // +16384
		0x00, 0x00, // 0
	}
	got := computePeak(mixed)
	if got < 0.49 || got > 0.51 {
		t.Errorf("computePeak(mixed) = %v; want ~0.5", got)
	}
}

// TestPeakHistory_RingShift drives the same onSamples shift logic that the
// audio callback uses. Rather than try to inject the callback into a live
// recorder, we exercise the public PeakHistory contract by simulating
// repeated peak-write cycles via a small helper that mirrors the callback
// math, then assert the ring moves left as new peaks arrive.
//
// The behavior under test:
//   - The newest peak is always at index 6.
//   - Older peaks shift toward index 0.
//   - After 7 writes the oldest write has fallen off the front.
func TestPeakHistory_RingShift(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	// Mirror the in-callback math so the assertion is independent of any
	// audio thread. The mutex acquire pattern matches the production code
	// so a future refactor that changes the ring's storage trips this test.
	pushPeak := func(p float64) {
		r.mu.Lock()
		for i := 0; i < len(r.peaks)-1; i++ {
			r.peaks[i] = r.peaks[i+1]
		}
		r.peaks[len(r.peaks)-1] = p
		r.mu.Unlock()
	}

	want := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7}
	for _, p := range want {
		pushPeak(p)
	}
	got := r.PeakHistory()
	for i, v := range want {
		if got[i] != v {
			t.Errorf("PeakHistory[%d] = %v; want %v (full want=%v, got=%v)",
				i, got[i], v, want, got)
		}
	}

	// One more push — the oldest (0.1) should drop off and 0.8 should
	// land at the end.
	pushPeak(0.8)
	got = r.PeakHistory()
	wantAfter := []float64{0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}
	for i, v := range wantAfter {
		if got[i] != v {
			t.Errorf("after shift PeakHistory[%d] = %v; want %v", i, got[i], v)
		}
	}
}

// TestPeakHistory_ReturnsCopy verifies that mutating the returned slice
// does not poison the recorder's internal ring. The UI goroutine reads
// this slice on every frame; an aliased return would let a buggy renderer
// corrupt the audio state.
func TestPeakHistory_ReturnsCopy(t *testing.T) {
	r, err := NewRecorder("")
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	defer func() {
		_ = r.Close()
	}()

	r.mu.Lock()
	r.peaks[6] = 0.42
	r.mu.Unlock()

	first := r.PeakHistory()
	first[6] = 999

	second := r.PeakHistory()
	if second[6] != 0.42 {
		t.Errorf("PeakHistory shared backing array: second[6]=%v; want 0.42", second[6])
	}
}

// TestListInputDevices_DoesNotPanic exercises the device enumeration entry
// point. We can't assert on specific devices (CI machines vary wildly), but
// the call must either return a slice (possibly empty) or a real error —
// never panic. If malgo init fails on this host, skip rather than fail.
func TestListInputDevices_DoesNotPanic(t *testing.T) {
	devices, err := ListInputDevices()
	if err != nil {
		t.Skipf("ListInputDevices unavailable on this host: %v", err)
	}
	for i, d := range devices {
		if d.UID == "" {
			t.Errorf("device %d has empty UID: %+v", i, d)
		}
		if d.Name == "" {
			t.Errorf("device %d has empty Name: %+v", i, d)
		}
	}
}
