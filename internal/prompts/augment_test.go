package prompts

import (
	"strings"
	"testing"
)

// TestApplyOutputLanguage_Empty: empty/whitespace language must leave the
// prompt unchanged — no trailing newlines, no empty translation directive.
func TestApplyOutputLanguage_Empty(t *testing.T) {
	base := "system prompt body"
	cases := []string{"", "   ", "\t", "\n\n"}
	for _, lang := range cases {
		got := ApplyOutputLanguage(base, lang)
		if got != base {
			t.Errorf("language=%q: expected unchanged prompt, got %q", lang, got)
		}
	}
}

// TestApplyOutputLanguage_Appended pins the exact the upstream reference translation
// instruction byte-for-byte. If this test fails, the augmentation has
// drifted from the Swift reference at PostProcessingService.swift:555-557.
func TestApplyOutputLanguage_Appended(t *testing.T) {
	base := "BASE"
	got := ApplyOutputLanguage(base, "French")
	want := "BASE\n\nIMPORTANT: Translate the final cleaned text into French. " +
		"Output ONLY in French, regardless of the original spoken language."
	if got != want {
		t.Fatalf("appended block mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestApplyOutputLanguage_TrimWhitespace: padded language values trim
// before substitution so the directive doesn't include stray spaces.
func TestApplyOutputLanguage_TrimWhitespace(t *testing.T) {
	got := ApplyOutputLanguage("BASE", "  Spanish  ")
	if !strings.Contains(got, "into Spanish.") {
		t.Errorf("expected trimmed 'Spanish' in output, got %q", got)
	}
	if strings.Contains(got, "  Spanish") || strings.Contains(got, "Spanish  ") {
		t.Errorf("expected padded whitespace stripped, got %q", got)
	}
}

// TestApplyVocabulary_Empty: empty / whitespace-only input is a no-op.
func TestApplyVocabulary_Empty(t *testing.T) {
	base := "system prompt body"
	cases := []string{"", "   ", "\n\n", ",,;\n ;", "\t\t"}
	for _, raw := range cases {
		got := ApplyVocabulary(base, raw)
		if got != base {
			t.Errorf("raw=%q: expected unchanged prompt, got %q", raw, got)
		}
	}
}

// TestApplyVocabulary_NewlineSeparated covers the common case where the
// user pastes one term per line into the multiline TOML field.
func TestApplyVocabulary_NewlineSeparated(t *testing.T) {
	got := ApplyVocabulary("BASE", "alpha\nbeta\ngamma")
	want := "BASE\n\nThe following vocabulary must be treated as high-priority terms while rewriting.\n" +
		"Use these spellings exactly in the output when relevant:\n" +
		"alpha, beta, gamma"
	if got != want {
		t.Fatalf("vocabulary block mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// TestApplyVocabulary_MixedSeparators verifies all three separators work
// together — a value with commas, semicolons, and newlines must produce
// the union of terms in encounter order.
func TestApplyVocabulary_MixedSeparators(t *testing.T) {
	got := ApplyVocabulary("BASE", "foo, bar; baz\nqux")
	if !strings.HasSuffix(got, "foo, bar, baz, qux") {
		t.Errorf("expected joined 'foo, bar, baz, qux' suffix, got %q", got)
	}
}

// TestApplyVocabulary_DedupesCaseInsensitive: duplicate terms that differ
// only in case collapse to the first occurrence's spelling.
func TestApplyVocabulary_DedupesCaseInsensitive(t *testing.T) {
	got := ApplyVocabulary("BASE", "Foo, foo, FOO")
	if !strings.HasSuffix(got, "Foo") {
		t.Errorf("expected single 'Foo' (first occurrence) at the end, got %q", got)
	}
	// "foo" or "FOO" should NOT appear independently after the colon.
	colonIdx := strings.LastIndex(got, ":\n")
	if colonIdx < 0 {
		t.Fatalf("missing block delimiter in %q", got)
	}
	joined := got[colonIdx+2:]
	if joined != "Foo" {
		t.Errorf("expected joined block to be exactly 'Foo', got %q", joined)
	}
}

// TestApplyVocabulary_TrimsTerms: per-term whitespace is stripped before
// joining so terms don't get padded with leading/trailing spaces.
func TestApplyVocabulary_TrimsTerms(t *testing.T) {
	got := ApplyVocabulary("BASE", "  foo  , bar")
	if !strings.HasSuffix(got, "foo, bar") {
		t.Errorf("expected trimmed 'foo, bar' suffix, got %q", got)
	}
}
