package audio

import (
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"time"
)

// ErrInvalidWAV indicates audio that cannot be safely trimmed as PCM16.
var ErrInvalidWAV = errors.New("silence removal requires a valid PCM16 WAV")

// SilenceResult contains a PCM16 WAV with long, quiet gaps removed.
type SilenceResult struct {
	WAV     []byte
	Removed time.Duration
	Empty   bool
}

// TrimSilence removes continuous gaps strictly longer than one second.
// In recordings with microphone noise, silence is detected using 20 ms RMS
// windows and a bounded, adaptive noise threshold. Such cuts leave 150 ms of
// padding next to speech. Near-zero recordings use sample-level detection to
// preserve very quiet signals. Leading and trailing gaps count.
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

	windowFrames, levels, threshold := silenceLevels(pcm, channels, rate)
	adaptive := threshold > 32*32
	padding := 0
	if adaptive {
		padding = rate * 150 / 1000 * frameSize
	}
	trimmed := make([]byte, 0, len(pcm))
	gapStart, keptStart := 0, 0
	removed := 0
	flushGap := func(gapEnd int) {
		if (gapEnd-gapStart)/frameSize > rate {
			start, end := gapStart, gapEnd
			if start > 0 {
				start += padding
			}
			if end < len(pcm) {
				end -= padding
			}
			trimmed = append(trimmed, pcm[keptStart:start]...)
			removed += end - start
			keptStart = end
		}
	}
	if adaptive {
		for i, level := range levels {
			if level > threshold {
				flushGap(i * windowFrames * frameSize)
				gapStart = min((i+1)*windowFrames*frameSize, len(pcm))
			}
		}
	} else {
		// With no reliable separation between noise and speech, retain the
		// conservative sample-level detector rather than risk quiet speech.
		for pos := 0; pos < len(pcm); pos += frameSize {
			for ch := 0; ch < frameSize; ch += 2 {
				sample := int16(binary.LittleEndian.Uint16(pcm[pos+ch : pos+ch+2]))
				if sample < -32 || sample > 32 {
					flushGap(pos)
					gapStart = pos + frameSize
					break
				}
			}
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

// silenceLevels measures mean-square energy in each window, using the loudest
// channel so a quiet channel cannot hide speech in another. Squared levels
// avoid a square root for every window.
func silenceLevels(pcm []byte, channels, rate int) (windowFrames int, levels []float64, threshold float64) {
	windowFrames = max(1, rate/50)
	frameSize := channels * 2
	windowBytes := windowFrames * frameSize
	energy := make([]float64, channels)
	for start := 0; start < len(pcm); start += windowBytes {
		end := min(start+windowBytes, len(pcm))
		clear(energy)
		for pos := start; pos < end; pos += frameSize {
			for ch := range channels {
				offset := pos + ch*2
				sample := float64(int16(binary.LittleEndian.Uint16(pcm[offset : offset+2])))
				energy[ch] += sample * sample
			}
		}
		levels = append(levels, slices.Max(energy)/float64((end-start)/frameSize))
	}
	threshold = 32 * 32
	if len(levels) == 0 {
		return
	}
	ordered := slices.Clone(levels)
	slices.Sort(ordered)
	// Allow noise up to three times the quietest 20% of windows, but stay
	// four times below the upper speech level and below -45 dBFS RMS. The
	// speech cap keeps a uniformly quiet voice from becoming its own noise
	// floor; the absolute cap keeps louder background audio intact.
	noise, speech := ordered[len(ordered)/5], ordered[(len(ordered)-1)*95/100]
	threshold = math.Max(threshold, math.Min(184*184, math.Min(noise*9, speech/16)))
	return
}
