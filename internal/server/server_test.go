package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mazin-ahmed/flowstate/internal/cleanup"
	"github.com/mazin-ahmed/flowstate/internal/transcribe"
)

// fakeGroq returns an httptest.Server that mimics the two Groq endpoints
// the transcribe + cleanup packages call. Both upstream calls are answered
// with canned JSON so the server-side handler can exercise the full
// round-trip without touching the real network.
//
// The handler is intentionally permissive about the multipart layout —
// the goal is to give the server a happy-path upstream, not to validate
// the multipart spec end-to-end (that's transcribe's own test).
func fakeGroq(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/audio/transcriptions"):
			_, _ = io.WriteString(w, `{"text":"hello there","segments":[{"no_speech_prob":0.01}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"Hello, there."}}]}`)
		default:
			http.Error(w, "unexpected upstream path: "+r.URL.Path, http.StatusBadRequest)
		}
	}))
}

// newTestServer wires up a Server that delegates Groq calls to upstream.
// Token may be empty (auth off) or non-empty (auth required).
func newTestServer(t *testing.T, upstreamURL, token string) *Server {
	t.Helper()
	tx := transcribe.NewClient(transcribe.Options{
		APIKey:  "test-key",
		BaseURL: upstreamURL,
		Model:   "whisper-large-v3",
	})
	cl := cleanup.NewClient(cleanup.Options{
		APIKey:  "test-key",
		BaseURL: upstreamURL,
		Model:   "openai/gpt-oss-20b",
	})
	return New(Options{
		ListenHost:   "127.0.0.1",
		ListenPort:   0,
		Token:        token,
		Transcribe:   tx,
		Cleanup:      cl,
		SystemPrompt: "test prompt",
		Version:      "test",
		Stderr:       &bytes.Buffer{},
	})
}

// minimalWAV builds a 44-byte WAV header + 8 zero PCM samples. The bytes
// never reach a real decoder (Groq is mocked) but supplying a plausible
// container header keeps the multipart payload realistic.
func minimalWAV() []byte {
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	buf.Write([]byte{36 + 8, 0, 0, 0})
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	buf.Write([]byte{16, 0, 0, 0})          // chunk size
	buf.Write([]byte{1, 0, 1, 0})           // PCM, mono
	buf.Write([]byte{0x80, 0x3e, 0, 0})     // 16000 Hz
	buf.Write([]byte{0x00, 0x7d, 0, 0})     // 32000 byte rate
	buf.Write([]byte{2, 0, 16, 0})          // block align, bits
	buf.WriteString("data")
	buf.Write([]byte{8, 0, 0, 0})
	buf.Write(make([]byte, 8))
	return buf.Bytes()
}

// buildMultipart wraps audio bytes in a multipart/form-data body suitable
// for POST /api/transcribe. Returns the body, the Content-Type header, and
// the boundary used.
func buildMultipart(t *testing.T, filename string, audio []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("audio", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := part.Write(audio); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := w.WriteField("session_id", "test-session"); err != nil {
		t.Fatalf("write session_id: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// startListening starts the Server on a random port and returns a base URL
// like http://127.0.0.1:54321. The returned cancel func must be called from
// the test to unblock ListenAndServe and release the port.
//
// Uses Server.WaitReady to block until the listener is bound. The earlier
// version of this helper polled s.opts.ListenPort, which the race detector
// correctly flagged because ListenAndServe was concurrently writing it.
// WaitReady is the race-safe path: it selects on a chan-struct{} that
// ListenAndServe closes under sync.Once after the bind succeeds.
func startListening(t *testing.T, s *Server) (string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	errCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		errCh <- s.ListenAndServe(ctx)
	}()

	readyCtx, readyCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readyCancel()
	addr, err := s.WaitReady(readyCtx)
	if err != nil {
		cancel()
		<-errCh
		t.Fatalf("server never came up: %v", err)
	}
	base := "http://" + addr

	t.Cleanup(func() {
		cancel()
		<-errCh
		wg.Wait()
	})
	return base, cancel
}

// TestServer_HealthOK exercises the bare-minimum readiness endpoint.
// A successful response confirms the listener bound, the routes wired,
// and the JSON encoder is happy.
func TestServer_HealthOK(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "")
	base, _ := startListening(t, s)

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !body.OK || body.Version != "test" {
		t.Fatalf("health response = %+v; want ok=true version=test", body)
	}
}

// TestServer_TranscribeRoundtrip is the happy-path smoke test for the API.
// A valid multipart upload should produce the canned cleaned text from the
// fake upstream — and the JSON shape should match what the frontend
// expects.
func TestServer_TranscribeRoundtrip(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "")
	base, _ := startListening(t, s)

	body, ct := buildMultipart(t, "audio.wav", minimalWAV())
	req, err := http.NewRequest(http.MethodPost, base+"/api/transcribe", body)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", ct)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/transcribe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, raw)
	}
	var out transcribeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Raw != "hello there" {
		t.Errorf("raw = %q; want %q", out.Raw, "hello there")
	}
	if out.Cleaned != "Hello, there." {
		t.Errorf("cleaned = %q; want %q", out.Cleaned, "Hello, there.")
	}
}

// TestServer_TokenAuthEnforced_401 verifies that requests without (or with
// a wrong) Bearer token are rejected with 401 when the server was
// constructed with a token.
func TestServer_TokenAuthEnforced_401(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	body, ct := buildMultipart(t, "audio.wav", minimalWAV())
	req, _ := http.NewRequest(http.MethodPost, base+"/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	// No Authorization header.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", resp.StatusCode)
	}

	// Wrong token also fails.
	body2, ct2 := buildMultipart(t, "audio.wav", minimalWAV())
	req2, _ := http.NewRequest(http.MethodPost, base+"/api/transcribe", body2)
	req2.Header.Set("Content-Type", ct2)
	req2.Header.Set("Authorization", "Bearer wrong")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("POST wrong-token: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 for wrong token", resp2.StatusCode)
	}
}

// TestServer_TokenAuthEnforced_200 is the matching success case: the
// correct Bearer token unlocks the API.
func TestServer_TokenAuthEnforced_200(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	body, ct := buildMultipart(t, "audio.wav", minimalWAV())
	req, _ := http.NewRequest(http.MethodPost, base+"/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer secret")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s; want 200", resp.StatusCode, raw)
	}
}

// TestServer_NoTokenAcceptsAll verifies the no-op middleware behavior:
// when Options.Token is empty, every request reaches the handler.
func TestServer_NoTokenAcceptsAll(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "")
	base, _ := startListening(t, s)

	body, ct := buildMultipart(t, "audio.wav", minimalWAV())
	req, _ := http.NewRequest(http.MethodPost, base+"/api/transcribe", body)
	req.Header.Set("Content-Type", ct)
	// No Authorization header — should succeed because Token is empty.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s; want 200", resp.StatusCode, raw)
	}
}

// TestServer_IndexServesHTML confirms the embedded index.html is served at
// "/" with text/html and contains the brand string.
func TestServer_IndexServesHTML(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "")
	base, _ := startListening(t, s)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "flowstate") {
		t.Errorf("index body missing 'flowstate'; got %q", string(body))
	}
}

// TestServer_InfoAuthRequired surfaces whether auth is on in the JSON
// payload — the frontend uses this to decide whether to show the token
// input.
func TestServer_InfoAuthRequired(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	req, _ := http.NewRequest(http.MethodGet, base+"/api/info", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/info: %v", err)
	}
	defer resp.Body.Close()
	var info infoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if !info.AuthRequired {
		t.Errorf("auth_required = false; want true when Token is set")
	}
}
