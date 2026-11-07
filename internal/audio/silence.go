package audio

import (
	"encoding/binary"
	"errors"
	"time"
)

// ErrInvalidWAV indicates audio that cannot be safely trimmed as PCM16.
var ErrInvalidWAV = errors.New("silence removal requires a valid PCM16 WAV")

// SilenceResult contains a PCM16 WAV with long, near-zero gaps removed.
type SilenceResult struct {
	WAV     []byte
	Removed time.Duration
	Empty   bool
}

// TrimSilence removes continuous gaps strictly longer than one second.
// Every channel must stay within 32 PCM16 units (about -60 dBFS), so quiet
// speech and ordinary pauses are retained. Leading and trailing gaps count.
// The input is never mutated. PCM16 WAVs at any sample rate are supported.
func TrimSilence(wav []byte) (SilenceResult, error) {
	invalid := func() (SilenceResult, error) {
		return SilenceResult{}, ErrInvalidWAV
	}
	if len(wav) < 12 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return invalid()
	}
	end := uint64(binary.LittleEndian.Uint32(wav[4:8])) + 8
	if end > uint64(len(wav)) || end < 12 {
		return invalid()
	}
	var format, pcm []byte
	for pos := uint64(12); pos+8 <= end; {
		size := uint64(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		start := pos + 8
		if start+size > end {
			return invalid()
		}
		switch string(wav[pos : pos+4]) {
		case "fmt ":
			format = wav[start : start+size]
		case "data":
			pcm = wav[start : start+size]
		}
		pos = start + size + size%2
	}
	if len(format) < 16 || binary.LittleEndian.Uint16(format[:2]) != 1 || binary.LittleEndian.Uint16(format[14:16]) != 16 || pcm == nil {
		return invalid()
	}
	channels := int(binary.LittleEndian.Uint16(format[2:4]))
	rate := int(binary.LittleEndian.Uint32(format[4:8]))
	frameSize := channels * 2
	if channels == 0 || rate == 0 || int(binary.LittleEndian.Uint16(format[12:14])) != frameSize || len(pcm)%frameSize != 0 {
		return invalid()
	}

	trimmed := make([]byte, 0, len(pcm))
	gapStart, keptStart := 0, 0
	removed := 0
	flushGap := func(gapEnd int) {
		if (gapEnd-gapStart)/frameSize > rate {
			trimmed = append(trimmed, pcm[keptStart:gapStart]...)
			removed += gapEnd - gapStart
			keptStart = gapEnd
		}
	}
	for pos := 0; pos < len(pcm); pos += frameSize {
		silent := true
		for ch := 0; ch < frameSize; ch += 2 {
			sample := int16(binary.LittleEndian.Uint16(pcm[pos+ch : pos+ch+2]))
			if sample < -32 || sample > 32 {
				silent = false
				break
			}
		}
		if !silent {
			flushGap(pos)
			gapStart = pos + frameSize
		}
	}
	flushGap(len(pcm))
	trimmed = append(trimmed, pcm[keptStart:]...)

	// Rebuild a canonical PCM header, omitting metadata whose offsets or
	// durations may no longer describe the shortened recording.
	header := writeWAVHeader(uint32(len(trimmed)))
	copy(header[20:36], format[:16])
	result := append(header[:], trimmed...)
	return SilenceResult{
		WAV:     result,
		Removed: time.Duration(removed/frameSize) * time.Second / time.Duration(rate),
		Empty:   len(trimmed) == 0,
	}, nil
}
