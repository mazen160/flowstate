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
