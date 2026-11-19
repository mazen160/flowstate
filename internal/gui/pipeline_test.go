package gui

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// fakeGroq returns a server speaking both Groq endpoints with canned
// payloads, so Process runs end-to-end with no network and no mic.
func fakeGroq(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello world","segments":[]}`))
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Hello world."}}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestProcess_FullPipeline drives Process (trim off, save on) against the
// fake Groq: any audio bytes in, canned transcript out, artifacts on disk.
// No microphone, no window.
func TestProcess_FullPipeline(t *testing.T) {
	base := fakeGroq(t)
	storage := t.TempDir()
	wav := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(wav, []byte("fake-audio-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := Process(context.Background(), wav, PipelineOpts{
		APIKey:             "test",
		BaseURL:            base,
		TranscriptionModel: "whisper-large-v3",
		CleanupModel:       "model",
		FallbackModel:      "other",
		SystemPrompt:       "prompt",
		TrimSilence:        false,
		SaveRecordings:     true,
		StorageDir:         storage,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Raw != "hello world" {
		t.Fatalf("Raw = %q; want %q", res.Raw, "hello world")
	}
	if res.Cleaned != "Hello world." {
		t.Fatalf("Cleaned = %q; want %q", res.Cleaned, "Hello world.")
	}
	if res.RecordingDir == "" {
		t.Fatal("RecordingDir empty with SaveRecordings=true")
	}
	for _, f := range []string{"original.wav", "modified.wav", "raw.txt", "cleaned.txt"} {
		if _, err := os.Stat(filepath.Join(res.RecordingDir, f)); err != nil {
			t.Fatalf("artifact %s missing: %v", f, err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(res.RecordingDir, "cleaned.txt")); err != nil || string(b) != "Hello world." {
		t.Fatalf("cleaned.txt = %q, err = %v", b, err)
	}
}

// TestProcess_SilentAudio maps empty-pipeline audio to an empty result
// (the caller's "(no speech detected)" path), not an error.
func TestProcess_SilentAudio(t *testing.T) {
	_ = fakeGroq(t)
	wav := filepath.Join(t.TempDir(), "clip.wav")
	// 2s of digital silence trims to Empty without contacting the
	// network at all.
	silenceWAV(t, wav, 2)
	res, err := Process(context.Background(), wav, PipelineOpts{
		APIKey:         "test",
		BaseURL:        "http://127.0.0.1:1",
		SystemPrompt:   "prompt",
		TrimSilence:    true,
		SaveRecordings: false,
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Cleaned != "" || res.Raw != "" {
		t.Fatalf("silent audio should yield empty result, got %+v", res)
	}
}

// TestRecentRecordings_ListsNewestFirst seeds two artifact dirs and checks
// ordering plus preview extraction.
// silenceWAV writes seconds of all-zero PCM16 mono 16kHz WAV — a valid
// silent recording for the trim-to-empty path.
func silenceWAV(t *testing.T, path string, seconds int) {
	t.Helper()
	dataLen := 16000 * seconds * 2
	buf := make([]byte, 44+dataLen)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataLen))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1)     // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)     // mono
	binary.LittleEndian.PutUint32(buf[24:28], 16000) // rate
	binary.LittleEndian.PutUint32(buf[28:32], 32000) // byte rate
	binary.LittleEndian.PutUint16(buf[32:34], 2)     // block align
	binary.LittleEndian.PutUint16(buf[34:36], 16)    // bits
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataLen))
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRecentRecordings_ListsNewestFirst(t *testing.T) {
	root := t.TempDir()
	mk := func(name, cleaned string) {
		d := filepath.Join(root, name)
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cleaned.txt"), []byte(cleaned), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("20240101T000000.000000000Z-aaa", "first words here")
	mk("20240201T000000.000000000Z-bbb", "second words here")

	got := RecentRecordings(root, 10)
	if len(got) != 2 {
		t.Fatalf("len = %d; want 2", len(got))
	}
	if got[0].Preview != "second words here" || got[1].Preview != "first words here" {
		t.Fatalf("order/preview wrong: %+v", got)
	}
	if RecentRecordings(filepath.Join(root, "missing"), 10) != nil {
		t.Fatal("missing root should yield nil, not an error row")
	}
}

// TestProcess_CleanupFailureKeepsRaw pins the fix for a lost take: when the
// cleanup call fails, the transcript the user spoke must still come back so
// the window can show it.
func TestProcess_CleanupFailureKeepsRaw(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"the words i spoke","segments":[]}`))
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wav := filepath.Join(t.TempDir(), "clip.wav")
	if err := os.WriteFile(wav, []byte("fake-audio-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Process(context.Background(), wav, PipelineOpts{
		APIKey:             "test",
		BaseURL:            srv.URL,
		TranscriptionModel: "whisper-large-v3",
		CleanupModel:       "model",
		SystemPrompt:       "prompt",
	})
	if err == nil {
		t.Fatal("expected a cleanup error")
	}
	if res.Raw != "the words i spoke" {
		t.Fatalf("Raw = %q after cleanup failure; want the spoken transcript", res.Raw)
	}
}

// TestPreviewText_DoesNotSplitRunes guards the history row preview against
// cutting a multi-byte character in half.
func TestPreviewText_DoesNotSplitRunes(t *testing.T) {
	long := strings.Repeat("é", 200)
	got := previewText(long)
	if !utf8.ValidString(got) {
		t.Fatalf("preview is not valid UTF-8: %q", got)
	}
}
