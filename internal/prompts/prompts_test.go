package prompts

import (
	"strings"
	"testing"
)

func TestDefaultPromptHeader(t *testing.T) {
	got := Default()
	const want = "You are a literal dictation cleanup layer"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("default prompt should start with %q, got %q...", want, got[:min(len(got), 80)])
	}
}

func TestCommandPromptHeader(t *testing.T) {
	got := Command()
	const want = "You transform highlighted text"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("command prompt should start with %q, got %q...", want, got[:min(len(got), 80)])
	}
}

func TestLiteralPromptHeader(t *testing.T) {
	got := Literal()
	const want = "You are a dictation post-processor"
	if !strings.HasPrefix(got, want) {
		t.Fatalf("literal prompt should start with %q, got %q...", want, got[:min(len(got), 80)])
	}
}

func TestGetKnown(t *testing.T) {
	for _, name := range Names() {
		body, ok := Get(name)
		if !ok {
			t.Fatalf("Get(%q) returned ok=false", name)
		}
		if len(body) == 0 {
			t.Fatalf("Get(%q) returned empty body", name)
		}
	}
}

func TestGetUnknown(t *testing.T) {
	if _, ok := Get("nope"); ok {
		t.Fatal("Get(nope) returned ok=true")
	}
}

func TestEmptySentinelMentioned(t *testing.T) {
	for _, name := range []string{"default", "literal"} {
		body, _ := Get(name)
		if !strings.Contains(body, "EMPTY") {
			t.Errorf("%s prompt must instruct returning EMPTY sentinel on empty input", name)
		}
	}
}
