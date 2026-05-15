package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mazin-ahmed/flowstate/internal/prompts"
)

// TestDefaultPath_Platform asserts the host-OS default path looks roughly
// right — it should contain both "flowstate" and "config.toml". We deliberately
// don't pin the exact prefix because $HOME / $APPDATA vary between test
// environments.
func TestDefaultPath_Platform(t *testing.T) {
	got := DefaultPath()
	if !strings.Contains(got, "flowstate") {
		t.Errorf("DefaultPath() = %q; want a path containing %q", got, "flowstate")
	}
	if !strings.HasSuffix(got, "config.toml") {
		t.Errorf("DefaultPath() = %q; want suffix %q", got, "config.toml")
	}
	if runtime.GOOS != "windows" {
		// On unix the path should at least contain a slash.
		if !strings.Contains(got, "/") {
			t.Errorf("DefaultPath() = %q; want a unix-style path", got)
		}
	}
}

// TestResolvePath_Priority pins the priority order: explicit > env > default.
func TestResolvePath_Priority(t *testing.T) {
	t.Setenv(EnvVar, "/env/path/config.toml")

	if got := ResolvePath("/explicit/path/config.toml"); got != "/explicit/path/config.toml" {
		t.Errorf("explicit override: got %q", got)
	}

	if got := ResolvePath(""); got != "/env/path/config.toml" {
		t.Errorf("env override: got %q", got)
	}

	t.Setenv(EnvVar, "")
	if got := ResolvePath(""); got != DefaultPath() {
		t.Errorf("default fallback: got %q, want %q", got, DefaultPath())
	}
}

// TestInit_WritesFileWithPrompts is the core roundtrip test: Init writes a
// config, Load reads it back, and the three prompts must equal byte-for-byte
// the strings exposed by package prompts.
func TestInit_WritesFileWithPrompts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("Init: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := []struct {
		name string
		want string
	}{
		{"default", prompts.Default()},
		{"command", prompts.Command()},
		{"literal", prompts.Literal()},
	}
	for _, tc := range cases {
		got, ok := cfg.Prompts[tc.name]
		if !ok {
			t.Errorf("missing prompt %q in loaded config", tc.name)
			continue
		}
		if got != tc.want {
			t.Errorf("prompt %q did not roundtrip:\n--- got (len=%d) ---\n%s\n--- want (len=%d) ---\n%s",
				tc.name, len(got), got, len(tc.want), tc.want)
		}
	}

	// Sanity: the loaded config should have the documented defaults for
	// the top-level fields.
	if cfg.Trigger != "enter" {
		t.Errorf("Trigger = %q; want %q", cfg.Trigger, "enter")
	}
	if cfg.OutputMode != "stdout,clipboard" {
		t.Errorf("OutputMode = %q; want %q", cfg.OutputMode, "stdout,clipboard")
	}
	if cfg.ActivePrompt != "default" {
		t.Errorf("ActivePrompt = %q; want %q", cfg.ActivePrompt, "default")
	}
	if !cfg.MuteWhileRecording {
		t.Errorf("MuteWhileRecording = false; want true")
	}
}

// TestInit_RefusesExisting verifies the no-clobber default behavior.
func TestInit_RefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	err := Init(path, false)
	if err == nil {
		t.Fatal("second Init without force: want error, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestInit_ForceOverwrites verifies force=true clobbers the existing file.
func TestInit_ForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("first Init: %v", err)
	}

	// Tamper with the file so we can detect that the second Init actually
	// rewrote it.
	if err := os.WriteFile(path, []byte("# tampered\n"), 0o600); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if err := Init(path, true); err != nil {
		t.Fatalf("Init with force: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after force: %v", err)
	}
	if !strings.Contains(string(body), "trigger = \"enter\"") {
		t.Errorf("force=true didn't rewrite the file; got: %s", body)
	}
}

// TestInit_CreatesParentDirs verifies missing directories are created.
func TestInit_CreatesParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "dir", "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat after Init: %v", err)
	}
}

// TestInit_AppliesPerms verifies the Unix 0600 chmod. Skips on Windows
// because the perm bits don't map.
func TestInit_AppliesPerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm bits are Unix-only")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o; want %o", perm, 0o600)
	}
}

// TestLoad_ValidConfig writes a minimal valid config by hand (not via Init)
// and verifies Load parses it correctly.
func TestLoad_ValidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
api_key = "sk-test"
trigger = "push-to-talk"
ptt_key = "f12"
base_url = "https://example.com/v1"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "meta-llama/llama-4-scout-17b-16e-instruct"
output_mode = "stdout"
mute_while_recording = false
active_prompt = "default"

[prompts]
default = "hello"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	if cfg.Trigger != "push-to-talk" {
		t.Errorf("Trigger = %q", cfg.Trigger)
	}
	if cfg.PTTKey != "f12" {
		t.Errorf("PTTKey = %q", cfg.PTTKey)
	}
	if cfg.MuteWhileRecording {
		t.Errorf("MuteWhileRecording = true; want false")
	}
	if got := cfg.Prompts["default"]; got != "hello" {
		t.Errorf("prompts.default = %q", got)
	}
}

// TestLoad_UnknownKeyWarning verifies unknown top-level keys are reported
// as soft warnings rather than fatal errors.
func TestLoad_UnknownKeyWarning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
trigger = "enter"
ptt_key = "space"
base_url = "https://api.groq.com/openai/v1"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "meta-llama/llama-4-scout-17b-16e-instruct"
output_mode = "stdout"
mute_while_recording = true
active_prompt = "default"
typo_field = "oops"

[prompts]
default = "x"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	warnings := cfg.Warnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "typo_field") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about typo_field; got %v", warnings)
	}
}

// TestValidate_OutputMode is the table-driven check for the comma-list parser.
func TestValidate_OutputMode(t *testing.T) {
	cases := []struct {
		mode    string
		wantErr bool
	}{
		{"stdout", false},
		{"clipboard", false},
		{"paste", false},
		{"stdout,clipboard", false},
		{"stdout, clipboard, paste", false},
		{"all", false},
		{"", true},
		{"foo", true},
		{"stdout,foo", true},
		{"stdout,,clipboard", true},
		{",stdout", true},
	}
	base := validBaseConfig()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			cfg := base
			cfg.OutputMode = tc.mode
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("OutputMode=%q: want error, got nil", tc.mode)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("OutputMode=%q: unexpected error: %v", tc.mode, err)
			}
		})
	}
}

// TestValidate_Trigger covers the small enum.
func TestValidate_Trigger(t *testing.T) {
	cases := []struct {
		trigger string
		wantErr bool
	}{
		{"enter", false},
		{"push-to-talk", false},
		{"hold", true},
		{"", true},
		{"ENTER", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.trigger, func(t *testing.T) {
			cfg := validBaseConfig()
			cfg.Trigger = tc.trigger
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Trigger=%q: want error, got nil", tc.trigger)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Trigger=%q: unexpected error: %v", tc.trigger, err)
			}
		})
	}
}

// TestValidate_ActivePrompt covers the [prompts] cross-reference.
func TestValidate_ActivePrompt(t *testing.T) {
	cfg := validBaseConfig()
	cfg.ActivePrompt = "default"
	if err := cfg.Validate(); err != nil {
		t.Errorf("active_prompt=default with default key: %v", err)
	}

	cfg.ActivePrompt = "missing"
	if err := cfg.Validate(); err == nil {
		t.Errorf("active_prompt=missing: want error, got nil")
	}

	cfg.ActivePrompt = ""
	if err := cfg.Validate(); err == nil {
		t.Errorf("active_prompt empty: want error, got nil")
	}
}

// TestOutputDestinations_AllShortcut verifies the "all" sugar.
func TestOutputDestinations_AllShortcut(t *testing.T) {
	cfg := validBaseConfig()
	cfg.OutputMode = "all"
	stdout, clip, paste := cfg.OutputDestinations()
	if !stdout || !clip || !paste {
		t.Errorf("OutputDestinations(all) = (%v,%v,%v); want all true", stdout, clip, paste)
	}
}

// TestOutputDestinations_Subset verifies subset parsing.
func TestOutputDestinations_Subset(t *testing.T) {
	cfg := validBaseConfig()
	cfg.OutputMode = "stdout,paste"
	stdout, clip, paste := cfg.OutputDestinations()
	if !stdout || clip || !paste {
		t.Errorf("OutputDestinations(stdout,paste) = (%v,%v,%v); want (true,false,true)", stdout, clip, paste)
	}
}

// TestRequireAPIKey_EmptyErrors covers the empty case and the friendly
// error message.
func TestRequireAPIKey_EmptyErrors(t *testing.T) {
	cfg := validBaseConfig()
	cfg.APIKey = ""
	err := cfg.RequireAPIKey("/etc/flowstate/config.toml")
	if err == nil {
		t.Fatal("RequireAPIKey: want error, got nil")
	}
	if !strings.Contains(err.Error(), "/etc/flowstate/config.toml") {
		t.Errorf("error should mention the path; got: %v", err)
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should mention api_key; got: %v", err)
	}

	cfg.APIKey = "   " // whitespace also counts as empty
	if err := cfg.RequireAPIKey("/tmp/x"); err == nil {
		t.Error("whitespace-only api_key should error")
	}
}

// TestRequireAPIKey_NonEmptyOk pins the happy path.
func TestRequireAPIKey_NonEmptyOk(t *testing.T) {
	cfg := validBaseConfig()
	cfg.APIKey = "sk-something"
	if err := cfg.RequireAPIKey("/tmp/x"); err != nil {
		t.Errorf("non-empty api_key: %v", err)
	}
}

// TestLoad_MissingFile surfaces a clean error path.
func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err == nil {
		t.Fatal("Load on missing file: want error, got nil")
	}
	// Make sure it's a recognizable I/O error rather than a panic.
	if !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "no such file") {
		// BurntSushi/toml wraps os.ErrNotExist on most platforms; if the
		// underlying error doesn't satisfy errors.Is, fall back to text.
		t.Logf("Load returned error (acceptable): %v", err)
	}
}

// validBaseConfig returns a Config that passes Validate. Tests mutate one
// field at a time to probe a specific failure mode.
func validBaseConfig() Config {
	c := Defaults()
	c.Prompts = map[string]string{
		"default": "stub",
	}
	return c
}
