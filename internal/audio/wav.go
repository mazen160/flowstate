// Package audio captures microphone input as 16 kHz mono PCM16 and writes
// WAV files for downstream transcription. The capture pipeline mirrors the
// FreeFlow Swift recorder's output format (PCM signed 16-bit, little-endian,
// mono, 16000 Hz) so the wire format reaching the Groq Whisper API is
// identical.
package audio

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAV format constants. These are fixed for the entire package because the
// transcription pipeline expects exactly this format — there is no scenario
// in which Flowstate writes a non-PCM16/mono/16k WAV.
const (
	wavSampleRate    uint32 = 16000
	wavNumChannels   uint16 = 1
	wavBitsPerSample uint16 = 16
	// wavHeaderSize is the standard 44-byte RIFF/WAVE header for a PCM
	// stream with a single fmt subchunk (size 16) followed by a single
	// data chunk.
	wavHeaderSize = 44
	// wavAudioFormatPCM is the WAVE_FORMAT_PCM tag.
	wavAudioFormatPCM uint16 = 1
)

// writeWAVHeader writes a 44-byte RIFF/WAVE header for a PCM16 mono 16 kHz
// stream whose data payload is dataSize bytes long. The header layout is:
//
//	offset  size  field
//	0       4     "RIFF"
//	4       4     overall size (file size - 8) = 36 + dataSize
//	8       4     "WAVE"
//	12      4     "fmt "
//	16      4     fmt subchunk size (16 for PCM)
//	20      2     audio format (1 = PCM)
//	22      2     num channels (1)
//	24      4     sample rate (16000)
//	28      4     byte rate = sample_rate * num_channels * bits/8 (32000)
//	32      2     block align = num_channels * bits/8 (2)
//	34      2     bits per sample (16)
//	36      4     "data"
//	40      4     data chunk size = dataSize
//
// It returns the 44-byte header rather than writing directly so callers can
// either prepend it to an in-memory payload or stream it to a writer.
func writeWAVHeader(dataSize uint32) [wavHeaderSize]byte {
	var hdr [wavHeaderSize]byte

	byteRate := wavSampleRate * uint32(wavNumChannels) * uint32(wavBitsPerSample) / 8
	blockAlign := wavNumChannels * wavBitsPerSample / 8

	// RIFF header
	copy(hdr[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(hdr[4:8], 36+dataSize)
	copy(hdr[8:12], []byte("WAVE"))

	// fmt subchunk
	copy(hdr[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(hdr[16:20], 16) // PCM subchunk size
	binary.LittleEndian.PutUint16(hdr[20:22], wavAudioFormatPCM)
	binary.LittleEndian.PutUint16(hdr[22:24], wavNumChannels)
	binary.LittleEndian.PutUint32(hdr[24:28], wavSampleRate)
	binary.LittleEndian.PutUint32(hdr[28:32], byteRate)
	binary.LittleEndian.PutUint16(hdr[32:34], blockAlign)
	binary.LittleEndian.PutUint16(hdr[34:36], wavBitsPerSample)

	// data subchunk
	copy(hdr[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(hdr[40:44], dataSize)

	return hdr
}

// writeWAV writes a complete WAV file (header + payload) to w. payload must
// contain raw little-endian PCM16 mono samples at 16 kHz; this function does
// not validate sample content, only the length (which must be a multiple of
// blockAlign for the file to be well-formed PCM).
func writeWAV(w io.Writer, payload []byte) error {
	if len(payload)%int(wavNumChannels*wavBitsPerSample/8) != 0 {
		return fmt.Errorf("payload length %d is not aligned to block size %d",
			len(payload), wavNumChannels*wavBitsPerSample/8)
	}
	if uint64(len(payload)) > uint64(^uint32(0))-36 {
		return fmt.Errorf("payload too large for WAV header (%d bytes)", len(payload))
	}

	hdr := writeWAVHeader(uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("write wav header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write wav payload: %w", err)
	}
	return nil
}

// writeWAVFile writes a WAV file at path with the given PCM16 mono 16 kHz
// payload. It creates the file with mode 0o600 (the file may contain audio
// the user expects to be private) and returns a wrapped error on failure.
func writeWAVFile(path string, payload []byte) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open wav file: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close wav file: %w", cerr)
		}
	}()

	return writeWAV(f, payload)
}
