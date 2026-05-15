package prompts

import "strings"

// ApplyOutputLanguage returns systemPrompt with FreeFlow's translation
// instruction appended when language is non-empty after trimming.
//
// The appended sentence is taken verbatim from FreeFlow's
// PostProcessingService.applyOutputLanguage:
//
//	"IMPORTANT: Translate the final cleaned text into <X>. Output ONLY in
//	 <X>, regardless of the original spoken language."
//
// Leading/trailing whitespace on language is stripped before the empty
// check and before substitution. If the trimmed language is empty,
// systemPrompt is returned unchanged.
//
// The two newlines between the original prompt and the instruction match
// FreeFlow's join behavior so byte-level diffs against the Swift output
// stay clean.
func ApplyOutputLanguage(systemPrompt, language string) string {
	lang := strings.TrimSpace(language)
	if lang == "" {
		return systemPrompt
	}
	return systemPrompt +
		"\n\nIMPORTANT: Translate the final cleaned text into " + lang +
		". Output ONLY in " + lang +
		", regardless of the original spoken language."
}

// ApplyVocabulary returns systemPrompt with FreeFlow's high-priority
// vocabulary block appended when rawVocabulary parses to at least one
// non-empty term.
//
// Parsing rules (match FreeFlow's mergedVocabularyTerms):
//   - Split on '\n', ',', and ';'.
//   - Trim each token.
//   - Drop empty tokens.
//   - Dedupe case-insensitively, preserving first-occurrence order
//     and the original case of the first occurrence.
//
// When at least one term survives, the appended block is:
//
//	"\n\nThe following vocabulary must be treated as high-priority terms
//	 while rewriting.\nUse these spellings exactly in the output when
//	 relevant:\n<comma-joined terms>"
//
// (joined with ", " — matches FreeFlow's normalizedVocabularyText).
//
// If no terms survive, systemPrompt is returned unchanged.
func ApplyVocabulary(systemPrompt, rawVocabulary string) string {
	terms := splitVocabulary(rawVocabulary)
	if len(terms) == 0 {
		return systemPrompt
	}
	joined := strings.Join(terms, ", ")
	return systemPrompt +
		"\n\nThe following vocabulary must be treated as high-priority terms while rewriting.\n" +
		"Use these spellings exactly in the output when relevant:\n" +
		joined
}

// splitVocabulary tokenizes rawVocabulary on \n , ; , trims, drops
// empties, and dedupes case-insensitively while preserving the case and
// order of each term's first occurrence.
func splitVocabulary(rawVocabulary string) []string {
	if strings.TrimSpace(rawVocabulary) == "" {
		return nil
	}
	fields := strings.FieldsFunc(rawVocabulary, func(r rune) bool {
		return r == '\n' || r == ',' || r == ';'
	})

	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		term := strings.TrimSpace(f)
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, term)
	}
	return out
}
