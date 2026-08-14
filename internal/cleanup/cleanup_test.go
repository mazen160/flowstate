package cleanup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

const (
	primaryModelID  = "openai/gpt-oss-20b"
	fallbackModelID = "openai/gpt-oss-120b"
)

// capturedRequest records what the handler saw on a single call. Tests use
// it to assert headers, model selection, and conditional-field presence.
type capturedRequest struct {
	authHeader string
	body       map[string]any
}

// newTestServer builds an httptest server whose handler delegates to
// respond. Every received request is parsed and appended to a synchronized
// slice returned by the closure for inspection. respond is given the call
// index (zero-based) and the parsed body, and returns the status code and
// response body bytes to send back.
func newTestServer(
	t *testing.T,
	respond func(call int, body map[string]any) (status int, payload []byte),
) (*httptest.Server, func() []capturedRequest) {
	t.Helper()

	var (
		mu    sync.Mutex
		calls []capturedRequest
		count int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s; want /chat/completions", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", ct)
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Errorf("unmarshal body: %v (raw=%s)", err, string(raw))
		}

		idx := int(atomic.AddInt64(&count, 1)) - 1

		mu.Lock()
		calls = append(calls, capturedRequest{
			authHeader: r.Header.Get("Authorization"),
			body:       parsed,
		})
		mu.Unlock()

		status, payload := respond(idx, parsed)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))

	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		out := make([]capturedRequest, len(calls))
		copy(out, calls)
		return out
	}
}

// chatPayload constructs a minimal valid /chat/completions JSON body whose
// choices[0].message.content equals content.
func chatPayload(t *testing.T, content string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return body
}

func newClient(srv *httptest.Server, model, fallback string) *Client {
	return NewClient(Options{
		APIKey:        "test-key",
		BaseURL:       srv.URL,
		Model:         model,
		FallbackModel: fallback,
		HTTPClient:    srv.Client(),
	})
}

func TestClean_PrimarySuccess(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 200, chatPayload(t, "Cleaned text")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	got, err := c.Clean(context.Background(), "sys", "raw", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "Cleaned text" {
		t.Errorf("Clean() = %q; want %q", got, "Cleaned text")
	}
	if n := len(getCalls()); n != 1 {
		t.Errorf("calls = %d; want 1", n)
	}
}

func TestClean_SanitizesQuotedResponse(t *testing.T) {
	srv, _ := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 200, chatPayload(t, "\"quoted answer\"")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	got, err := c.Clean(context.Background(), "sys", "raw", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "quoted answer" {
		t.Errorf("Clean() = %q; want %q", got, "quoted answer")
	}
}

func TestClean_EmptySentinel(t *testing.T) {
	srv, _ := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 200, chatPayload(t, "EMPTY")
	})
	defer srv.Close()

	// Primary returns EMPTY, which sanitize maps to "". But the raw content
	// before sanitization is "EMPTY" (non-empty after trim), so no fallback
	// fires — we get "" through the sanitize path.
	c := newClient(srv, primaryModelID, "") // no fallback to keep it deterministic
	got, err := c.Clean(context.Background(), "sys", "raw", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "" {
		t.Errorf("Clean() = %q; want empty", got)
	}
}

func TestClean_FallbackOn429(t *testing.T) {
	srv, getCalls := newTestServer(t, func(call int, _ map[string]any) (int, []byte) {
		if call == 0 {
			return 429, []byte(`{"error":"rate limited"}`)
		}
		return 200, chatPayload(t, "fallback text")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	got, err := c.Clean(context.Background(), "sys", "raw", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "fallback text" {
		t.Errorf("Clean() = %q; want %q", got, "fallback text")
	}

	calls := getCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d; want 2", len(calls))
	}
	if calls[0].body["model"] != primaryModelID {
		t.Errorf("first call model = %v; want %s", calls[0].body["model"], primaryModelID)
	}
	if calls[1].body["model"] != fallbackModelID {
		t.Errorf("second call model = %v; want %s", calls[1].body["model"], fallbackModelID)
	}
}

func TestClean_FallbackOnEmptyOutput(t *testing.T) {
	srv, getCalls := newTestServer(t, func(call int, _ map[string]any) (int, []byte) {
		if call == 0 {
			return 200, chatPayload(t, "   \n   ") // whitespace only -> empty after trim
		}
		return 200, chatPayload(t, "hi")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	got, err := c.Clean(context.Background(), "sys", "raw", "")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if got != "hi" {
		t.Errorf("Clean() = %q; want %q", got, "hi")
	}
	if n := len(getCalls()); n != 2 {
		t.Errorf("calls = %d; want 2", n)
	}
}

func TestClean_NoFallbackWhenFallbackEmpty(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 429, []byte(`{"error":"rate limited"}`)
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, "")
	_, err := c.Clean(context.Background(), "sys", "raw", "")
	if err == nil {
		t.Fatalf("Clean: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Rate limited") {
		t.Errorf("error = %q; want 'Rate limited' substring", err.Error())
	}
	if n := len(getCalls()); n != 1 {
		t.Errorf("calls = %d; want 1 (no fallback)", n)
	}
}

func TestClean_NoFallbackWhenSame(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 429, []byte(`{"error":"rate limited"}`)
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, primaryModelID)
	_, err := c.Clean(context.Background(), "sys", "raw", "")
	if err == nil {
		t.Fatalf("Clean: want error, got nil")
	}
	if n := len(getCalls()); n != 1 {
		t.Errorf("calls = %d; want 1 (no fallback when models match)", n)
	}
}

func TestClean_5xxNoFallback(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 500, []byte(`{"error":"boom"}`)
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	_, err := c.Clean(context.Background(), "sys", "raw", "")
	if err == nil {
		t.Fatalf("Clean: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Provider error") {
		t.Errorf("error = %q; want 'Provider error' substring", err.Error())
	}
	if n := len(getCalls()); n != 1 {
		t.Errorf("calls = %d; want 1 (no fallback on 5xx)", n)
	}
}

func TestClean_GPTOSSModelsIncludeConditionalFields(t *testing.T) {
	for _, model := range []string{primaryModelID, fallbackModelID} {
		t.Run(model, func(t *testing.T) {
			srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
				return 200, chatPayload(t, "ok")
			})
			defer srv.Close()

			c := newClient(srv, model, "")
			if _, err := c.Clean(context.Background(), "sys", "raw", ""); err != nil {
				t.Fatalf("Clean: %v", err)
			}

			body := getCalls()[0].body
			mct, ok := body["max_completion_tokens"]
			if !ok {
				t.Errorf("max_completion_tokens missing; body keys=%v", keys(body))
			} else if asFloat(mct) != 4096 {
				t.Errorf("max_completion_tokens = %v; want 4096", mct)
			}
			if re, ok := body["reasoning_effort"]; !ok || re != "low" {
				t.Errorf("reasoning_effort = %v (ok=%v); want \"low\"", re, ok)
			}
			if ir, ok := body["include_reasoning"]; !ok || ir != false {
				t.Errorf("include_reasoning = %v (ok=%v); want false", ir, ok)
			}
		})
	}
}

func TestClean_OtherModelOmitsConditionalFields(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 200, chatPayload(t, "ok")
	})
	defer srv.Close()

	c := newClient(srv, "qwen/qwen3.6-27b", "")
	if _, err := c.Clean(context.Background(), "sys", "raw", ""); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	body := getCalls()[0].body
	for _, key := range []string{"max_completion_tokens", "reasoning_effort", "include_reasoning"} {
		if _, ok := body[key]; ok {
			t.Errorf("body has %q; should be absent for non-GPT-OSS model. body=%v", key, body)
		}
	}
}

func TestClean_FallbackRebuildsModelFields(t *testing.T) {
	srv, getCalls := newTestServer(t, func(call int, _ map[string]any) (int, []byte) {
		if call == 0 {
			return 429, []byte(`{"error":"rate limited"}`)
		}
		return 200, chatPayload(t, "ok")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	if _, err := c.Clean(context.Background(), "sys", "raw", ""); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	calls := getCalls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d; want 2", len(calls))
	}

	// Both GPT OSS requests must carry the conditional fields.
	for callIndex, call := range calls {
		for _, key := range []string{"max_completion_tokens", "reasoning_effort", "include_reasoning"} {
			if _, ok := call.body[key]; !ok {
				t.Errorf("call %d missing %q; body=%v", callIndex, key, call.body)
			}
		}
	}
}

func TestClean_UserMessageContainsTranscript(t *testing.T) {
	srv, getCalls := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 200, chatPayload(t, "ok")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, "")
	transcript := "hello there general kenobi"
	if _, err := c.Clean(context.Background(), "sys", transcript, ""); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	body := getCalls()[0].body
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v; want 2-element array", body["messages"])
	}
	userMsg, ok := msgs[1].(map[string]any)
	if !ok {
		t.Fatalf("messages[1] = %v; want map", msgs[1])
	}
	if role := userMsg["role"]; role != "user" {
		t.Errorf("messages[1].role = %v; want \"user\"", role)
	}
	content, ok := userMsg["content"].(string)
	if !ok {
		t.Fatalf("messages[1].content = %v; want string", userMsg["content"])
	}
	wantSub := fmt.Sprintf("RAW_TRANSCRIPTION: \"%s\"", transcript)
	if !strings.Contains(content, wantSub) {
		t.Errorf("user content missing %q; got %q", wantSub, content)
	}
	// Also confirm the system prompt was placed at index 0.
	sysMsg, ok := msgs[0].(map[string]any)
	if !ok || sysMsg["role"] != "system" || sysMsg["content"] != "sys" {
		t.Errorf("messages[0] = %v; want system/sys", msgs[0])
	}
}

func TestClean_AuthorizationHeader(t *testing.T) {
	srv, getCalls := newTestServer(t, func(call int, _ map[string]any) (int, []byte) {
		if call == 0 {
			return 429, []byte(`{}`)
		}
		return 200, chatPayload(t, "ok")
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	if _, err := c.Clean(context.Background(), "sys", "raw", ""); err != nil {
		t.Fatalf("Clean: %v", err)
	}

	for i, call := range getCalls() {
		if call.authHeader != "Bearer test-key" {
			t.Errorf("call %d Authorization = %q; want \"Bearer test-key\"", i, call.authHeader)
		}
	}
}

func TestClean_UnauthorizedFriendly(t *testing.T) {
	srv, _ := newTestServer(t, func(_ int, _ map[string]any) (int, []byte) {
		return 401, []byte(`{"error":"unauthorized"}`)
	})
	defer srv.Close()

	c := newClient(srv, primaryModelID, fallbackModelID)
	_, err := c.Clean(context.Background(), "sys", "raw", "")
	if err == nil {
		t.Fatalf("Clean: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error = %q; want 'Invalid API key' substring", err.Error())
	}
}

// keys returns the keys of m in arbitrary order — used only for diagnostic
// output in failing assertions.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// asFloat coerces a JSON-decoded numeric value (which is always float64) to
// a float64. Returns NaN-like 0 if v isn't a number; tests that care will
// fail the assertion anyway when comparing against the expected value.
func asFloat(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}
