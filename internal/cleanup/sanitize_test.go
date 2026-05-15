package cleanup

import "testing"

func TestSanitizeOutput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain_pass_through", "Hello world.", "Hello world."},
		{"trim_leading_trailing_whitespace", "   hello   ", "hello"},
		{"trim_newlines", "\n\nhello\n\n", "hello"},
		{"strip_outer_double_quotes", "\"quoted answer\"", "quoted answer"},
		{"strip_outer_quotes_then_trim", "  \"quoted answer\"  ", "quoted answer"},
		{"single_leading_quote_only_kept", "\"unbalanced", "\"unbalanced"},
		{"single_trailing_quote_only_kept", "unbalanced\"", "unbalanced\""},
		{"empty_sentinel", "EMPTY", ""},
		{"quoted_empty_sentinel", "\"EMPTY\"", ""},
		{"whitespace_padded_empty_sentinel", "  EMPTY  ", ""},
		{"empty_string_in_empty_string_out", "", ""},
		{"whitespace_only_in_empty_string_out", "   \n  ", ""},
		// A bare lonely double-quote is length 1; the strip rule requires
		// length > 1, so it should be returned as-is.
		{"single_quote_alone_kept", "\"", "\""},
		// Quotes around an inner sentence stay if there's no matching pair
		// at both ends — e.g. embedded quotes should not be touched.
		{"inner_quotes_preserved", "he said \"hi\" then left", "he said \"hi\" then left"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeOutput(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeOutput(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
