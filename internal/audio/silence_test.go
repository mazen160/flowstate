package audio

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

func pcmWAV(t *testing.T, samples []int16) []byte {
	t.Helper()
	pcm := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	var out bytes.Buffer
	if err := writeWAV(&out, pcm); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTrimSilenceBoundaries(t *testing.T) {
	for _, frames := range []int{15999, 16000, 16001, 32000} {
		for _, position := range []string{"leading", "middle", "trailing", "all"} {
			t.Run(position+"/"+time.Duration(frames).String(), func(t *testing.T) {
				samples := make([]int16, frames)
				var speech []int16
				if position == "middle" || position == "trailing" {
					samples = append([]int16{1000, -1000}, samples...)
					speech = append(speech, 1000, -1000)
				}
				if position == "leading" || position == "middle" {
					samples = append(samples, 2000, -2000)
					speech = append(speech, 2000, -2000)
				}
				original := pcmWAV(t, samples)
				snapshot := bytes.Clone(original)
				got, err := TrimSilence(original)
				if err != nil {
					t.Fatal(err)
				}
				want := original
				var removed time.Duration
				if frames > 16000 {
					want = pcmWAV(t, speech)
					removed = time.Duration(frames) * time.Second / 16000
				}
				if !bytes.Equal(got.WAV, want) || got.Removed != removed || got.Empty != (len(want) == 44) {
					t.Fatalf("got length=%d removed=%s empty=%v; want length=%d removed=%s", len(got.WAV), got.Removed, got.Empty, len(want), removed)
				}
				if !bytes.Equal(original, snapshot) {
					t.Fatal("original was modified")
				}
			})
		}
	}
}

func TestTrimSilenceMultipleGapsAndQuietSpeech(t *testing.T) {
	samples := []int16{-32768, 32767}
	samples = append(samples, make([]int16, 20000)...)
	samples = append(samples, 33, -33)
	samples = append(samples, make([]int16, 16000)...)
	samples = append(samples, 100)
	for i := 0; i < 20000; i++ {
		samples = append(samples, 32)
	}
	samples = append(samples, -100)
	got, err := TrimSilence(pcmWAV(t, samples))
	if err != nil {
		t.Fatal(err)
	}
	want := []int16{-32768, 32767, 33, -33}
	want = append(want, make([]int16, 16000)...)
	want = append(want, 100, -100)
	if !bytes.Equal(got.WAV, pcmWAV(t, want)) || got.Removed != 2500*time.Millisecond {
		t.Fatalf("short pauses or quiet speech changed; removed=%s", got.Removed)
	}
}

func TestTrimSilenceStereoAndMetadata(t *testing.T) {
	// A signal in only the right channel must prevent silence detection.
	samples := make([]int16, 22050*2*2)
	for i := 1; i < len(samples); i += 2 {
		samples[i] = 100
	}
	wav := pcmWAV(t, samples)
	binary.LittleEndian.PutUint16(wav[22:], 2)
	binary.LittleEndian.PutUint32(wav[24:], 22050)
	binary.LittleEndian.PutUint32(wav[28:], 22050*4)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	// Add an odd-sized, padded metadata chunk before data.
	withMetadata := append(bytes.Clone(wav[:36]), []byte{'J', 'U', 'N', 'K', 1, 0, 0, 0, 7, 0}...)
	withMetadata = append(withMetadata, wav[36:]...)
	binary.LittleEndian.PutUint32(withMetadata[4:], uint32(len(withMetadata)-8))
	got, err := TrimSilence(withMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if got.Removed != 0 || !bytes.Equal(got.WAV, wav) {
		t.Fatal("stereo audio changed")
	}
	clear(wav[44:])
	got, err = TrimSilence(wav)
	if err != nil || !got.Empty || got.Removed != 2*time.Second {
		t.Fatalf("stereo silence: %+v, %v", got, err)
	}
}

func TestTrimSilenceRejectsMalformedWAV(t *testing.T) {
	wav := pcmWAV(t, []int16{1, 2})
	cases := [][]byte{nil, []byte("compressed audio"), wav[:40], bytes.Clone(wav), bytes.Clone(wav), bytes.Clone(wav), bytes.Clone(wav)}
	binary.LittleEndian.PutUint16(cases[3][20:], 3)    // float audio
	binary.LittleEndian.PutUint32(cases[4][40:], 1000) // truncated chunk
	binary.LittleEndian.PutUint32(cases[5][24:], 0)    // zero sample rate
	binary.LittleEndian.PutUint16(cases[6][32:], 0)    // bad frame size
	for i, input := range cases {
		if _, err := TrimSilence(input); err == nil {
			t.Fatalf("case %d accepted invalid WAV", i)
		}
	}
}
