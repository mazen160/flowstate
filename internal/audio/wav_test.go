package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteWAVHeader_RoundtripStandard(t *testing.T) {
	// Pretend we have 1 second of audio: 16000 frames * 2 bytes/frame.
	const payloadSize uint32 = 32000
	hdr := writeWAVHeader(payloadSize)

	if got := string(hdr[0:4]); got != "RIFF" {
		t.Fatalf("RIFF magic = %q, want %q", got, "RIFF")
	}
	if got := binary.LittleEndian.Uint32(hdr[4:8]); got != 36+payloadSize {
		t.Fatalf("RIFF chunk size = %d, want %d", got, 36+payloadSize)
	}
	if got := string(hdr[8:12]); got != "WAVE" {
		t.Fatalf("WAVE magic = %q, want %q", got, "WAVE")
	}

	if got := string(hdr[12:16]); got != "fmt " {
		t.Fatalf("fmt magic = %q, want %q", got, "fmt ")
	}
	if got := binary.LittleEndian.Uint32(hdr[16:20]); got != 16 {
		t.Fatalf("fmt subchunk size = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint16(hdr[20:22]); got != 1 {
		t.Fatalf("audio format = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(hdr[22:24]); got != 1 {
		t.Fatalf("channels = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[24:28]); got != 16000 {
		t.Fatalf("sample rate = %d, want 16000", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[28:32]); got != 32000 {
		t.Fatalf("byte rate = %d, want 32000", got)
	}
	if got := binary.LittleEndian.Uint16(hdr[32:34]); got != 2 {
		t.Fatalf("block align = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(hdr[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}

	if got := string(hdr[36:40]); got != "data" {
		t.Fatalf("data magic = %q, want %q", got, "data")
	}
	if got := binary.LittleEndian.Uint32(hdr[40:44]); got != payloadSize {
		t.Fatalf("data chunk size = %d, want %d", got, payloadSize)
	}
}

func TestWAVHeader_EmptyPayload(t *testing.T) {
	hdr := writeWAVHeader(0)

	if got := string(hdr[0:4]); got != "RIFF" {
		t.Fatalf("RIFF magic = %q", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[4:8]); got != 36 {
		t.Fatalf("RIFF chunk size for empty payload = %d, want 36", got)
	}
	if got := string(hdr[8:12]); got != "WAVE" {
		t.Fatalf("WAVE magic = %q", got)
	}
	if got := string(hdr[36:40]); got != "data" {
		t.Fatalf("data magic = %q", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[40:44]); got != 0 {
		t.Fatalf("data size for empty payload = %d, want 0", got)
	}
}

func TestWriteWAV_ProducesParseableFile(t *testing.T) {
	// 1 second of a 440 Hz sine at 16 kHz mono PCM16.
	const (
		sampleRate = 16000
		freq       = 440.0
		amplitude  = 0.25
		duration   = 1
	)
	samples := make([]byte, 0, sampleRate*duration*2)
	for i := 0; i < sampleRate*duration; i++ {
		v := math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)) * amplitude
		s := int16(v * math.MaxInt16)
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(s))
		samples = append(samples, b[:]...)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "sine.wav")
	if err := writeWAVFile(path, samples); err != nil {
		t.Fatalf("writeWAVFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back wav: %v", err)
	}
	if want := wavHeaderSize + len(samples); len(got) != want {
		t.Fatalf("file size = %d, want %d (header %d + payload %d)",
			len(got), want, wavHeaderSize, len(samples))
	}

	if !bytes.Equal(got[:4], []byte("RIFF")) {
		t.Fatalf("file does not start with RIFF: %q", got[:4])
	}
	if !bytes.Equal(got[8:12], []byte("WAVE")) {
		t.Fatalf("WAVE missing at offset 8: %q", got[8:12])
	}

	dataSize := binary.LittleEndian.Uint32(got[40:44])
	if int(dataSize) != len(samples) {
		t.Fatalf("data size in header = %d, want %d", dataSize, len(samples))
	}

	// Payload roundtrip
	if !bytes.Equal(got[wavHeaderSize:], samples) {
		t.Fatal("payload bytes differ after roundtrip")
	}
}

func TestWriteWAV_RejectsUnalignedPayload(t *testing.T) {
	// Mono PCM16 has 2-byte block align; an odd-length payload is illegal.
	var buf bytes.Buffer
	err := writeWAV(&buf, []byte{0x00, 0x01, 0x02})
	if err == nil {
		t.Fatal("expected error for unaligned payload, got nil")
	}
}
