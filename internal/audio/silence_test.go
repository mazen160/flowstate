package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
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

func TestTrimSilenceMicrophoneNoise(t *testing.T) {
	for _, rate := range []int{8000, 16000, 48000} {
		for _, channels := range []int{1, 2} {
			t.Run(fmt.Sprintf("%dHz/%dch", rate, channels), func(t *testing.T) {
				rng := rand.New(rand.NewSource(1))
				var samples []int16
				appendPart := func(ms, amplitude int) {
					for frame := 0; frame < rate*ms/1000; frame++ {
						for ch := range channels {
							sample := int16(rng.Intn(295) - 147) // ~-52 dBFS microphone noise
							if ch == channels-1 {
								sample += int16(float64(amplitude) * math.Sin(2*math.Pi*200*float64(frame)/float64(rate)))
							}
							samples = append(samples, sample)
						}
					}
				}
				appendPart(2000, 0)
				appendPart(100, 100) // quiet consonant before the louder voice
				appendPart(500, 1200)
				appendPart(100, 100) // quiet word ending
				appendPart(2000, 0)
				appendPart(100, 100)
				appendPart(500, 1200)
				appendPart(100, 100)
				appendPart(2000, 0)
				// A brief noise spike must not split the long middle pause.
				samples[rate*3700/1000*channels] += 1500
				wav := pcmWAV(t, samples)
				binary.LittleEndian.PutUint16(wav[22:], uint16(channels))
				binary.LittleEndian.PutUint32(wav[24:], uint32(rate))
				binary.LittleEndian.PutUint32(wav[28:], uint32(rate*channels*2))
				binary.LittleEndian.PutUint16(wav[32:], uint16(channels*2))
				snapshot := bytes.Clone(wav)
				got, err := TrimSilence(wav)
				if err != nil {
					t.Fatal(err)
				}
				// Retain both 700 ms utterances plus 50 ms of background on
				// each side: padding must protect the quiet consonants too.
				byteOffset := func(ms int) int { return 44 + rate*ms/1000*channels*2 }
				wantPCM := append(bytes.Clone(wav[byteOffset(1950):byteOffset(2750)]), wav[byteOffset(4650):byteOffset(5450)]...)
				if got.Empty || got.Removed != 5800*time.Millisecond || !bytes.Equal(got.WAV[44:], wantPCM) {
					t.Fatalf("noisy pauses or speech padding incorrect: removed=%s, bytes=%d", got.Removed, len(got.WAV))
				}
				if !bytes.Equal(got.WAV[20:36], wav[20:36]) || !bytes.Equal(wav, snapshot) {
					t.Fatal("audio format or original recording changed")
				}
			})
		}
	}
}

func TestTrimSilenceNoisyPauseBoundary(t *testing.T) {
	for _, gapMS := range []int{980, 1000, 1020} {
		t.Run(fmt.Sprint(gapMS), func(t *testing.T) {
			rng := rand.New(rand.NewSource(2))
			frames := 16000 * (1000 + gapMS) / 1000
			samples := make([]int16, frames)
			for i := range samples {
				samples[i] = int16(rng.Intn(295) - 147)
				if i < 8000 || i >= frames-8000 {
					samples[i] += int16(1200 * math.Sin(2*math.Pi*200*float64(i)/16000))
				}
			}
			wav := pcmWAV(t, samples)
			got, err := TrimSilence(wav)
			if err != nil {
				t.Fatal(err)
			}
			if gapMS <= 1000 {
				if got.Removed != 0 || !bytes.Equal(got.WAV, wav) {
					t.Fatal("a pause of one second or less was changed")
				}
			} else if got.Removed != 720*time.Millisecond {
				t.Fatalf("removed %s, want 720 ms with speech padding", got.Removed)
			}
		})
	}
}

func TestTrimSilenceRetainsUniformQuietSpeechAndLoudNoise(t *testing.T) {
	for _, amplitude := range []int16{33, 100, 1000} {
		t.Run(fmt.Sprint(amplitude), func(t *testing.T) {
			samples := make([]int16, 48000)
			for i := range samples {
				samples[i] = amplitude
				if i%2 == 0 {
					samples[i] = -amplitude
				}
			}
			wav := pcmWAV(t, samples)
			got, err := TrimSilence(wav)
			if err != nil || got.Empty || got.Removed != 0 || !bytes.Equal(got.WAV, wav) {
				t.Fatalf("uniform audio became its own noise floor: removed=%s, err=%v", got.Removed, err)
			}
		})
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
