package e2e

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mazen160/flowstate/internal/cleanup"
	"github.com/mazen160/flowstate/internal/transcribe"
)

// TestFlowstateE2E_CLISurface builds the flowstate binary and exercises its
// user-facing subcommands. Each subtest is independent so a failure in one
// (e.g. `devices` on a headless host) does not mask the others.
//
// The binary itself is built once in TestMain-style setup at the top of
// the function so the slow `go build` cost is paid only once per `go test`
// run of this package.
func TestFlowstateE2E_CLISurface(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping e2e binary build")
	}

	bin := buildBinary(t)

	t.Run("version", func(t *testing.T) {
		out, code := runBinary(t, bin, nil, "version")
		if code != 0 {
			t.Fatalf("flowstate version exit code = %d, want 0; output=%q", code, out)
		}
		if strings.TrimSpace(out) == "" {
			t.Fatalf("flowstate version produced empty stdout")
		}
		if !strings.Contains(out, "flowstate") {
			t.Fatalf("flowstate version stdout missing 'flowstate': %q", out)
		}
	})

	t.Run("help", func(t *testing.T) {
		out, code := runBinary(t, bin, nil, "--help")
		if code != 0 {
			t.Fatalf("flowstate --help exit code = %d, want 0; output=%q", code, out)
		}
		// The help text references at least the "Usage" header and the
		// `version` subcommand. We avoid pinning the exact wording so
		// minor doc tweaks don't break the smoke test.
		if !strings.Contains(out, "Usage") {
			t.Fatalf("flowstate --help missing 'Usage' line: %q", out)
		}
		if !strings.Contains(out, "version") {
			t.Fatalf("flowstate --help missing 'version' subcommand mention: %q", out)
		}
	})

	t.Run("config init writes file", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "cfg.toml")

		// stderr carries the "wrote config to ..." line — we just want
		// the exit code and the file's presence afterwards.
		_, code := runBinary(t, bin, nil, "config", "init", "--config", cfgPath)
		if code != 0 {
			t.Fatalf("flowstate config init exit code = %d, want 0", code)
		}
		info, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("config file not created at %s: %v", cfgPath, err)
		}
		if info.Size() == 0 {
			t.Fatalf("config file at %s is empty", cfgPath)
		}
	})

	t.Run("config path honors FLOWSTATE_CONFIG", func(t *testing.T) {
		dir := t.TempDir()
		want := filepath.Join(dir, "cfg.toml")

		out, code := runBinary(t, bin, []string{"FLOWSTATE_CONFIG=" + want}, "config", "path")
		if code != 0 {
			t.Fatalf("flowstate config path exit code = %d, want 0; output=%q", code, out)
		}
		got := strings.TrimSpace(out)
		if got != want {
			t.Fatalf("flowstate config path stdout = %q, want %q", got, want)
		}
	})

	t.Run("devices runs without panicking", func(t *testing.T) {
		// Device enumeration is host-dependent: a CI runner without an
		// input device prints "no input devices found" and exits 1.
		// We accept both 0 and 1, but not 2 (flag error) or signals.
		_, code := runBinary(t, bin, nil, "devices")
		if code != 0 && code != 1 {
			t.Fatalf("flowstate devices exit code = %d, want 0 or 1", code)
		}
	})
}

// TestFlowstateE2E_WireLevelGroq stands up an httptest server that fakes
// Groq's /audio/transcriptions and /chat/completions endpoints, then drives
// internal/transcribe and internal/cleanup against it. This is the closest
// thing to an end-to-end pipeline check we can do without driving real audio
// capture — it verifies the bytes-on-the-wire behavior of the two HTTP
// clients that actually talk to Groq in production.
//
// What it does NOT verify: audio capture, the trigger goroutines, mute/paste
// helpers, output dispatch, config loading. Those each have their own unit
// tests under internal/*.
func TestFlowstateE2E_WireLevelGroq(t *testing.T) {
	// 1. Stand up the fake Groq server.
	var (
		gotTranscribeAuth string
		gotCleanupAuth    string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		gotTranscribeAuth = r.Header.Get("Authorization")

		// Canned verbose_json: text + a single segment with a low
		// no_speech_prob so the hallucination filter does NOT trip.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"text": "hello world",
			"segments": []map[string]any{
				{"no_speech_prob": 0.01},
			},
		})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		gotCleanupAuth = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]string{
						"role":    "assistant",
						"content": "Hello, world.",
					},
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 2. Write a fixture WAV file to a temp dir. The transcribe client just
	// streams the bytes into the multipart body — the fake server doesn't
	// inspect them — so a minimal 44-byte WAV header plus a tiny PCM payload
	// is enough.
	tmpDir := t.TempDir()
	wavPath := filepath.Join(tmpDir, "fixture.wav")
	if err := writeFixtureWAV(wavPath); err != nil {
		t.Fatalf("write fixture wav: %v", err)
	}

	// 3. Write a config file pointing at the fake server. We don't *use* it
	// (the wire-level subtest skips the binary), but its existence on disk
	// proves the spec-described shape of the config is writable in a temp
	// dir and matches what the README documents.
	cfgPath := filepath.Join(tmpDir, "config.toml")
	cfg := `# fixture config for the e2e wire-level smoke test
# (API key now comes from GROQ_API_KEY env var; not written to config)
trigger = "enter"
output_mode = "stdout"
mute_while_recording = false
base_url = "` + srv.URL + `"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "openai/gpt-oss-120b"
active_prompt = "default"

[prompts]
default = "system prompt body"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}

	// 4. Drive transcribe + cleanup against the fake server.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tx := transcribe.NewClient(transcribe.Options{
		APIKey:  "fake",
		BaseURL: srv.URL,
		Model:   "whisper-large-v3",
	})
	raw, err := tx.Transcribe(ctx, wavPath)
	if err != nil {
		t.Fatalf("transcribe against fake server failed: %v", err)
	}
	if raw != "hello world" {
		t.Fatalf("transcribe text = %q, want %q", raw, "hello world")
	}
	if gotTranscribeAuth != "Bearer fake" {
		t.Fatalf("fake server saw Authorization=%q for transcribe, want %q",
			gotTranscribeAuth, "Bearer fake")
	}

	cl := cleanup.NewClient(cleanup.Options{
		APIKey:  "fake",
		BaseURL: srv.URL,
		Model:   "openai/gpt-oss-20b",
	})
	cleaned, err := cl.Clean(ctx, "system prompt body", raw, "")
	if err != nil {
		t.Fatalf("cleanup against fake server failed: %v", err)
	}
	if cleaned != "Hello, world." {
		t.Fatalf("cleanup text = %q, want %q", cleaned, "Hello, world.")
	}
	if gotCleanupAuth != "Bearer fake" {
		t.Fatalf("fake server saw Authorization=%q for cleanup, want %q",
			gotCleanupAuth, "Bearer fake")
	}
}

// buildBinary compiles the flowstate command into the test's temp dir and
// returns the absolute path to the resulting binary. On Windows it appends
// `.exe`. Build failures are fatal — the rest of the test is meaningless
// without a binary.
func buildBinary(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	name := "flowstate"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(dir, name)

	// We build from the package path, not from a hard-coded relative dir,
	// so the test works regardless of where `go test` is invoked from.
	cmd := exec.Command("go", "build", "-o", out, "github.com/mazen160/flowstate/cmd/flowstate")
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, combined)
	}
	return out
}

// runBinary invokes the built binary with the given args and optional extra
// environment variables (in `KEY=value` form). It returns the combined
// stdout+stderr output and the exit code. A signal kill is reported as -1.
//
// We capture combined output rather than separating streams because the
// CLI surface tests only check substrings; the few cases that need to
// inspect stdout specifically (config path) emit nothing to stderr on
// success so the result is unambiguous.
func runBinary(t *testing.T, bin string, extraEnv []string, args ...string) (string, int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	// Start from os.Environ so PATH, HOME, etc. are inherited (the binary
	// uses them for config-path defaults).
	cmd.Env = append(os.Environ(), extraEnv...)

	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	// exec.ExitError carries the exit code; anything else (e.g. context
	// deadline) is reported as -1 so the test can distinguish kills from
	// real non-zero exits.
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode()
	}
	return string(out), -1
}

// writeFixtureWAV writes a minimal 44-byte RIFF/WAVE header followed by 32
// zero PCM samples (64 bytes) to path. The content is not meaningful audio —
// the fake Groq server doesn't decode it — but it gives the transcribe
// client a real file to upload, exercising the multipart body builder's
// file-reading path.
func writeFixtureWAV(path string) error {
	const (
		sampleRate    uint32 = 16000
		numChannels   uint16 = 1
		bitsPerSample uint16 = 16
		numSamples    uint32 = 32 // 32 samples * 2 bytes = 64 bytes of data.
	)

	dataSize := numSamples * uint32(numChannels) * uint32(bitsPerSample/8)
	byteRate := sampleRate * uint32(numChannels) * uint32(bitsPerSample/8)
	blockAlign := numChannels * (bitsPerSample / 8)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// RIFF header.
	if _, err := f.Write([]byte("RIFF")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(36+dataSize)); err != nil {
		return err
	}
	if _, err := f.Write([]byte("WAVE")); err != nil {
		return err
	}

	// fmt chunk.
	if _, err := f.Write([]byte("fmt ")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint32(16)); err != nil { // PCM fmt chunk size.
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, uint16(1)); err != nil { // PCM format.
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, numChannels); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, sampleRate); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, byteRate); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, blockAlign); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, bitsPerSample); err != nil {
		return err
	}

	// data chunk.
	if _, err := f.Write([]byte("data")); err != nil {
		return err
	}
	if err := binary.Write(f, binary.LittleEndian, dataSize); err != nil {
		return err
	}
	// Zeroed PCM payload.
	zero := make([]byte, dataSize)
	if _, err := f.Write(zero); err != nil {
		return err
	}
	return nil
}

// TestFlowstateE2E_FullBinaryPipeline drives the built binary through the
// FULL record pipeline end-to-end: it builds the binary, stands up an
// httptest mock for Groq, writes a temp config that points the binary at
// the mock, and invokes `./flowstate --wav-source <fixture>` with an
// output_mode that lands the cleaned transcript on stdout. The hidden
// --wav-source flag lets the test bypass real microphone capture; every
// other step of the orchestrator (load config → resolve API key →
// transcribe → cleanup → output) runs exactly as it would for a real user.
//
// The previous two e2e tests cover the CLI surface (version/config/etc.)
// and the wire-level Groq integration via package-level imports. This
// one is the missing layer: a regression in cmd/flowstate/record.go that
// breaks the pipeline wiring (e.g. swapping the transcribe and cleanup
// client constructors) would surface here even if every internal package
// test passes.
func TestFlowstateE2E_FullBinaryPipeline(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; skipping e2e binary build")
	}

	bin := buildBinary(t)

	// Mock Groq endpoints. Same canned content as TestFlowstateE2E_WireLevelGroq
	// — keeps the two tests assert against the same expected output so a
	// reader can tell at a glance that the binary path produces the same
	// result as the library path.
	const cannedRaw = "this is a binary e2e probe"
	const cannedCleaned = "This is a binary E2E probe."
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"` + cannedRaw + `","segments":[{"no_speech_prob":0.01}]}`))
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + cannedCleaned + `"}}]}`))
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()

	// Fixture: a real WAV file on disk that --wav-source can hand to the
	// transcribe client. The bytes never reach a real decoder (Groq is
	// mocked) but a valid 44-byte header keeps the file looking realistic.
	tmpDir := t.TempDir()
	wavPath := filepath.Join(tmpDir, "fixture.wav")
	if err := writeFixtureWAV(wavPath); err != nil {
		t.Fatalf("writeFixtureWAV: %v", err)
	}

	// Config pointing at the mock. output_mode = stdout keeps the test
	// hermetic — no clipboard init, no paste keystroke, no real output
	// destination beyond the captured CombinedOutput from runBinary.
	// mute_while_recording = false so the test doesn't muck with the
	// host's audio output during the run.
	cfgPath := filepath.Join(tmpDir, "flowstate.toml")
	cfg := `
trigger = "enter"
ptt_key = "space"
base_url = "` + upstream.URL + `"
transcription_model = "whisper-large-v3"
cleanup_model = "openai/gpt-oss-20b"
cleanup_fallback_model = "openai/gpt-oss-120b"
language = "en"
output_language = ""
input_device = ""
mute_while_recording = false
output_mode = "stdout"
preserve_clipboard_after_paste = false
active_prompt = "default"
custom_vocabulary = ""
colors = "never"

[prompts]
default = "test-default"
command = "test-command"
literal = "test-literal"
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Drive the full pipeline.
	out, code := runBinary(t, bin, []string{
		"GROQ_API_KEY=test-key",
		"NO_COLOR=1",
	}, "--config", cfgPath, "--wav-source", wavPath)

	if code != 0 {
		t.Fatalf("flowstate full-pipeline exit code = %d, want 0\noutput:\n%s", code, out)
	}

	// stdout must contain the cleaned text. Combined output mixes
	// stderr-bound status lines with the stdout transcript, so we just
	// check substring membership rather than expecting exact equality.
	if !strings.Contains(out, cannedCleaned) {
		t.Fatalf("expected cleaned transcript %q in output, got:\n%s", cannedCleaned, out)
	}
	// Sanity: the raw transcript must NOT appear on stdout — it's only
	// exposed through the cleanup pass. (It MAY appear in a debug log
	// somewhere, but the binary's stdout/stderr never carry it today.)
	if strings.Contains(out, cannedRaw) {
		t.Fatalf("raw transcript leaked into output (should only surface cleaned):\n%s", out)
	}
}
