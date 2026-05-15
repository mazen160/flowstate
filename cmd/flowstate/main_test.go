package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mazin-ahmed/flowstate/internal/config"
	"github.com/mazin-ahmed/flowstate/internal/ui"
)

// TestVersionSubcommand drives runVersion against a bytes.Buffer and asserts
// the output looks roughly like what we promise on the tin. We don't pin the
// exact version string so bumping `version` doesn't force a test update.
func TestVersionSubcommand(t *testing.T) {
	var buf bytes.Buffer
	runVersion(&buf)
	got := buf.String()
	if !strings.Contains(got, "flowstate") {
		t.Fatalf("version output missing 'flowstate': %q", got)
	}
	if !strings.Contains(got, "go ") {
		t.Fatalf("version output missing Go runtime: %q", got)
	}
}

// TestConfigPathSubcommand verifies that runConfigPath honors $FLOWSTATE_CONFIG
// (via config.ResolvePath) and prints exactly that path + newline to stdout.
// Using t.Setenv keeps the test hermetic — Go restores the var on cleanup.
func TestConfigPathSubcommand(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "config.toml")
	t.Setenv(config.EnvVar, want)

	var stdout, stderr bytes.Buffer
	code := runConfigPath(nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runConfigPath exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	got := stdout.String()
	if got != want+"\n" {
		t.Fatalf("runConfigPath stdout = %q, want %q", got, want+"\n")
	}
}

// TestApplyFlags_OverridesPrompt checks that a --prompt=literal flag flips
// the config's ActivePrompt to "literal" — the primary path users exercise
// when they want a one-off prompt override.
func TestApplyFlags_OverridesPrompt(t *testing.T) {
	cfg := config.Defaults()
	cfg.ActivePrompt = "default"

	fs, rf := newRecordFlagSet(&bytes.Buffer{})
	if err := fs.Parse([]string{"--prompt=literal"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	rf.captureSetFlags(fs)
	applyFlags(&cfg, rf)

	if cfg.ActivePrompt != "literal" {
		t.Fatalf("ActivePrompt = %q, want %q", cfg.ActivePrompt, "literal")
	}
}

// TestApplyFlags_NoMute verifies that the boolean --no-mute presence
// (not its value) flips mute_while_recording to false. The default config
// has MuteWhileRecording=true so this exercises a real change.
func TestApplyFlags_NoMute(t *testing.T) {
	cfg := config.Defaults()
	if !cfg.MuteWhileRecording {
		t.Fatalf("precondition: defaults should have MuteWhileRecording=true")
	}

	fs, rf := newRecordFlagSet(&bytes.Buffer{})
	if err := fs.Parse([]string{"--no-mute"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	rf.captureSetFlags(fs)
	applyFlags(&cfg, rf)

	if cfg.MuteWhileRecording {
		t.Fatalf("MuteWhileRecording = true, want false after --no-mute")
	}
}

// TestApplyFlags_Output checks that --output=stdout,paste sets OutputMode to
// exactly that string. We don't reparse it here; that's the config package's
// responsibility (covered by OutputDestinations in config_test.go).
func TestApplyFlags_Output(t *testing.T) {
	cfg := config.Defaults()

	fs, rf := newRecordFlagSet(&bytes.Buffer{})
	if err := fs.Parse([]string{"--output=stdout,paste"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	rf.captureSetFlags(fs)
	applyFlags(&cfg, rf)

	if cfg.OutputMode != "stdout,paste" {
		t.Fatalf("OutputMode = %q, want %q", cfg.OutputMode, "stdout,paste")
	}
}

// TestResolveColorMode_Precedence pins the documented precedence ladder.
// The whole point of this helper is that command-line and env overrides win
// over config, and that NO_COLOR=1 acts as a kill-switch even when the
// config says "always". A test on the helper rather than the full runRecord
// pipeline keeps us off the audio device.
func TestResolveColorMode_Precedence(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	noColorEnv := func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}

	cases := []struct {
		name        string
		cfgColors   string
		noColorFlag bool
		env         func(string) string
		want        ui.ColorMode
	}{
		{"flag_wins_over_always_config", "always", true, emptyEnv, ui.ColorNever},
		{"env_wins_over_always_config", "always", false, noColorEnv, ui.ColorNever},
		{"config_never_with_no_flag_no_env", "never", false, emptyEnv, ui.ColorNever},
		{"config_always_with_no_flag_no_env", "always", false, emptyEnv, ui.ColorAlways},
		{"config_auto_with_no_flag_no_env", "auto", false, emptyEnv, ui.ColorAuto},
		{"empty_legacy_config_treated_as_auto", "", false, emptyEnv, ui.ColorAuto},
		{"unknown_value_falls_to_auto", "garbage", false, emptyEnv, ui.ColorAuto},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := resolveColorMode(tc.cfgColors, tc.noColorFlag, tc.env)
			if got != tc.want {
				t.Errorf("resolveColorMode(%q, %v) = %v; want %v",
					tc.cfgColors, tc.noColorFlag, got, tc.want)
			}
		})
	}
}

// TestRecordOutput_NoANSIWhenStderrIsBuffer is the end-to-end guard for the
// "no escape codes when piped" contract. We can't easily drive runRecord
// without a real audio device, but the reporter built from a bytes.Buffer
// stderr must never emit ANSI on its own — regardless of how cfg.Colors is
// set, NO_COLOR's presence, or the --no-color flag. We exercise this by
// constructing a reporter the same way runRecord does and confirming all
// four documented "disable" paths produce zero-ANSI output.
func TestRecordOutput_NoANSIWhenStderrIsBuffer(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	paths := []struct {
		name        string
		cfgColors   string
		noColorFlag bool
		setNoColor  bool
	}{
		{"auto_with_buffer_stderr", "auto", false, false},
		{"explicit_never", "never", false, false},
		{"no_color_flag", "auto", true, false},
		{"no_color_env", "auto", false, true},
	}
	for _, p := range paths {
		p := p
		t.Run(p.name, func(t *testing.T) {
			if p.setNoColor {
				t.Setenv("NO_COLOR", "1")
			} else {
				t.Setenv("NO_COLOR", "")
			}
			mode := resolveColorMode(p.cfgColors, p.noColorFlag, os.Getenv)
			var buf bytes.Buffer
			r := ui.NewReporter(&buf, mode)

			// Drive every reporter method that flows through stderr in
			// runRecord. Each must produce ANSI-free output.
			r.Warning("warn message")
			r.Step("Transcribing")
			r.Step("Cleaning up")
			r.Error("transcribe: %v", "boom")
			stop := r.Recording(func() []float64 { return nil })
			stop()

			if strings.Contains(buf.String(), "\x1b") {
				t.Fatalf("path %q leaked ANSI to non-TTY stderr:\n%q",
					p.name, buf.String())
			}
		})
	}
}

// TestApplyFlags_UnsetFlagsDoNotOverride is a regression guard: parsing an
// empty flag list must leave the config exactly as-is. The bug it catches
// is `applyFlags` reading rf.field directly (which would zero out config
// fields) instead of checking rf.set["..."].
func TestApplyFlags_UnsetFlagsDoNotOverride(t *testing.T) {
	cfg := config.Defaults()
	cfg.ActivePrompt = "literal"
	cfg.InputDevice = "Builtin Mic"
	cfg.CleanupModel = "openai/gpt-oss-20b"

	fs, rf := newRecordFlagSet(&bytes.Buffer{})
	if err := fs.Parse([]string{}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	rf.captureSetFlags(fs)
	applyFlags(&cfg, rf)

	if cfg.ActivePrompt != "literal" {
		t.Fatalf("ActivePrompt clobbered to %q", cfg.ActivePrompt)
	}
	if cfg.InputDevice != "Builtin Mic" {
		t.Fatalf("InputDevice clobbered to %q", cfg.InputDevice)
	}
	if cfg.CleanupModel != "openai/gpt-oss-20b" {
		t.Fatalf("CleanupModel clobbered to %q", cfg.CleanupModel)
	}
}
