package transcribe

import (
	"context"
	"encoding/binary"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestWAV creates a tiny valid-ish WAV file (44-byte header + a handful
// of zero samples) in t.TempDir() and returns its path. The bytes never
// reach a real decoder so the contents don't matter beyond being non-empty.
func writeTestWAV(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.wav")

	var hdr [44]byte
	copy(hdr[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(hdr[4:8], 36+8)
	copy(hdr[8:12], []byte("WAVE"))
	copy(hdr[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(hdr[16:20], 16)
	binary.LittleEndian.PutUint16(hdr[20:22], 1)
	binary.LittleEndian.PutUint16(hdr[22:24], 1)
	binary.LittleEndian.PutUint32(hdr[24:28], 16000)
	binary.LittleEndian.PutUint32(hdr[28:32], 32000)
	binary.LittleEndian.PutUint16(hdr[32:34], 2)
	binary.LittleEndian.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(hdr[40:44], 8)

	body := append(hdr[:], 0, 0, 0, 0, 0, 0, 0, 0)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write test wav: %v", err)
	}
	return path
}

// newTestClient wires a Client to point at srv.URL. Tests can override Model
// and Language via the returned Options pointer before calling Transcribe
// indirectly through the closure pattern below.
func newTestClient(srv *httptest.Server, language string) *Client {
	return NewClient(Options{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		Model:      "whisper-large-v3",
		Language:   language,
		HTTPClient: srv.Client(),
	})
}

func TestTranscribe_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello world","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "en")
	got, err := c.Transcribe(context.Background(), writeTestWAV(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Transcribe() = %q; want %q", got, "hello world")
	}
}

func TestTranscribe_HallucinationFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"Thank you.","segments":[{"no_speech_prob":0.5}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	got, err := c.Transcribe(context.Background(), writeTestWAV(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "" {
		t.Errorf("Transcribe() = %q; want \"\" (hallucination filtered)", got)
	}
}

func TestTranscribe_UnauthorizedMapsToFriendly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	_, err := c.Transcribe(context.Background(), writeTestWAV(t))
	if err == nil {
		t.Fatal("Transcribe: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error = %q; want substring 'Invalid API key'", err.Error())
	}
}

func TestTranscribe_413Friendly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	_, err := c.Transcribe(context.Background(), writeTestWAV(t))
	if err == nil {
		t.Fatal("Transcribe: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Audio too large") {
		t.Errorf("error = %q; want substring 'Audio too large'", err.Error())
	}
}

// TestTranscribe_MultipartFieldOrder uses mime/multipart.NewReader to walk
// the incoming request body and verify the field order matches the spec:
// model, response_format, language, file (when language is set).
func TestTranscribe_MultipartFieldOrder(t *testing.T) {
	var observedOrder []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		_, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Errorf("parse Content-Type %q: %v", ct, err)
			http.Error(w, "bad ct", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			observedOrder = append(observedOrder, part.FormName())
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"ok","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "en")
	if _, err := c.Transcribe(context.Background(), writeTestWAV(t)); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	want := []string{"model", "response_format", "language", "file"}
	if len(observedOrder) != len(want) {
		t.Fatalf("multipart fields = %v; want %v", observedOrder, want)
	}
	for i, name := range want {
		if observedOrder[i] != name {
			t.Errorf("field[%d] = %q; want %q (full order: %v)",
				i, observedOrder[i], name, observedOrder)
		}
	}
}

// TestTranscribe_NoLanguage_OmitsField confirms that when Options.Language
// is empty, the request multipart body contains no "language" form field at
// all. Field order must collapse to model, response_format, file.
func TestTranscribe_NoLanguage_OmitsField(t *testing.T) {
	var observedOrder []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		_, params, _ := mime.ParseMediaType(ct)
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			observedOrder = append(observedOrder, part.FormName())
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"ok","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	if _, err := c.Transcribe(context.Background(), writeTestWAV(t)); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	for _, name := range observedOrder {
		if name == "language" {
			t.Errorf("multipart contained 'language' field; want it omitted. Full order: %v", observedOrder)
		}
	}

	want := []string{"model", "response_format", "file"}
	if len(observedOrder) != len(want) {
		t.Fatalf("multipart fields = %v; want %v", observedOrder, want)
	}
	for i, name := range want {
		if observedOrder[i] != name {
			t.Errorf("field[%d] = %q; want %q", i, observedOrder[i], name)
		}
	}
}

func TestTranscribe_AuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"ok","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "en")
	if _, err := c.Transcribe(context.Background(), writeTestWAV(t)); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	want := "Bearer test-key"
	if gotAuth != want {
		t.Errorf("Authorization header = %q; want %q", gotAuth, want)
	}
}

// TestTranscribe_NonJSONBody confirms the plain-text fallback: when the
// provider returns 200 with non-JSON, we collapse newlines to spaces and
// trim.
func TestTranscribe_NonJSONBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello\nworld\n")
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	got, err := c.Transcribe(context.Background(), writeTestWAV(t))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "hello world" {
		t.Errorf("Transcribe() = %q; want %q", got, "hello world")
	}
}

// TestTranscribeReader_WebM_ContentType confirms that TranscribeReader
// honors the upload filename's extension when picking the multipart "file"
// part Content-Type. A "recording.webm" upload must advertise audio/webm so
// Groq's container sniff picks the right demuxer. This is the path the
// `flowstate web` HTTP server exercises for browser MediaRecorder output.
func TestTranscribeReader_WebM_ContentType(t *testing.T) {
	var fileCT, fileName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			if part.FormName() == "file" {
				fileCT = part.Header.Get("Content-Type")
				fileName = part.FileName()
			}
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	// 100 bytes of arbitrary audio payload; Groq is mocked so contents don't
	// matter beyond ensuring the multipart body is well-formed.
	payload := strings.NewReader(strings.Repeat("a", 100))
	got, err := c.TranscribeReader(context.Background(), payload, "recording.webm")
	if err != nil {
		t.Fatalf("TranscribeReader: %v", err)
	}
	if got != "hello" {
		t.Errorf("TranscribeReader() = %q; want %q", got, "hello")
	}
	if fileCT != "audio/webm" {
		t.Errorf("file part Content-Type = %q; want %q", fileCT, "audio/webm")
	}
	if fileName != "recording.webm" {
		t.Errorf("file part filename = %q; want %q", fileName, "recording.webm")
	}
}

// TestTranscribe_FileContentType confirms the multipart "file" part carries
// Content-Type: audio/wav. The provider contract pins this header.
func TestTranscribe_FileContentType(t *testing.T) {
	var fileCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("NextPart: %v", err)
				return
			}
			if part.FormName() == "file" {
				fileCT = part.Header.Get("Content-Type")
			}
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"ok","segments":[{"no_speech_prob":0.01}]}`)
	}))
	defer srv.Close()

	c := newTestClient(srv, "")
	if _, err := c.Transcribe(context.Background(), writeTestWAV(t)); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if fileCT != "audio/wav" {
		t.Errorf("file part Content-Type = %q; want %q", fileCT, "audio/wav")
	}
}
