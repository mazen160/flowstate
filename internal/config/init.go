package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/mazin-ahmed/flowstate/internal/prompts"
)

// configTemplate is the canonical default config body. The three
// %s placeholders are filled in with TOML literal-multiline-encoded
// embedded prompts at write time. We hand-write the file (rather than using
// the toml encoder) so the comments survive — TOML encoders don't preserve
// comments on roundtrip, and the comments are user-facing documentation.
const configTemplate = `# Flowstate config file.
#
# The Groq API key is read from the GROQ_API_KEY environment variable
# (or GROQ_API_TOKEN as a fallback). It is NOT stored in this file.
# Get a free key from https://console.groq.com, then:
#
#   export GROQ_API_KEY=gsk_...
#
# Trigger mode. Picks how a recording is stopped.
#   "enter"          → start on launch, stop when the user presses Enter.
#   "push-to-talk"   → record only while ptt_key is held. Requires global
#                      keyboard hook permission (macOS: Accessibility).
trigger = "enter"
ptt_key = "space"

# Groq endpoint. Override only for a self-hosted OpenAI-compatible proxy.
base_url = "https://api.groq.com/openai/v1"

# Models.
transcription_model    = "whisper-large-v3"
cleanup_model          = "openai/gpt-oss-20b"
cleanup_fallback_model = "meta-llama/llama-4-scout-17b-16e-instruct"

# ISO-639-1 language code that biases transcription. Defaults to "en" (English).
# Set to "" to let Whisper auto-detect, or "fr", "es", "de", etc. for other languages.
language = "en"

# Output language for cleanup (translation target). Empty = same as spoken.
# Example: output_language = "French" — output will be translated to French
# regardless of what was spoken.
output_language = ""

# Audio input device. Empty = system default. Use ` + "`flowstate devices`" + ` to list.
input_device = ""

# Mute system audio output for the duration of the recording so playback from
# other apps doesn't bleed into the mic. Restored on stop.
mute_while_recording = true

# Output destination. Comma-separated combination of:
#   stdout, clipboard, paste
# or the special value "all".
output_mode = "stdout,clipboard"

# Preserve clipboard after paste. When paste output is enabled and this is
# true, flowstate saves the current clipboard, sets the transcript, pastes,
# then restores the previous clipboard ~500ms later. If you copy something
# else in that window, flowstate leaves your fresh copy alone.
preserve_clipboard_after_paste = true

# Active prompt key. Must match a key under [prompts] below.
active_prompt = "default"

# Custom vocabulary: words and phrases to preserve during cleanup.
# Separate entries with commas, newlines, or semicolons.
# These are appended to the cleanup system prompt as high-priority spellings.
custom_vocabulary = """
"""

# Terminal colors for the recording prompt and status lines.
#   "auto"   → emit ANSI colors only when stderr is a TTY and NO_COLOR is
#              unset. This is the documented default and is safe for piping.
#   "always" → force ANSI colors on (use this inside multiplexers that don't
#              propagate the TTY mode bit).
#   "never"  → suppress ANSI colors entirely. Equivalent to passing
#              --no-color on the command line or setting NO_COLOR=1.
colors = "auto"

# Auto-stop recording after this many seconds. 0 disables auto-stop so
# the recording continues until you press Enter (or release the PTT key).
# When > 0 the recording stops automatically after the configured time
# AND processes through the full pipeline (transcribe → cleanup → output
# → exit 0). You can still stop earlier with Enter; first signal wins.
# Override per-invocation with --max-time <seconds>.
max_time_seconds = 0

# Delay (in seconds) between writing the transcript to the clipboard and
# firing the paste keystroke. 0 = paste immediately (current behavior).
# When > 0 AND output_mode contains "paste", flowstate copies the transcript
# to the clipboard, waits this long, then sends the paste shortcut — gives
# you time to switch to the destination window. The clipboard-restore timer
# (preserve_clipboard_after_paste) starts AFTER the paste fires.
# Override per-invocation with --paste-delay <seconds>.
paste_delay_seconds = 0

[prompts]
# default — full FreeFlow-style cleanup with self-correction, formatting,
# and developer-syntax handling. See README for the long version.
default = %s

# command — transform highlighted text per a spoken instruction. Reserved
# for a future "edit mode" subcommand; ignored unless explicitly selected.
command = %s

# literal — minimal cleanup, no context-awareness. Matches the simple prompt
# in FreeFlow's README "Custom Cleanup" section.
literal = %s
`

// Init writes the canonical default config to path with the three embedded
// prompts inlined under [prompts].
//
// Behavior:
//   - Creates parent directories as needed (0700; the directory historically
//     held the API key, and the conservative perms remain so we don't widen
//     access for users who keep other secrets next to the config).
//   - Refuses to overwrite an existing file unless force is true.
//   - Applies 0600 perms on Unix (no-op on Windows).
func Init(path string, force bool) error {
	if path == "" {
		return errors.New("Init: empty path")
	}

	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing config at %s (pass --force to replace)", path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create config dir %s: %w", dir, err)
		}
	}

	body, err := renderDefault()
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := applyConfigPerms(path); err != nil {
		return fmt.Errorf("set perms on %s: %w", path, err)
	}
	return nil
}

// renderDefault produces the full default config body with the three
// embedded prompts inlined. Each prompt is TOML-encoded with a delimiter
// chosen so the resulting bytes roundtrip equal to prompts.Get(name).
func renderDefault() (string, error) {
	def, err := tomlMultiline(prompts.Default())
	if err != nil {
		return "", fmt.Errorf("encode default prompt: %w", err)
	}
	cmd, err := tomlMultiline(prompts.Command())
	if err != nil {
		return "", fmt.Errorf("encode command prompt: %w", err)
	}
	lit, err := tomlMultiline(prompts.Literal())
	if err != nil {
		return "", fmt.Errorf("encode literal prompt: %w", err)
	}
	return fmt.Sprintf(configTemplate, def, cmd, lit), nil
}

// tomlMultiline encodes s as a TOML multiline string. It prefers the
// literal form ('''...''') because that form preserves bytes verbatim — no
// backslash escapes, no whitespace folding. It falls back to a basic
// multiline string ("""...""") with the minimal escaping required if the
// payload itself contains a '''.
//
// A leading newline is inserted directly after the opening delimiter so the
// first line of the prompt isn't on the same line as the delimiter. TOML
// strips exactly one immediately-following newline, so this leading newline
// is consumed on decode and the prompt body roundtrips unchanged.
func tomlMultiline(s string) (string, error) {
	if !strings.Contains(s, "'''") {
		return "'''\n" + s + "'''", nil
	}

	// Fallback: basic multiline. Escape the characters TOML requires us to
	// escape inside a """..."""  string. Per the TOML spec the only
	// characters that need escaping in a basic multiline string are
	// backslash, and three-or-more consecutive double quotes. We
	// conservatively escape every double quote that is part of a """
	// sequence by replacing each `"""` with `""\"`.
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"""`, `""\"`)
	return "\"\"\"\n" + escaped + "\"\"\"", nil
}
