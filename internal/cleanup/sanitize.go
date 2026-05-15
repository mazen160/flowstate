// Package cleanup — output sanitization.
//
// sanitizeOutput applies the three rules from the "Groq Provider Contract"
// doc to the raw choices[0].message.content string. The rules are ported
// verbatim from the upstream cleanup service's sanitizer.
package cleanup

import "strings"

// sanitizeOutput trims the model's reply, strips matching outer double
// quotes if the entire reply was wrapped in them, and maps the literal
// sentinel "EMPTY" to the empty string.
//
// Order matters and matches the Swift implementation:
//
//  1. Trim leading/trailing whitespace.
//  2. If the trimmed value starts AND ends with a literal `"` and is longer
//     than one character, strip both quotes and trim again.
//  3. If the result equals the literal "EMPTY", return "".
//
// A `"EMPTY"` reply therefore becomes "" — the outer quotes are stripped
// (rule 2) and the sentinel matches (rule 3). A reply with only a leading
// or only a trailing quote is left untouched.
func sanitizeOutput(raw string) string {
	result := strings.TrimSpace(raw)
	if result == "" {
		return ""
	}

	if len(result) > 1 && strings.HasPrefix(result, "\"") && strings.HasSuffix(result, "\"") {
		result = result[1 : len(result)-1]
		result = strings.TrimSpace(result)
	}

	if result == "EMPTY" {
		return ""
	}

	return result
}
