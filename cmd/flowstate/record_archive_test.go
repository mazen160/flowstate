package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordArchivesAndTranscribesModifiedAudio(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "test-key")
	for _, tc := range []struct{ trim, save bool }{{true, true}, {false, true}, {true, false}, {false, false}} {
		t.Run(fmt.Sprintf("trim=%v/save=%v", tc.trim, tc.save), func(t *testing.T) {
			dir := t.TempDir()
			pcm := make([]byte, 64004)
			binary.LittleEndian.PutUint16(pcm, 1000)
			binary.LittleEndian.PutUint16(pcm[len(pcm)-2:], 1000)
			var wav bytes.Buffer
			wav.WriteString("RIFF")
			_ = binary.Write(&wav, binary.LittleEndian, uint32(36+len(pcm)))
			wav.WriteString("WAVEfmt ")
			for _, value := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
				if err := binary.Write(&wav, binary.LittleEndian, value); err != nil {
					t.Fatal(err)
				}
			}
			wav.WriteString("data")
			_ = binary.Write(&wav, binary.LittleEndian, uint32(len(pcm)))
			wav.Write(pcm)
			source := filepath.Join(dir, "source.wav")
			if err := os.WriteFile(source, wav.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			var uploaded []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						return
					}
					defer r.MultipartForm.RemoveAll()
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						return
					}
					defer file.Close()
					uploaded, _ = io.ReadAll(file)
					_, _ = io.WriteString(w, `{"text":"um hello"}`)
				} else {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Hello."}}]}`)
				}
			}))
			defer upstream.Close()
			cfgPath := filepath.Join(dir, "config.toml")
			body := fmt.Sprintf("base_url = %q\nmute_while_recording = false\noutput_mode = \"stdout\"\n[prompts]\ndefault = \"Test\"\n", upstream.URL)
			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			storageDir := filepath.Join(dir, ".flowstate")
			var stdout, stderr bytes.Buffer
			args := []string{"--config", cfgPath, "--wav-source", source, "--storage-dir", storageDir, fmt.Sprintf("--trim-silence=%v", tc.trim)}
			if !tc.save {
				args = append(args, "--save-recordings=false")
			}
			code := runRecord(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stdout.String() != "Hello.\n" {
				t.Fatalf("code=%d stdout=%q stderr=%s", code, stdout.String(), stderr.String())
			}
			wantSize := len(wav.Bytes())
			if tc.trim {
				wantSize = 48
			}
			if len(uploaded) != wantSize {
				t.Fatalf("uploaded %d bytes, want %d", len(uploaded), wantSize)
			}
			if tc.save {
				dirs, err := os.ReadDir(storageDir)
				if err != nil || len(dirs) != 1 {
					t.Fatalf("archive dirs: %v, %v", dirs, err)
				}
				for name, want := range map[string][]byte{"original.wav": wav.Bytes(), "modified.wav": uploaded, "raw.txt": []byte("um hello"), "cleaned.txt": []byte("Hello.")} {
					got, err := os.ReadFile(filepath.Join(storageDir, dirs[0].Name(), name))
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("artifact %s differs: %v", name, err)
					}
				}
			} else {
				if _, err := os.Stat(storageDir); !os.IsNotExist(err) {
					t.Fatalf("disabled storage touched recording folder: %v", err)
				}
				if strings.Contains(stderr.String(), "Saved recording") {
					t.Fatal("disabled storage reported a saved recording")
				}
			}
			got, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(got, wav.Bytes()) {
				t.Fatal("source file was removed or changed")
			}
		})
	}
}
