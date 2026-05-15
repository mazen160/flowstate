package audio

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

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

	// mu guards started, stopped, samples, and device (the malgo runtime
	// invokes onSamples from a background thread).
	mu      sync.Mutex
	started bool
	stopped bool
	samples []byte

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
	// works on the FreeFlow side when input_device is unset.
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
		// Stash the DeviceID on the heap so its pointer remains valid
		// for the lifetime of the malgo Device.
		stored := id
		deviceConfig.Capture.DeviceID = unsafe.Pointer(&stored)
	}

	onSamples := func(_, pSample []byte, _ uint32) {
		if len(pSample) == 0 {
			return
		}
		r.mu.Lock()
		r.samples = append(r.samples, pSample...)
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
