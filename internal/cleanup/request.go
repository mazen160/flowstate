// Package cleanup — request body assembly.
//
// buildRequestBody assembles the JSON body for POST /chat/completions. The
// shape and the conditional gpt-oss-20b fields are pinned by the
// "Groq Provider Contract" doc and ported from the upstream cleanup service.
package cleanup

import (
	"encoding/json"
	"fmt"
)

// gptOSS20BModel is the model id that triggers the three conditional fields
// (max_completion_tokens, reasoning_effort, include_reasoning). Any other
// model — including the llama-4-scout fallback — must NOT carry those
// fields in the request body.
const gptOSS20BModel = "openai/gpt-oss-20b"

// Conditional-field constants for the gpt-oss-20b model. Hard-coded here
// rather than parameterized because every reference implementation
// (the upstream reference, the contract doc) pins them; making them configurable would
// only invite drift.
const (
	postProcessingMaxCompletionTokens = 4096
	postProcessingReasoningEffort     = "low"
)

// chatMessageOut is the wire representation of a single message in the
// "messages" array. Field tags are explicit so the body is byte-identical to
// the documented payload regardless of map iteration order.
type chatMessageOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatRequest is the base request shape sent for every model. The
// conditional gpt-oss-20b fields are appended in [buildRequestBody] by
// re-marshaling, because struct-with-omitempty would force pointer types
// (or sentinel "magic" values) and obscure the doc's "always sent for X,
// never for Y" contract.
type chatRequest struct {
	Model       string           `json:"model"`
	Temperature float64          `json:"temperature"`
	Messages    []chatMessageOut `json:"messages"`
}

// buildRequestBody returns the JSON bytes for a single chat-completions
// request. The user-message template is verbatim from the upstream reference:
//
//	Instructions: Clean up RAW_TRANSCRIPTION and return only the cleaned
//	transcript text without surrounding quotes. Return EMPTY if there
//	should be no result.
//
//	CONTEXT: "<contextSummary>"
//
//	RAW_TRANSCRIPTION: "<transcript>"
//
// The three gpt-oss-20b-only fields are added by merging into a map after
// the base struct is marshaled, so they ONLY appear in the JSON when
// model == "openai/gpt-oss-20b" (no zero values, no omitempty guesswork).
func buildRequestBody(model, systemPrompt, transcript, contextSummary string) ([]byte, error) {
	userMessage := fmt.Sprintf(
		"Instructions: Clean up RAW_TRANSCRIPTION and return only the cleaned transcript text without surrounding quotes. Return EMPTY if there should be no result.\n\nCONTEXT: \"%s\"\n\nRAW_TRANSCRIPTION: \"%s\"",
		contextSummary,
		transcript,
	)

	base := chatRequest{
		Model:       model,
		Temperature: 0.0,
		Messages: []chatMessageOut{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	}

	if model != gptOSS20BModel {
		return json.Marshal(base)
	}

	// Re-marshal through a map so the three extra fields appear in the
	// body exclusively for gpt-oss-20b. Going through a map costs one
	// extra unmarshal/marshal but keeps the conditional contract explicit
	// — see the doc note above.
	raw, err := json.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("marshal cleanup request base: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("re-decode cleanup request base: %w", err)
	}
	generic["max_completion_tokens"] = postProcessingMaxCompletionTokens
	generic["reasoning_effort"] = postProcessingReasoningEffort
	generic["include_reasoning"] = false
	return json.Marshal(generic)
}
