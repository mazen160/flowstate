// Package prompts embeds the canonical Flowstate system prompts and exposes
// them by name. The strings are ported verbatim from FreeFlow so cleanup
// behavior matches.
package prompts

import _ "embed"

//go:embed default.txt
var defaultPrompt string

//go:embed command.txt
var commandPrompt string

//go:embed literal.txt
var literalPrompt string

// Default returns the FreeFlow-style cleanup prompt with self-correction,
// formatting, and developer-syntax handling.
func Default() string { return defaultPrompt }

// Command returns the edit-mode prompt for transforming highlighted text by
// a spoken instruction.
func Command() string { return commandPrompt }

// Literal returns the simpler context-light cleanup prompt from the
// FreeFlow README "Custom Cleanup" section.
func Literal() string { return literalPrompt }

// Get returns the prompt for a name. Names are lowercased before lookup.
// The second return is false for unknown names.
func Get(name string) (string, bool) {
	switch name {
	case "default":
		return defaultPrompt, true
	case "command":
		return commandPrompt, true
	case "literal":
		return literalPrompt, true
	}
	return "", false
}

// Names returns the registered prompt names in stable order.
func Names() []string {
	return []string{"default", "command", "literal"}
}
