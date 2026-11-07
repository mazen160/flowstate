package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wavWithGap() ([]byte, []byte) {
	header := bytes.Clone(minimalWAV()[:44])
	pcm := make([]byte, 64004)
	binary.LittleEndian.PutUint16(pcm, 1000)
	binary.LittleEndian.PutUint16(pcm[len(pcm)-2:], 2000)
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	wav := append(bytes.Clone(header), pcm...)
	binary.LittleEndian.PutUint32(header[4:], 40)
	binary.LittleEndian.PutUint32(header[40:], 4)
	return wav, append(header, []byte{0xe8, 0x03, 0xd0, 0x07}...)
}

func recordingRequest(t *testing.T, route string, wav, original []byte, trim string, save ...string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("audio", "recording.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(wav); err != nil {
		t.Fatal(err)
	}
	if original != nil {
		part, err := w.CreateFormFile("original", "../../original.webm")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(original); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteField("trim_silence", trim); err != nil {
		t.Fatal(err)
	}
	if len(save) > 0 {
		if err := w.WriteField("save_recordings", save[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, route, &body)
	r.Header.Set("Content-Type", w.FormDataContentType())
	return r
}

func TestServerOptionalRecordingStorage(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	wav, trimmed := wavWithGap()
	for _, route := range []string{"/api/transcribe", "/api/record"} {
		for _, tc := range []struct {
			name           string
			disableDefault bool
			override       string
			wantSaved      bool
		}{
			{"default on", false, "", true},
			{"default off", true, "", false},
			{"turn off", false, "false", false},
			{"turn on", true, "true", true},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				s := newTestServer(t, upstream.URL, "")
				s.opts.StorageDir = filepath.Join(t.TempDir(), ".flowstate")
				s.opts.DisableStorage = tc.disableDefault
				w := httptest.NewRecorder()
				s.handler.ServeHTTP(w, recordingRequest(t, route, wav, []byte("original bytes"), "true", tc.override))
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d: %s", w.Code, w.Body)
				}
				var out struct {
					Audio        []byte `json:"audio"`
					RecordingDir string `json:"recording_dir"`
					Removed      int64  `json:"removed_silence_ms"`
					Raw          string `json:"raw"`
					Cleaned      string `json:"cleaned"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.Removed != 2000 {
					t.Fatalf("trimming stopped working: removed=%d", out.Removed)
				}
				if route == "/api/record" && !bytes.Equal(out.Audio, trimmed) {
					t.Fatal("replay audio differs")
				}
				if route == "/api/transcribe" && (out.Raw != "hello there" || out.Cleaned != "Hello, there.") {
					t.Fatalf("transcription failed: %+v", out)
				}
				if tc.wantSaved {
					if out.RecordingDir == "" {
						t.Fatal("recording was not saved")
					}
					if _, err := os.Stat(filepath.Join(out.RecordingDir, "original.webm")); err != nil {
						t.Fatal(err)
					}
				} else {
					if out.RecordingDir != "" {
						t.Fatalf("unexpected archive: %q", out.RecordingDir)
					}
					if _, err := os.Stat(s.opts.StorageDir); !os.IsNotExist(err) {
						t.Fatalf("disabled storage touched the recording folder: %v", err)
					}
				}
				info := httptest.NewRecorder()
				s.handler.ServeHTTP(info, httptest.NewRequest(http.MethodGet, "/api/info", nil))
				var defaults infoResponse
				if err := json.Unmarshal(info.Body.Bytes(), &defaults); err != nil {
					t.Fatal(err)
				}
				if defaults.SaveRecordings == tc.disableDefault {
					t.Fatal("incorrect storage default in /api/info")
				}
			})
		}
	}
}

func TestServerInvalidStoragePreferenceDoesNotWrite(t *testing.T) {
	s := New(Options{StorageDir: filepath.Join(t.TempDir(), ".flowstate")})
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, recordingRequest(t, "/api/record", minimalWAV(), nil, "false", "invalid"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d: %s", w.Code, w.Body)
	}
	if _, err := os.Stat(s.opts.StorageDir); !os.IsNotExist(err) {
		t.Fatalf("invalid preference created an archive: %v", err)
	}
}

func TestServerArchivesTranscriptionStages(t *testing.T) {
	for _, failCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "cleanup failure"}[failCleanup], func(t *testing.T) {
			wav, modified := wavWithGap()
			var uploaded []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/audio/transcriptions") {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						return
					}
					defer removeMultipart(r)
					file, _, err := r.FormFile("file")
					if err != nil {
						t.Error(err)
						return
					}
					defer file.Close()
					uploaded, _ = io.ReadAll(file)
					_, _ = io.WriteString(w, `{"text":"um hello"}`)
				} else if failCleanup {
					http.Error(w, "failed", http.StatusInternalServerError)
				} else {
					_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Hello."}}]}`)
				}
			}))
			defer upstream.Close()
			s := newTestServer(t, upstream.URL, "")
			original := []byte("exact original MediaRecorder bytes")
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, recordingRequest(t, "/api/transcribe", wav, original, "true"))
			wantStatus := http.StatusOK
			if failCleanup {
				wantStatus = http.StatusInternalServerError
			}
			if w.Code != wantStatus {
				t.Fatalf("status=%d: %s", w.Code, w.Body)
			}
			if !bytes.Equal(uploaded, modified) {
				t.Fatal("provider did not receive trimmed audio")
			}
			dirs, err := os.ReadDir(s.opts.StorageDir)
			if err != nil || len(dirs) != 1 {
				t.Fatalf("recording dirs: %v, %v", dirs, err)
			}
			dir := filepath.Join(s.opts.StorageDir, dirs[0].Name())
			for name, want := range map[string][]byte{"original.webm": original, "modified.wav": modified, "raw.txt": []byte("um hello")} {
				got, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s: got=%q, err=%v", name, got, err)
				}
			}
			if !failCleanup {
				got, err := os.ReadFile(filepath.Join(dir, "cleaned.txt"))
				if err != nil || string(got) != "Hello." {
					t.Fatalf("cleaned text: %q, %v", got, err)
				}
				var out transcribeResponse
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if out.RemovedSilenceMS != 2000 || out.RecordingDir != dir {
					t.Fatalf("archive response: %+v", out)
				}
			}
		})
	}
}

func TestServerMicRecordUsesArchiveAndTrimming(t *testing.T) {
	wav, trimmed := wavWithGap()
	for _, trim := range []string{"true", "false"} {
		t.Run(trim, func(t *testing.T) {
			s := New(Options{StorageDir: t.TempDir(), TrimSilence: true})
			w := httptest.NewRecorder()
			s.handler.ServeHTTP(w, recordingRequest(t, "/api/record", wav, nil, trim))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", w.Code, w.Body)
			}
			var out struct {
				Audio        []byte `json:"audio"`
				RecordingDir string `json:"recording_dir"`
				Removed      int64  `json:"removed_silence_ms"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			want, removed := wav, int64(0)
			if trim == "true" {
				want, removed = trimmed, 2000
			}
			if !bytes.Equal(out.Audio, want) || out.Removed != removed {
				t.Fatal("replay audio or duration is incorrect")
			}
			original, err := os.ReadFile(filepath.Join(out.RecordingDir, "original.wav"))
			if err != nil || !bytes.Equal(original, wav) {
				t.Fatalf("original changed: %v", err)
			}
		})
	}
}

func TestServerEntirelySilentAudioSkipsProvider(t *testing.T) {
	wav, _ := wavWithGap()
	clear(wav[44:])
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("silent audio sent to provider") }))
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "")
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, recordingRequest(t, "/api/transcribe", wav, nil, "true"))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d: %s", w.Code, w.Body)
	}
	var out transcribeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Raw != "" || out.Cleaned != "" {
		t.Fatalf("silent response: %+v", out)
	}
	for _, name := range []string{"raw.txt", "cleaned.txt"} {
		data, err := os.ReadFile(filepath.Join(out.RecordingDir, name))
		if err != nil || len(data) != 0 {
			t.Fatalf("%s: %q, %v", name, data, err)
		}
	}
}

func TestServerRecordRequiresAuthAndEnforcesUploadLimit(t *testing.T) {
	s := New(Options{StorageDir: t.TempDir(), Token: "secret"})
	w := httptest.NewRecorder()
	s.handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/record", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", w.Code)
	}
	request := recordingRequest(t, "/api/record", make([]byte, maxUploadBytes+1), nil, "false")
	request.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	s.handler.ServeHTTP(w, request)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize status=%d: %s", w.Code, w.Body)
	}
	dirs, err := os.ReadDir(s.opts.StorageDir)
	if err != nil || len(dirs) != 0 {
		t.Fatalf("rejected request wrote files: %v, %v", dirs, err)
	}
}
