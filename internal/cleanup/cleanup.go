// Package cleanup is Flowstate's client for the Groq chat-completions endpoint
// used to post-process raw transcripts. It wraps a single HTTP call: POST a
// JSON body to /chat/completions, parse choices[0].message.content, sanitize
// the result, and return the cleaned text.
//
// The wire format, the conditional gpt-oss-20b fields, the sanitization
// rules, and the fallback policy are pinned by the "Groq Provider Contract"
// doc and ported from FreeFlow's PostProcessingService so the bytes leaving
// Flowstate are indistinguishable from the Swift app.
//
// This package is stdlib-only by design. The whole client is a Client struct
// with a single public method, Clean, and a small set of unexported helpers
// split across files for readability:
//
//   - cleanup.go    Client, NewClient, Clean orchestration, fallback policy
//   - request.go    JSON body assembly, model-conditional fields
//   - sanitize.go   trim + outer-quote strip + EMPTY sentinel
//   - errors.go     status-code -> user-readable message mapping
package cleanup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTimeout is the per-request timeout when the caller doesn't supply
// an http.Client. It matches FreeFlow's postProcessingTimeoutSeconds and the
// "Timeout: 20 s per request" line in the provider contract doc.
const defaultTimeout = 20 * time.Second

// Options configures a Client. APIKey, BaseURL, and Model are required for
// a request to succeed; FallbackModel is optional and disables fallback when
// empty or equal to Model; HTTPClient is optional and falls back to a
// 20-second-timeout default when nil.
type Options struct {
	// APIKey is the Bearer token sent in the Authorization header.
	APIKey string
	// BaseURL is the provider root, e.g. "https://api.groq.com/openai/v1".
	// The endpoint path "/chat/completions" is appended to it.
	BaseURL string
	// Model is the primary model id, e.g. "openai/gpt-oss-20b". Forwarded
	// as the JSON "model" field verbatim.
	Model string
	// FallbackModel is the model id retried on 429 or empty-output from
	// the primary. When "" or equal to Model, no fallback is attempted.
	FallbackModel string
	// HTTPClient lets callers inject a pre-configured client (for tests,
	// retries, custom transports). When nil a default client with a
	// 20-second Timeout is used.
	HTTPClient *http.Client
}

// Client performs cleanup (chat-completion) requests against a single
// provider. It is safe for concurrent use as long as the underlying
// *http.Client is.
type Client struct {
	apiKey        string
	baseURL       string
	host          string // pre-extracted for error messages
	model         string
	fallbackModel string
	httpClient    *http.Client
}

// NewClient constructs a Client from the given Options. It does not validate
// the inputs (an empty BaseURL or APIKey is allowed at construction time)
// because the caller — config.Load + the runtime — owns validation and
// already produces friendly errors when fields are missing. The first Clean
// call will surface any wire-level problems.
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
		apiKey:        opts.APIKey,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		host:          host,
		model:         opts.Model,
		fallbackModel: opts.FallbackModel,
		httpClient:    httpClient,
	}
}

// chatResponse mirrors the JSON shape we need from /chat/completions. The
// provider returns more fields (id, created, usage, etc.) but we only
// consume choices[0].message.content; everything else is ignored.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatMessage struct {
	Content string `json:"content"`
}

// Clean post-processes the given transcript using systemPrompt (the active
// prompt body from the config's [prompts] table) and contextSummary (the
// AppContext string, always "" in v1). It returns the cleaned text or an
// error.
//
// Fallback policy: on HTTP 429 or empty-after-trim 200 response, the request
// is retried once with FallbackModel (if a usable fallback is configured —
// see [Options.FallbackModel]). Any other failure is surfaced directly.
//
// Cancellation: ctx controls each request's lifetime. If ctx has no
// deadline, the client's default 20-second timeout still applies via
// http.Client. If ctx has a shorter deadline, that wins. Any error returned
// wraps a sentinel where useful (context.DeadlineExceeded, context.Canceled)
// so callers can switch on errors.Is.
func (c *Client) Clean(ctx context.Context, systemPrompt, transcript, contextSummary string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	text, status, err := c.doRequest(ctx, c.model, systemPrompt, transcript, contextSummary)

	// Decide whether to attempt the fallback. The rules (per the doc):
	//   - HTTP 429 from primary -> try fallback.
	//   - HTTP 200 from primary but empty (after trim) content -> try fallback.
	//   - Anything else -> return as-is.
	// And: no fallback if FallbackModel is unset or equal to the primary.
	if c.shouldFallback(err, status, text) {
		return c.doFallback(ctx, systemPrompt, transcript, contextSummary)
	}

	if err != nil {
		return "", err
	}
	return sanitizeOutput(text), nil
}

// shouldFallback returns true if the primary call's outcome qualifies for
// a fallback retry. It encapsulates the doc's two trigger conditions so
// Clean stays readable.
func (c *Client) shouldFallback(err error, status int, text string) bool {
	if c.fallbackModel == "" || c.fallbackModel == c.model {
		return false
	}
	if err != nil && status == http.StatusTooManyRequests {
		return true
	}
	if err == nil && status == http.StatusOK && strings.TrimSpace(text) == "" {
		return true
	}
	return false
}

// doFallback issues the fallback request and returns its sanitized result
// (or its error verbatim — there is no third attempt).
func (c *Client) doFallback(ctx context.Context, systemPrompt, transcript, contextSummary string) (string, error) {
	text, _, err := c.doRequest(ctx, c.fallbackModel, systemPrompt, transcript, contextSummary)
	if err != nil {
		return "", err
	}
	return sanitizeOutput(text), nil
}

// doRequest performs a single chat-completions call against the given model
// and returns the raw content string, the HTTP status code, and a non-nil
// error iff the call failed. Non-2xx responses produce a friendly error via
// [mapHTTPError]; the status code is returned alongside so the caller can
// branch on 429 for the fallback policy.
func (c *Client) doRequest(
	ctx context.Context,
	model, systemPrompt, transcript, contextSummary string,
) (string, int, error) {
	body, err := buildRequestBody(model, systemPrompt, transcript, contextSummary)
	if err != nil {
		return "", 0, err
	}

	endpoint := c.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("build cleanup request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = int64(len(body))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Propagate context errors directly so callers can detect
		// cancellation/deadline via errors.Is.
		if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("cleanup request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read cleanup response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, errors.New(mapHTTPError(resp.StatusCode, c.host))
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return "", resp.StatusCode, fmt.Errorf("parse cleanup response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", resp.StatusCode, fmt.Errorf("cleanup response had no choices")
	}
	return parsed.Choices[0].Message.Content, resp.StatusCode, nil
}
