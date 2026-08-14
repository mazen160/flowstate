package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mazen160/flowstate/internal/prompts"
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
	if cfg.TranscriptionModel != "whisper-large-v3" {
		t.Errorf("TranscriptionModel = %q; want %q", cfg.TranscriptionModel, "whisper-large-v3")
	}
	if cfg.CleanupModel != "openai/gpt-oss-20b" {
		t.Errorf("CleanupModel = %q; want %q", cfg.CleanupModel, "openai/gpt-oss-20b")
	}
	if cfg.CleanupFallbackModel != "openai/gpt-oss-120b" {
		t.Errorf("CleanupFallbackModel = %q; want %q", cfg.CleanupFallbackModel, "openai/gpt-oss-120b")
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
trigger = "push-to-talk"
ptt_key = "f12"
base_url = "https://example.com/v1"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "openai/gpt-oss-120b"
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
cleanup_fallback_model = "openai/gpt-oss-120b"
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

// TestResolveAPIKey_GROQ_API_KEY pins the preferred env var: GROQ_API_KEY
// alone is enough and its value is returned verbatim.
func TestResolveAPIKey_GROQ_API_KEY(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gsk_primary")
	t.Setenv("GROQ_API_TOKEN", "")

	got, err := ResolveAPIKey()
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "gsk_primary" {
		t.Errorf("ResolveAPIKey() = %q; want %q", got, "gsk_primary")
	}
}

// TestResolveAPIKey_GROQ_API_TOKEN_Fallback verifies the compat fallback
// fires when the preferred var is unset.
func TestResolveAPIKey_GROQ_API_TOKEN_Fallback(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROQ_API_TOKEN", "gsk_fallback")

	got, err := ResolveAPIKey()
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "gsk_fallback" {
		t.Errorf("ResolveAPIKey() = %q; want %q", got, "gsk_fallback")
	}
}

// TestResolveAPIKey_BothSet_PrefersAPIKey pins the priority order when
// both env vars are populated: GROQ_API_KEY always wins.
func TestResolveAPIKey_BothSet_PrefersAPIKey(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gsk_primary")
	t.Setenv("GROQ_API_TOKEN", "gsk_fallback")

	got, err := ResolveAPIKey()
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "gsk_primary" {
		t.Errorf("ResolveAPIKey() = %q; want %q (GROQ_API_KEY should win)",
			got, "gsk_primary")
	}
}

// TestResolveAPIKey_NeitherSet_ReturnsError verifies the friendly error
// path. The message must name GROQ_API_KEY (so the user knows what to
// export) and point at console.groq.com (so they know where to get one).
func TestResolveAPIKey_NeitherSet_ReturnsError(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	t.Setenv("GROQ_API_TOKEN", "")

	got, err := ResolveAPIKey()
	if err == nil {
		t.Fatalf("ResolveAPIKey: want error, got value %q", got)
	}
	if got != "" {
		t.Errorf("ResolveAPIKey on error: got value %q; want empty", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "GROQ_API_KEY") {
		t.Errorf("error should mention GROQ_API_KEY; got: %v", err)
	}
	if !strings.Contains(msg, "console.groq.com") {
		t.Errorf("error should point at console.groq.com; got: %v", err)
	}

	// And it must be the typed sentinel error so callers can errors.As it.
	var missing *APIKeyMissingError
	if !errors.As(err, &missing) {
		t.Errorf("error should be *APIKeyMissingError; got %T", err)
	}
}

// TestDefaults_NewFields pins the documented defaults for the three
// settings ported in TASK-131.
func TestDefaults_NewFields(t *testing.T) {
	d := Defaults()
	if d.OutputLanguage != "" {
		t.Errorf("OutputLanguage default = %q; want \"\"", d.OutputLanguage)
	}
	if !d.PreserveClipboardAfterPaste {
		t.Errorf("PreserveClipboardAfterPaste default = false; want true")
	}
	if d.CustomVocabulary != "" {
		t.Errorf("CustomVocabulary default = %q; want \"\"", d.CustomVocabulary)
	}
}

// TestInit_WritesNewFields verifies the rendered config template surfaces
// the three new field names so users can find and edit them. We only
// assert presence-by-name — the surrounding comments are documentation
// and can change without breaking the field contract.
func TestInit_WritesNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, key := range []string{"output_language", "preserve_clipboard_after_paste", "custom_vocabulary"} {
		if !strings.Contains(string(body), key) {
			t.Errorf("rendered config missing %q", key)
		}
	}
}

// TestLoad_RoundtripsNewFields writes a hand-rolled config that sets the
// three new fields, then verifies Load reads them back.
func TestLoad_RoundtripsNewFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `
trigger = "enter"
ptt_key = "space"
base_url = "https://api.groq.com/openai/v1"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "openai/gpt-oss-120b"
output_mode = "stdout,clipboard,paste"
mute_while_recording = true
active_prompt = "default"
output_language = "French"
preserve_clipboard_after_paste = false
custom_vocabulary = """
alpha
beta, gamma
"""

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
	if cfg.OutputLanguage != "French" {
		t.Errorf("OutputLanguage = %q; want %q", cfg.OutputLanguage, "French")
	}
	if cfg.PreserveClipboardAfterPaste {
		t.Errorf("PreserveClipboardAfterPaste = true; want false")
	}
	if !strings.Contains(cfg.CustomVocabulary, "alpha") || !strings.Contains(cfg.CustomVocabulary, "gamma") {
		t.Errorf("CustomVocabulary missing expected terms: %q", cfg.CustomVocabulary)
	}
}

// TestDefaults_Colors pins the documented default for the colors field. A
// fresh config produced by Defaults() should opt into "auto" so a brand-new
// user gets colors on TTY without explicit configuration.
func TestDefaults_Colors(t *testing.T) {
	if got := Defaults().Colors; got != "auto" {
		t.Errorf("Defaults().Colors = %q; want %q", got, "auto")
	}
}

// TestValidate_Colors covers the small enum, including the empty-string
// tolerance: an existing config written before TASK-135 (and so missing
// the key entirely) must still pass Validate.
func TestValidate_Colors(t *testing.T) {
	cases := []struct {
		colors  string
		wantErr bool
	}{
		{"auto", false},
		{"always", false},
		{"never", false},
		{"", false},    // legacy configs: empty → treated as auto at runtime.
		{"AUTO", true}, // case-sensitive.
		{"sometimes", true},
		{"on", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.colors, func(t *testing.T) {
			cfg := validBaseConfig()
			cfg.Colors = tc.colors
			err := cfg.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Colors=%q: want error, got nil", tc.colors)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Colors=%q: unexpected error: %v", tc.colors, err)
			}
		})
	}
}

// TestInit_WritesColorsField verifies the init template surfaces the
// colors key so users can find and edit it in the generated file.
func TestInit_WritesColorsField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := Init(path, false); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "colors") {
		t.Errorf("rendered config missing colors key")
	}
	if !strings.Contains(string(body), `colors = "auto"`) {
		t.Errorf("rendered config should default colors to \"auto\"; body:\n%s", body)
	}
}

// TestLoad_RoundtripsColors verifies Load reads the colors field back from
// disk and that all three documented values survive the roundtrip.
func TestLoad_RoundtripsColors(t *testing.T) {
	for _, mode := range []string{"auto", "always", "never"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			body := `
trigger = "enter"
ptt_key = "space"
base_url = "https://api.groq.com/openai/v1"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "openai/gpt-oss-120b"
output_mode = "stdout"
mute_while_recording = true
active_prompt = "default"
colors = "` + mode + `"

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
			if cfg.Colors != mode {
				t.Errorf("Colors = %q; want %q", cfg.Colors, mode)
			}
		})
	}
}

// TestResolveAPIKey_WhitespaceOnly_ReturnsError makes sure that an env var
// set to whitespace doesn't accidentally pass as a valid key (which would
// lead to a useless 401 from Groq later in the pipeline).
func TestResolveAPIKey_WhitespaceOnly_ReturnsError(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "   ")
	t.Setenv("GROQ_API_TOKEN", "\t\n")

	if _, err := ResolveAPIKey(); err == nil {
		t.Fatal("ResolveAPIKey with whitespace-only env vars: want error, got nil")
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

// TestDefaults_MaxTimeSeconds pins the documented default: 0 (disabled).
// A non-zero default would silently auto-stop recordings for users who
// haven't opted into a max-time, which would be surprising.
func TestDefaults_MaxTimeSeconds(t *testing.T) {
	if got := Defaults().MaxTimeSeconds; got != 0 {
		t.Errorf("Defaults().MaxTimeSeconds = %d; want 0", got)
	}
}

// TestValidate_MaxTimeSeconds_Negative rejects negative values. Zero is
// the disabled sentinel; positives are honored as a duration in seconds.
func TestValidate_MaxTimeSeconds(t *testing.T) {
	cases := []struct {
		max     int
		wantErr bool
	}{
		{0, false},   // disabled
		{5, false},   // typical
		{600, false}, // 10 minutes — arbitrary upper-realm sanity
		{-1, true},   // negative is meaningless
		{-300, true},
	}
	for _, tc := range cases {
		c := validBaseConfig()
		c.MaxTimeSeconds = tc.max
		err := c.Validate()
		gotErr := err != nil
		if gotErr != tc.wantErr {
			t.Errorf("Validate(MaxTimeSeconds=%d) err = %v; wantErr = %v", tc.max, err, tc.wantErr)
		}
	}
}

// TestDefaults_PasteDelaySeconds pins the documented default: 0 (no delay).
// A non-zero default would silently pause paste output for users who
// didn't opt in.
func TestDefaults_PasteDelaySeconds(t *testing.T) {
	if got := Defaults().PasteDelaySeconds; got != 0 {
		t.Errorf("Defaults().PasteDelaySeconds = %d; want 0", got)
	}
}

// TestValidate_PasteDelaySeconds rejects negatives. Zero = no delay;
// positives stretch the gap between clipboard write and paste keystroke.
func TestValidate_PasteDelaySeconds(t *testing.T) {
	cases := []struct {
		delay   int
		wantErr bool
	}{
		{0, false},
		{1, false},
		{30, false},
		{-1, true},
	}
	for _, tc := range cases {
		c := validBaseConfig()
		c.PasteDelaySeconds = tc.delay
		err := c.Validate()
		gotErr := err != nil
		if gotErr != tc.wantErr {
			t.Errorf("Validate(PasteDelaySeconds=%d) err = %v; wantErr = %v", tc.delay, err, tc.wantErr)
		}
	}
}
