// Package cleanup — HTTP error mapping.
//
// mapHTTPError translates a non-2xx response from the provider into a
// one-line, user-readable error message. The mapping is the canonical list
// in the "Groq Provider Contract" doc and is ported (with flowstate
// phrasing) from the upstream reference.
//
// The table is intentionally duplicated from internal/transcribe rather
// than imported: cleanup does not conceptually depend on transcribe, the
// doc lists the messages as a per-endpoint table, and a future divergence
// (e.g. different 413 phrasing for chat vs. audio) is easier to express
// when the two tables live next to their respective wire formats.
package cleanup

import "fmt"

// mapHTTPError returns the user-readable string for the given HTTP status
// and the host extracted from the provider's base URL. The host is
// interpolated into the message so the user can tell at a glance which
// provider rejected the request. If host is empty, the literal "the
// provider" is substituted.
//
// The 5xx range (500..599) is treated as a single bucket. Any status not
// listed in the table falls through to the generic "Request failed"
// message — the spec calls this out explicitly as the default case.
//
// Note: 413 ("Payload too large") is mapped to the "Audio too large"
// phrasing from transcribe; the doc lists this message under both
// endpoints. If the cleanup endpoint ever returns 413 in practice the
// message will read slightly oddly (chat completions don't carry audio),
// but the status code is so rare on /chat/completions that diverging the
// table here is not worth the maintenance cost — the existing string is
// what the contract doc pins.
func mapHTTPError(status int, host string) string {
	if host == "" {
		host = "the provider"
	}

	switch status {
	case 401:
		return fmt.Sprintf("Invalid API key for %s. Edit your config to fix it.", host)
	case 403:
		return fmt.Sprintf("Key lacks permission at %s (HTTP 403). Check key scopes.", host)
	case 404:
		return fmt.Sprintf("Endpoint not found at %s (HTTP 404). Base URL is wrong.", host)
	case 413:
		return "Audio too large (HTTP 413). Try a shorter recording."
	case 429:
		return "Rate limited (HTTP 429). Wait a moment and retry."
	}

	if status >= 500 && status <= 599 {
		return fmt.Sprintf("Provider error at %s (HTTP %d). Try again in a moment.", host, status)
	}

	return fmt.Sprintf("Request failed at %s (HTTP %d).", host, status)
}
