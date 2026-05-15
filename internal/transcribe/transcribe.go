// Package transcribe is Flowstate's client for the Groq Whisper transcription
// API (OpenAI-compatible /audio/transcriptions endpoint). It wraps a single
// HTTP call: POST a WAV file as multipart/form-data, parse the verbose_json
// response, optionally drop known-hallucination phrases, and return the
// transcript text.
//
// The wire format and hallucination filter are pinned by the
// "Groq Provider Contract" doc and ported from the upstream transcription service
// so the bytes leaving Flowstate are indistinguishable from the Swift app.
//
// This package is stdlib-only by design — no JSON/HTTP helpers beyond what
// Go's standard library provides. The whole client is a Client struct with a
// single public method, Transcribe, and a small set of unexported helpers
// split across files for readability:
//
//   - transcribe.go      Client, NewClient, Transcribe orchestration
//   - multipart.go       request body assembly
//   - hallucination.go   stock-phrase filter
//   - errors.go          status-code -> user-readable message mapping
package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// defaultTimeout is the per-request timeout when the caller doesn't supply
// an http.Client. It matches the documented transcription timeout and the
// "Timeout: 20 s per request" line in the provider contract doc.
const defaultTimeout = 20 * time.Second

// Options configures a Client. APIKey, BaseURL, and Model are required for
// a request to succeed; the zero value of Language means "let the provider
// auto-detect"; HTTPClient is optional and falls back to a 20-second-timeout
// default when nil.
type Options struct {
	// APIKey is the Bearer token sent in the Authorization header.
	APIKey string
	// BaseURL is the provider root, e.g. "https://api.groq.com/openai/v1".
	// The endpoint path "/audio/transcriptions" is appended to it.
	BaseURL string
	// Model is the Whisper model id, e.g. "whisper-large-v3". Forwarded
	// as the multipart "model" field verbatim.
	Model string
	// Language is an ISO-639-1 hint (e.g. "en"). Empty means omit the
	// language field and let the provider auto-detect.
	Language string
	// HTTPClient lets callers inject a pre-configured client (for tests,
	// retries, custom transports). When nil a default client with a
	// 20-second Timeout is used.
	HTTPClient *http.Client
}

// Client performs transcription requests against a single provider. It is
// safe for concurrent use as long as the underlying *http.Client is.
type Client struct {
	apiKey     string
	baseURL    string
	host       string // pre-extracted for error messages
	model      string
	language   string
	httpClient *http.Client
}

// NewClient constructs a Client from the given Options. It does not validate
// the inputs (an empty BaseURL or APIKey is allowed at construction time)
// because the caller — config.Load + the runtime — owns validation and
// already produces friendly errors when fields are missing. The first
// Transcribe call will surface any wire-level problems.
func NewClient(opts Options) *Client {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}

	host := ""
	if u, err := url.Parse(opts.BaseURL); err == nil {
		host = u.Host
	}

	return &Client{
		apiKey:     opts.APIKey,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		host:       host,
		model:      opts.Model,
		language:   opts.Language,
		httpClient: httpClient,
	}
}

// transcriptionResponse mirrors the verbose_json payload from the provider.
// We only consume two fields — text and segments[0].no_speech_prob — but
// keep the JSON structure minimal and explicit so any future drift in the
// API shape (extra fields, renamed fields) is easy to spot when debugging.
type transcriptionResponse struct {
	Text     string                 `json:"text"`
	Segments []transcriptionSegment `json:"segments"`
}

// transcriptionSegment captures only the no_speech_prob field. It uses a
// *float64 so we can tell "the provider omitted this number" (nil) from
// "the provider reported 0.0" (non-nil pointing at 0.0). The hallucination
// filter relies on this distinction.
type transcriptionSegment struct {
	NoSpeechProb *float64 `json:"no_speech_prob"`
}

// Transcribe uploads the WAV file at wavPath, parses the response, applies
// the hallucination filter, and returns the resulting transcript text. The
// returned string can be "" — either because the audio was genuinely silent
// (text was empty in the response) or because the hallucination filter
// dropped a stock phrase. Callers should treat "" as "nothing to display."
//
// Cancellation: ctx controls the request lifetime. If ctx has no deadline,
// the client's default 20-second timeout still applies via http.Client.
// If ctx has a shorter deadline, that wins. Any error returned wraps a
// sentinel where useful (context.DeadlineExceeded, context.Canceled) so
// callers can switch on errors.Is.
//
// Implementation note: Transcribe reads the file into memory once and
// delegates to TranscribeReader. The disk path is preserved so existing
// callers (the record pipeline) keep their current signature.
func (c *Client) Transcribe(ctx context.Context, wavPath string) (string, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("open audio file: %w", err)
	}
	defer f.Close()
	return c.TranscribeReader(ctx, f, filepath.Base(wavPath))
}

// TranscribeReader is the streaming-friendly form of Transcribe: it accepts
// a reader containing the audio bytes plus the filename Groq should see in
// the multipart "file" part header. The filename's extension picks the
// Content-Type ("recording.webm" → audio/webm, "audio.wav" → audio/wav,
// etc.).
//
// The reader is fully drained into memory before the request fires. We need
// a known Content-Length for the outer request (Groq's edge has historically
// been unhappy with chunked-encoded multipart uploads), and multipart bodies
// in flowstate are always small enough that a single in-memory copy is
// cheaper than the alternative.
func (c *Client) TranscribeReader(ctx context.Context, r io.Reader, filename string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil {
		return "", fmt.Errorf("transcribe: nil reader")
	}

	audioBytes, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read audio: %w", err)
	}

	body, boundary, err := buildMultipartBodyFromBytes(audioBytes, filename, c.model, c.language)
	if err != nil {
		return "", err
	}

	endpoint := c.baseURL + "/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("build transcription request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	// ContentLength makes the server's life easier and avoids chunked
	// transfer encoding for what is always a known-length upload.
	req.ContentLength = int64(len(body))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Propagate context errors directly so callers can detect
		// cancellation/deadline via errors.Is.
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return "", err
		}
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read transcription response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", errors.New(mapHTTPError(resp.StatusCode, c.host))
	}

	return parseTranscript(respBytes)
}

// parseTranscript unmarshals the response body and applies the hallucination
// filter. It mirrors the upstream reference's parseTranscript:
//
//  1. Try JSON first. If we get a "text" field, that's the answer; run the
//     hallucination filter and either return text or "".
//  2. If JSON parsing fails or text is absent, fall back to treating the
//     body as plain text — split on newlines, join with spaces, trim. An
//     empty fallback is an error (the provider gave us nothing usable).
//
// The plain-text fallback matters because some non-Groq OpenAI-compatible
// providers (and some error pages on 200) return raw text instead of JSON;
// the Swift code's identical fallback has caught real provider bugs in the
// wild.
func parseTranscript(body []byte) (string, error) {
	var resp transcriptionResponse
	if err := json.Unmarshal(body, &resp); err == nil && resp.Text != "" {
		var nsp float64
		hasNSP := false
		if len(resp.Segments) > 0 && resp.Segments[0].NoSpeechProb != nil {
			nsp = *resp.Segments[0].NoSpeechProb
			hasNSP = true
		}
		if isHallucination(resp.Text, nsp, hasNSP) {
			return "", nil
		}
		return resp.Text, nil
	}

	// Plain-text fallback.
	collapsed := strings.Join(strings.Split(string(body), "\n"), " ")
	collapsed = strings.TrimSpace(collapsed)
	// Some bodies have a trailing space from the newline-join when the
	// body ended with "\n"; collapse runs of spaces back to single ones
	// so the result is tidy.
	collapsed = collapseSpaces(collapsed)
	if collapsed == "" {
		return "", fmt.Errorf("transcription response was empty or unparseable")
	}
	return collapsed, nil
}

// collapseSpaces replaces runs of ASCII space characters with a single
// space. Used only by the plain-text fallback to tidy up output after the
// newline→space join.
func collapseSpaces(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
			b.WriteRune(r)
			continue
		}
		prevSpace = false
		b.WriteRune(r)
	}
	return b.String()
}
