package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mazin-ahmed/flowstate/internal/config"
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
