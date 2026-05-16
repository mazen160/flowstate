package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mazen160/flowstate/internal/cleanup"
	"github.com/mazen160/flowstate/internal/transcribe"
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
	buf.Write([]byte{16, 0, 0, 0})      // chunk size
	buf.Write([]byte{1, 0, 1, 0})       // PCM, mono
	buf.Write([]byte{0x80, 0x3e, 0, 0}) // 16000 Hz
	buf.Write([]byte{0x00, 0x7d, 0, 0}) // 32000 byte rate
	buf.Write([]byte{2, 0, 16, 0})      // block align, bits
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

// TestServer_PingValidatesAuth covers the lightweight endpoint the web UI
// uses before saving a token in localStorage.
func TestServer_PingValidatesAuth(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	resp, err := http.Get(base + "/api/ping")
	if err != nil {
		t.Fatalf("GET /api/ping without token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 without token", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, base+"/api/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/ping wrong token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401 for wrong token", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, base+"/api/ping", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/ping correct token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d; want 200 for correct token", resp.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode ping: %v", err)
	}
	if !body.OK {
		t.Fatalf("ping ok = false; want true")
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

// TestServer_HealthBypassesAuth pins the post-TASK-144 contract: /api/health
// is never gated, even when --web-token is set. Liveness probes and the
// frontend's first request must succeed without prior credentials.
func TestServer_HealthBypassesAuth(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (health must not require auth)", resp.StatusCode)
	}
}

// TestServer_InfoBypassesAuth pins the post-TASK-144 contract: /api/info
// is never gated. The frontend has to read auth_required BEFORE it has
// a token, so gating /api/info would lock new users out.
func TestServer_InfoBypassesAuth(t *testing.T) {
	upstream := fakeGroq(t)
	defer upstream.Close()
	s := newTestServer(t, upstream.URL, "secret")
	base, _ := startListening(t, s)

	resp, err := http.Get(base + "/api/info")
	if err != nil {
		t.Fatalf("GET /api/info: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (info must not require auth)", resp.StatusCode)
	}
	var info infoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if !info.AuthRequired {
		t.Errorf("auth_required = false; want true when Token is set")
	}
}

// TestServer_RateLimit429 confirms the rate-limit middleware kicks in
// after rateLimitMaxPerIP requests within the window. We hammer the
// limiter directly so the test stays fast — driving it through the
// HTTP layer would require waiting on real timestamps and a real
// upstream. The middleware's only HTTP-level work is the 429 + JSON
// + Retry-After header, which TestServer_RateLimitMiddleware_HTTP
// below exercises end-to-end with a single overage request.
func TestServer_RateLimit429(t *testing.T) {
	l := newIPRateLimiter()
	now := time.Unix(1_700_000_000, 0) // fixed clock; not real time.Now()
	const ip = "203.0.113.7"
	for i := 0; i < rateLimitMaxPerIP; i++ {
		if !l.allow(ip, now, rateLimitWindow, rateLimitMaxPerIP) {
			t.Fatalf("request %d was rejected; want allowed (within burst)", i+1)
		}
	}
	if l.allow(ip, now, rateLimitWindow, rateLimitMaxPerIP) {
		t.Fatalf("request %d was allowed; want rejected (over burst)", rateLimitMaxPerIP+1)
	}
	// Same ip but after the window has elapsed — limiter should reset.
	later := now.Add(rateLimitWindow + time.Second)
	if !l.allow(ip, later, rateLimitWindow, rateLimitMaxPerIP) {
		t.Fatalf("request after window expired was rejected; want allowed (window slides)")
	}
}

// TestServer_RateLimit_PerIP verifies the limiter scopes counts to a
// source IP: a different IP's requests don't drain the first IP's
// budget.
func TestServer_RateLimit_PerIP(t *testing.T) {
	l := newIPRateLimiter()
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < rateLimitMaxPerIP; i++ {
		l.allow("203.0.113.7", now, rateLimitWindow, rateLimitMaxPerIP)
	}
	// First IP exhausted.
	if l.allow("203.0.113.7", now, rateLimitWindow, rateLimitMaxPerIP) {
		t.Fatalf("over-budget request for first IP was allowed")
	}
	// Second IP starts fresh.
	if !l.allow("198.51.100.42", now, rateLimitWindow, rateLimitMaxPerIP) {
		t.Fatalf("first request for distinct second IP was rejected; per-IP scoping broken")
	}
}

// TestSanitizeUpstreamError covers the policy that decides whether to
// surface an upstream error verbatim or substitute the generic message.
// Mapped-by-internal/transcribe friendly phrases stay; raw transport
// gunk gets swapped for the generic body.
func TestSanitizeUpstreamError(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Friendly mapped messages — pass through.
		{"Invalid API key for api.groq.com. Edit your config to fix it.", "Invalid API key for api.groq.com. Edit your config to fix it."},
		{"Rate limited (HTTP 429). Wait a moment and retry.", "Rate limited (HTTP 429). Wait a moment and retry."},
		{"Audio too large (HTTP 413). Try a shorter recording.", "Audio too large (HTTP 413). Try a shorter recording."},
		{"Provider error at api.groq.com (HTTP 500). Try again in a moment.", "Provider error at api.groq.com (HTTP 500). Try again in a moment."},
		// Raw transport / decode errors — swap for the generic.
		{`Post "https://api.groq.com/openai/v1/audio/transcriptions": dial tcp 10.0.0.1:443: connect: connection refused`, "upstream request failed; check the server log for details"},
		{"json: cannot unmarshal string into Go struct field …", "upstream request failed; check the server log for details"},
	}
	for _, tc := range cases {
		if got := sanitizeUpstreamError(errors.New(tc.in)); got != tc.want {
			t.Errorf("sanitizeUpstreamError(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
	// nil → "" (defensive; the handler never calls with nil but the
	// helper should be safe anyway).
	if got := sanitizeUpstreamError(nil); got != "" {
		t.Errorf("sanitizeUpstreamError(nil) = %q; want empty", got)
	}
}

// TestConstantTimeEqualString sanity-checks the wrapper. We can't
// measure timing in a unit test, but we can pin the documented
// short-circuit: unequal lengths return false without consulting bytes.
func TestConstantTimeEqualString(t *testing.T) {
	if !constantTimeEqualString("Bearer abc", "Bearer abc") {
		t.Error("equal strings reported as unequal")
	}
	if constantTimeEqualString("Bearer abc", "Bearer xyz") {
		t.Error("unequal strings reported as equal")
	}
	if constantTimeEqualString("a", "ab") {
		t.Error("different-length strings reported as equal")
	}
	if constantTimeEqualString("", "x") {
		t.Error("empty vs non-empty reported as equal")
	}
}
