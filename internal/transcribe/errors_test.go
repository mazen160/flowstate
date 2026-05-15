package transcribe

import (
	"strings"
	"testing"
)

func TestMapHTTPError(t *testing.T) {
	host := "api.groq.com"

	cases := []struct {
		name      string
		status    int
		wantParts []string // substrings that must all appear in the result
	}{
		{
			name:      "401_unauthorized",
			status:    401,
			wantParts: []string{"Invalid API key", host, "Edit your config"},
		},
		{
			name:      "403_forbidden",
			status:    403,
			wantParts: []string{"Key lacks permission", host, "HTTP 403", "scopes"},
		},
		{
			name:      "404_not_found",
			status:    404,
			wantParts: []string{"Endpoint not found", host, "HTTP 404", "Base URL"},
		},
		{
			name:      "413_payload_too_large",
			status:    413,
			wantParts: []string{"Audio too large", "HTTP 413", "shorter recording"},
		},
		{
			name:      "429_rate_limited",
			status:    429,
			wantParts: []string{"Rate limited", "HTTP 429", "Wait a moment"},
		},
		{
			name:      "500_internal",
			status:    500,
			wantParts: []string{"Provider error", host, "HTTP 500"},
		},
		{
			name:      "502_bad_gateway",
			status:    502,
			wantParts: []string{"Provider error", host, "HTTP 502"},
		},
		{
			name:      "599_edge_of_5xx",
			status:    599,
			wantParts: []string{"Provider error", host, "HTTP 599"},
		},
		{
			name:      "418_teapot_default",
			status:    418,
			wantParts: []string{"Request failed", host, "HTTP 418"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapHTTPError(tc.status, host)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("mapHTTPError(%d, %q) = %q; missing substring %q",
						tc.status, host, got, want)
				}
			}
		})
	}
}

// TestMapHTTPError_EmptyHost confirms the function still produces a sane
// message when the BaseURL didn't parse cleanly and host is "".
func TestMapHTTPError_EmptyHost(t *testing.T) {
	got := mapHTTPError(401, "")
	if !strings.Contains(got, "the provider") {
		t.Errorf("mapHTTPError(401, \"\") = %q; want fallback containing 'the provider'", got)
	}
}

// TestMapHTTPError_HostInMessage explicitly confirms the host substring
// appears for the codes where it's interpolated (the table requirement).
func TestMapHTTPError_HostInMessage(t *testing.T) {
	host := "custom.example.com"
	for _, status := range []int{401, 403, 404, 500, 503, 418} {
		msg := mapHTTPError(status, host)
		if !strings.Contains(msg, host) {
			t.Errorf("mapHTTPError(%d, %q) = %q; host missing", status, host, msg)
		}
	}
}
