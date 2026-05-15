package audio

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// peakHistorySize is the number of recent peak amplitudes retained for the
// audio-level meter UI. Seven matches the rendered bar width in
// internal/ui's meterBar so the consumer can pass the slice through
// without resizing.
const peakHistorySize = 7

// Recorder captures microphone input as 16 kHz mono PCM16 and writes a WAV
// file on Stop. A Recorder is single-use within a Start/Stop pair: after
// Stop, the recorder is closed and cannot be restarted. Callers that want
// to record again should construct a new Recorder.
//
// Recorder is safe for use from a single goroutine that drives the
// Start/Stop/Close lifecycle. The malgo data callback runs on an internal
// audio thread; appended samples are protected by mu.
type Recorder struct {
	deviceUID string

	// mu guards started, stopped, samples, peaks, and device (the malgo
	// runtime invokes onSamples from a background thread; PeakHistory may
	// be called concurrently from the UI redraw goroutine).
	mu      sync.Mutex
	started bool
	stopped bool
	samples []byte
	peaks   [peakHistorySize]float64

	ctx    *malgo.AllocatedContext
	device *malgo.Device
}

// NewRecorder constructs a recorder for the given device UID. Passing the
// empty string selects the system default input device. Construction does
// NOT open the device — that happens in Start — so a NewRecorder call
// against a missing device still succeeds and the error surfaces from
// Start.
func NewRecorder(deviceUID string) (*Recorder, error) {
	return &Recorder{deviceUID: deviceUID}, nil
}

// Start begins capture. It opens an malgo context and capture device
// configured for PCM16 mono 16 kHz, then registers a data callback that
// appends incoming PCM bytes to an in-memory buffer. Start is non-blocking;
// the data callback runs on an internal audio thread until Stop is called.
//
// Start returns an error if called twice on the same recorder, if the
// underlying malgo context fails to initialize, or if the capture device
// cannot be opened.
func (r *Recorder) Start() error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("audio: recorder already started")
	}
	if r.stopped {
		r.mu.Unlock()
		return errors.New("audio: recorder already stopped")
	}
	r.started = true
	r.mu.Unlock()

	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("audio: init malgo context: %w", err)
	}

	deviceConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	deviceConfig.Capture.Format = malgo.FormatS16
	deviceConfig.Capture.Channels = uint32(wavNumChannels)
	deviceConfig.SampleRate = wavSampleRate
	deviceConfig.Alsa.NoMMap = 1

	// Bind a specific device by UID if requested. The empty string falls
	// through to the system default, which mirrors how AVCaptureDevice
	// works on the upstream when input_device is unset.
	if r.deviceUID != "" {
		id, found, lookupErr := findCaptureDeviceID(ctx, r.deviceUID)
		if lookupErr != nil {
			_ = ctx.Uninit()
			ctx.Free()
			r.mu.Lock()
			r.started = false
			r.mu.Unlock()
			return fmt.Errorf("audio: enumerate capture devices: %w", lookupErr)
		}
		if !found {
			_ = ctx.Uninit()
			ctx.Free()
			r.mu.Lock()
			r.started = false
			r.mu.Unlock()
			return fmt.Errorf("audio: input device %q not found", r.deviceUID)
		}
		// Stash the DeviceID and pin it so its address can cross the
		// cgo boundary. Go 1.21+ rejects passing a Go pointer (the
		// DeviceConfig struct) that itself contains a Go pointer
		// (DeviceID field) into C without explicit pinning. The pin
		// must outlive malgo.InitDevice below; ma_device_init copies
		// the bytes into its own state and doesn't need our pointer
		// after it returns, so deferring Unpin until Start completes
		// is safe.
		stored := id
		var pinner runtime.Pinner
		defer pinner.Unpin()
		pinner.Pin(&stored)
		deviceConfig.Capture.DeviceID = unsafe.Pointer(&stored)
	}

	onSamples := func(_, pSample []byte, _ uint32) {
		if len(pSample) == 0 {
			return
		}
		// Peak amplitude over this callback buffer. Done outside the lock
		// — it's a pure function of the input bytes — so the audio
		// thread spends less time blocked on r.mu when PeakHistory races
		// from the UI goroutine.
		peak := computePeak(pSample)
		r.mu.Lock()
		r.samples = append(r.samples, pSample...)
		// Shift the ring left by one and append the new peak at the end.
		// A copy() call would also work but the small constant width
		// makes the unrolled shift clearer.
		for i := 0; i < len(r.peaks)-1; i++ {
			r.peaks[i] = r.peaks[i+1]
		}
		r.peaks[len(r.peaks)-1] = peak
		r.mu.Unlock()
	}

	device, err := malgo.InitDevice(ctx.Context, deviceConfig, malgo.DeviceCallbacks{
		Data: onSamples,
	})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("audio: init capture device: %w", err)
	}

	if err := device.Start(); err != nil {
		device.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		r.mu.Lock()
		r.started = false
		r.mu.Unlock()
		return fmt.Errorf("audio: start capture device: %w", err)
	}

	r.mu.Lock()
	r.ctx = ctx
	r.device = device
	r.mu.Unlock()
	return nil
}

// Stop ends capture, flushes the buffered PCM bytes to a freshly created
// temp WAV file, and returns the file path. The caller owns the temp file
// and is responsible for deleting it when done.
//
// Stop is idempotent: a second call after a successful Stop returns
// ("", nil). Calling Stop before Start returns an error.
//
// Stop calls Close implicitly, releasing the underlying device and malgo
// context regardless of success.
func (r *Recorder) Stop() (string, error) {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return "", errors.New("audio: stop called before start")
	}
	if r.stopped {
		r.mu.Unlock()
		return "", nil
	}
	r.stopped = true
	device := r.device
	ctx := r.ctx
	r.device = nil
	r.ctx = nil
	r.mu.Unlock()

	// Tear down the capture device before reading r.samples so the data
	// callback can't race with the snapshot below.
	if device != nil {
		device.Uninit()
	}
	if ctx != nil {
		_ = ctx.Uninit()
		ctx.Free()
	}

	r.mu.Lock()
	samples := r.samples
	r.samples = nil
	r.mu.Unlock()

	if len(samples) == 0 {
		return "", errors.New("audio: no audio captured")
	}

	f, err := os.CreateTemp("", "flowstate-*.wav")
	if err != nil {
		return "", fmt.Errorf("audio: create temp wav: %w", err)
	}
	path := f.Name()
	// Close immediately; writeWAVFile reopens with truncation. This keeps
	// the WAV write logic in one place (wav.go) and avoids passing an
	// open file handle across packages.
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("audio: close temp wav: %w", cerr)
	}

	if err := writeWAVFile(path, samples); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// PeakHistory returns a snapshot of the most recent peak amplitudes
// observed in the audio data callback. The returned slice always has
// length [peakHistorySize] (currently 7); slots that haven't been filled
// yet (e.g. immediately after Start) hold zeros. Index 0 is the oldest
// retained peak; the last index is the newest.
//
// Safe for concurrent reads while the audio thread is invoking onSamples.
// The returned slice is a fresh copy; mutating it does not affect the
// recorder's internal state.
func (r *Recorder) PeakHistory() []float64 {
	out := make([]float64, peakHistorySize)
	r.mu.Lock()
	for i := range r.peaks {
		out[i] = r.peaks[i]
	}
	r.mu.Unlock()
	return out
}

// computePeak returns the largest absolute sample value in pcm,
// interpreted as little-endian PCM16, normalized to [0, 1]. The input is
// expected to be a multiple of 2 bytes per sample; a trailing odd byte
// (which malgo doesn't produce in practice) is silently dropped.
//
// Extracted as a top-level helper so PeakHistory's accuracy can be tested
// without a real audio device.
func computePeak(pcm []byte) float64 {
	var peak int32
	n := len(pcm) - len(pcm)%2
	for i := 0; i < n; i += 2 {
		// Little-endian int16 decode.
		sample := int16(pcm[i]) | int16(pcm[i+1])<<8
		v := int32(sample)
		if v < 0 {
			v = -v
		}
		if v > peak {
			peak = v
		}
	}
	// 32768 is the magnitude of the most-negative int16 (-32768). Dividing
	// by it normalizes a true full-scale signal to 1.0; positive samples
	// max out at 32767/32768 ≈ 0.99997, which is close enough that the
	// meter saturation looks correct.
	return float64(peak) / 32768.0
}

// Close releases any held device handles and malgo context. Safe to call
// multiple times; Stop calls Close implicitly. Close does NOT write a WAV
// file — for that, call Stop.
func (r *Recorder) Close() error {
	r.mu.Lock()
	device := r.device
	ctx := r.ctx
	r.device = nil
	r.ctx = nil
	r.stopped = true
	r.mu.Unlock()

	if device != nil {
		device.Uninit()
	}
	if ctx != nil {
		_ = ctx.Uninit()
		ctx.Free()
	}
	return nil
}
