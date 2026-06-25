// Package config holds Flowstate's TOML configuration: load, init, validate,
// and path resolution. The canonical schema is documented in the "Config
// Specification" doc; this package is the executable mirror of that spec.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config models the on-disk TOML.
//
// Field names match the snake_case keys in the spec. Pointer-free zero values
// let us treat an unset field as "use the documented default" in [Defaults].
//
// Note: the Groq API key is intentionally NOT a field here. It is read at
// runtime from the GROQ_API_KEY environment variable (or GROQ_API_TOKEN as
// a fallback) by [ResolveAPIKey]. Keeping secrets out of the config file
// makes them less likely to be checked in or shared accidentally.
type Config struct {
	Trigger                     string            `toml:"trigger"`
	PTTKey                      string            `toml:"ptt_key"`
	BaseURL                     string            `toml:"base_url"`
	TranscriptionModel          string            `toml:"transcription_model"`
	CleanupModel                string            `toml:"cleanup_model"`
	CleanupFallbackModel        string            `toml:"cleanup_fallback_model"`
	Language                    string            `toml:"language"`
	OutputLanguage              string            `toml:"output_language"`
	InputDevice                 string            `toml:"input_device"`
	MuteWhileRecording          bool              `toml:"mute_while_recording"`
	OutputMode                  string            `toml:"output_mode"`
	PreserveClipboardAfterPaste bool              `toml:"preserve_clipboard_after_paste"`
	ActivePrompt                string            `toml:"active_prompt"`
	CustomVocabulary            string            `toml:"custom_vocabulary"`
	Colors                      string            `toml:"colors"`
	MaxTimeSeconds              int               `toml:"max_time_seconds"`
	PasteDelaySeconds           int               `toml:"paste_delay_seconds"`
	Prompts                     map[string]string `toml:"prompts"`

	// warnings is populated during Load for soft issues (e.g. unrecognized
	// keys surfaced by the TOML decoder). Read via [Config.Warnings].
	warnings []string `toml:"-"`
}

// Defaults returns a Config populated with the documented defaults. It does
// NOT inline the embedded prompts — [Init] does that so prompts only ship in
// freshly-written files (Load preserves whatever the user has on disk).
func Defaults() Config {
	return Config{
		Trigger:                     "enter",
		PTTKey:                      "space",
		BaseURL:                     "https://api.groq.com/openai/v1",
		TranscriptionModel:          "whisper-large-v3",
		CleanupModel:                "openai/gpt-oss-20b",
		CleanupFallbackModel:        "llama-3.3-70b-versatile",
		Language:                    "en",
		OutputLanguage:              "",
		InputDevice:                 "",
		MuteWhileRecording:          true,
		OutputMode:                  "stdout,clipboard",
		PreserveClipboardAfterPaste: true,
		ActivePrompt:                "default",
		CustomVocabulary:            "",
		Colors:                      "auto",
		MaxTimeSeconds:              0,
		PasteDelaySeconds:           0,
	}
}

// validTriggers enumerates the allowed values for [Config.Trigger].
var validTriggers = map[string]struct{}{
	"enter":        {},
	"push-to-talk": {},
}

// validOutputModes enumerates the allowed atomic destinations. The full
// [Config.OutputMode] string is a comma-separated subset, or the literal
// "all".
var validOutputModes = map[string]struct{}{
	"stdout":    {},
	"clipboard": {},
	"paste":     {},
}

// validColorModes enumerates the allowed values for [Config.Colors]. The
// empty string is also accepted at validate time and treated as "auto" —
// existing configs that predate this field load without a forced rewrite.
var validColorModes = map[string]struct{}{
	"auto":   {},
	"always": {},
	"never":  {},
}

// Validate checks that the parsed Config is internally consistent. It does
// NOT require an API key — that's checked separately by [ResolveAPIKey] so
// `flowstate config init` and `config path` can run before the user has
// exported one.
func (c *Config) Validate() error {
	if _, ok := validTriggers[c.Trigger]; !ok {
		return fmt.Errorf("invalid trigger %q: must be one of %s",
			c.Trigger, joinKeys(validTriggers))
	}

	if _, _, _, err := parseOutputMode(c.OutputMode); err != nil {
		return err
	}

	if c.ActivePrompt == "" {
		return fmt.Errorf("active_prompt is empty")
	}
	if _, ok := c.Prompts[c.ActivePrompt]; !ok {
		return fmt.Errorf("active_prompt %q does not match any key under [prompts]", c.ActivePrompt)
	}

	// The empty string is tolerated — an existing config that predates
	// the colors field shouldn't suddenly fail Validate. The runtime
	// resolver in cmd/flowstate treats "" the same as "auto".
	if c.Colors != "" {
		if _, ok := validColorModes[c.Colors]; !ok {
			return fmt.Errorf("invalid colors %q: must be one of %s",
				c.Colors, joinKeys(validColorModes))
		}
	}

	if c.MaxTimeSeconds < 0 {
		return fmt.Errorf("invalid max_time_seconds %d: must be >= 0 (0 = disabled)", c.MaxTimeSeconds)
	}

	if c.PasteDelaySeconds < 0 {
		return fmt.Errorf("invalid paste_delay_seconds %d: must be >= 0 (0 = no delay)", c.PasteDelaySeconds)
	}

	return nil
}

// Warnings returns any non-fatal issues recorded during Load (currently:
// unknown top-level keys reported by the TOML decoder). Callers that want to
// surface these can print them to stderr.
func (c *Config) Warnings() []string {
	out := make([]string, len(c.warnings))
	copy(out, c.warnings)
	return out
}

// OutputDestinations parses [Config.OutputMode] into a boolean triple. It
// assumes Validate has already passed — if the field is malformed, all three
// booleans are false.
func (c *Config) OutputDestinations() (stdout, clipboard, paste bool) {
	stdout, clipboard, paste, _ = parseOutputMode(c.OutputMode)
	return
}

// APIKeyMissingError is returned by [ResolveAPIKey] when neither
// GROQ_API_KEY nor GROQ_API_TOKEN is set in the environment. Exported so
// callers that want to special-case the missing-key path (rather than rely
// on the error message) can use errors.As.
type APIKeyMissingError struct{}

// Error returns a friendly message naming both env vars and pointing the
// user at console.groq.com to get a free key.
func (*APIKeyMissingError) Error() string {
	return "Groq API key not found. Set GROQ_API_KEY (preferred) or GROQ_API_TOKEN in your environment. Get a free key at https://console.groq.com."
}

// ResolveAPIKey returns the Groq API key from the environment.
//
// Resolution order:
//  1. GROQ_API_KEY (preferred)
//  2. GROQ_API_TOKEN (accepted as a fallback for compatibility)
//
// If neither is set (or both are empty / whitespace-only), it returns an
// [*APIKeyMissingError] with a user-facing message.
//
// The key is intentionally not read from the on-disk config file: keeping
// secrets out of TOML reduces the risk of them ending up in git, backups,
// or shared screenshots.
func ResolveAPIKey() (string, error) {
	if v := strings.TrimSpace(os.Getenv("GROQ_API_KEY")); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv("GROQ_API_TOKEN")); v != "" {
		return v, nil
	}
	return "", &APIKeyMissingError{}
}

// parseOutputMode validates the raw string form of output_mode and returns
// the resolved destination triple.
func parseOutputMode(raw string) (stdout, clipboard, paste bool, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, false, false, fmt.Errorf("output_mode is empty: must be \"all\" or a comma-separated subset of stdout,clipboard,paste")
	}
	if trimmed == "all" {
		return true, true, true, nil
	}

	parts := strings.Split(trimmed, ",")
	seen := map[string]bool{}
	for _, p := range parts {
		key := strings.TrimSpace(p)
		if key == "" {
			return false, false, false, fmt.Errorf("output_mode %q contains an empty entry", raw)
		}
		if _, ok := validOutputModes[key]; !ok {
			return false, false, false, fmt.Errorf("output_mode entry %q is not valid: must be one of %s, or the literal \"all\"",
				key, joinKeys(validOutputModes))
		}
		seen[key] = true
	}
	return seen["stdout"], seen["clipboard"], seen["paste"], nil
}

// joinKeys returns a stable, comma-separated list of the keys in m. Used for
// error messages.
func joinKeys(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
