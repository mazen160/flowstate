// Package config holds Flowstate's TOML configuration: load, init, validate,
// and path resolution. The canonical schema is documented in the "Config
// Specification" doc; this package is the executable mirror of that spec.
package config

import (
	"fmt"
	"sort"
	"strings"
)

// Config models the on-disk TOML.
//
// Field names match the snake_case keys in the spec. Pointer-free zero values
// let us treat an unset field as "use the documented default" in [Defaults].
type Config struct {
	APIKey               string            `toml:"api_key"`
	Trigger              string            `toml:"trigger"`
	PTTKey               string            `toml:"ptt_key"`
	BaseURL              string            `toml:"base_url"`
	TranscriptionModel   string            `toml:"transcription_model"`
	CleanupModel         string            `toml:"cleanup_model"`
	CleanupFallbackModel string            `toml:"cleanup_fallback_model"`
	Language             string            `toml:"language"`
	InputDevice          string            `toml:"input_device"`
	MuteWhileRecording   bool              `toml:"mute_while_recording"`
	OutputMode           string            `toml:"output_mode"`
	ActivePrompt         string            `toml:"active_prompt"`
	Prompts              map[string]string `toml:"prompts"`

	// warnings is populated during Load for soft issues (e.g. unrecognized
	// keys surfaced by the TOML decoder). Read via [Config.Warnings].
	warnings []string `toml:"-"`
}

// Defaults returns a Config populated with the documented defaults. It does
// NOT inline the embedded prompts — [Init] does that so prompts only ship in
// freshly-written files (Load preserves whatever the user has on disk).
func Defaults() Config {
	return Config{
		APIKey:               "",
		Trigger:              "enter",
		PTTKey:               "space",
		BaseURL:              "https://api.groq.com/openai/v1",
		TranscriptionModel:   "whisper-large-v3",
		CleanupModel:         "openai/gpt-oss-20b",
		CleanupFallbackModel: "meta-llama/llama-4-scout-17b-16e-instruct",
		Language:             "",
		InputDevice:          "",
		MuteWhileRecording:   true,
		OutputMode:           "stdout,clipboard",
		ActivePrompt:         "default",
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

// Validate checks that the parsed Config is internally consistent. It does
// NOT require [Config.APIKey] to be non-empty — that's checked separately by
// [Config.RequireAPIKey] so `flowstate config init` and `config path` can
// run before the user has set a key.
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

// RequireAPIKey returns a friendly error if [Config.APIKey] is empty,
// pointing the user at the file they need to edit.
func (c *Config) RequireAPIKey(path string) error {
	if strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("api_key is empty; edit %s and add your Groq key (get one free at https://groq.com)", path)
	}
	return nil
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
