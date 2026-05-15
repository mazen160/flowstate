package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mazin-ahmed/flowstate/internal/audio"
	"github.com/mazin-ahmed/flowstate/internal/cleanup"
	"github.com/mazin-ahmed/flowstate/internal/config"
	"github.com/mazin-ahmed/flowstate/internal/mute"
	"github.com/mazin-ahmed/flowstate/internal/output"
	"github.com/mazin-ahmed/flowstate/internal/transcribe"
	"github.com/mazin-ahmed/flowstate/internal/trigger"
)

// runRecord is the default pipeline. It parses record-mode flags, loads the
// config (with friendly errors when missing or unkeyed), and then walks the
// load -> mute -> capture -> trigger -> transcribe -> cleanup -> output
// sequence documented in the Architecture & Pipeline doc.
//
// Returns the process exit code so main.run() stays a thin dispatcher.
func runRecord(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs, rf := newRecordFlagSet(stderr)
	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp -> usage already printed by fs; exit 0.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rf.captureSetFlags(fs)

	// 1. Resolve and load config. If the file is absent and the user is
	//    running the default command, that's a first-run; point them at
	//    `flowstate config init` rather than failing with a raw fs error.
	path := config.ResolvePath(rf.configPath)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "no config found at %s; run `flowstate config init` to create one\n", path)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}

	// 2. Apply CLI overrides BEFORE validation re-check. We don't re-run
	//    Validate here — applyFlags should not introduce invalid values
	//    because the flag inputs are free-form strings already validated
	//    downstream (e.g. trigger.NewPushToTalkTrigger rejects bad keys).
	applyFlags(cfg, rf)

	// 3. Require an API key. After overrides so a (currently-unused but
	//    spec-allowed) future --api-key flag would Just Work.
	if err := cfg.RequireAPIKey(path); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}

	// 4. Mute (best-effort). Always pair with a deferred Unmute so even a
	//    later error path restores the user's audio output.
	if cfg.MuteWhileRecording {
		if mErr := mute.Mute(); mErr != nil {
			fmt.Fprintf(stderr, "warning: mute failed: %v\n", mErr)
		}
		defer func() { _ = mute.Unmute() }()
	}

	// 5. Construct recorder. Open happens in Start, so this only fails on
	//    "internal precondition" errors that the package never returns in
	//    practice — we still check for completeness.
	recorder, err := audio.NewRecorder(cfg.InputDevice)
	if err != nil {
		fmt.Fprintf(stderr, "audio: %v\n", err)
		return 1
	}

	// 6/7. Construct trigger and start capture. The two trigger modes have
	// different start semantics: Enter assumes capture is already running
	// when Run blocks for the keystroke; PTT starts capture inside onStart
	// when the key goes DOWN. Pick the right wiring per cfg.Trigger.
	wavPath, err := runCapture(ctx, cfg, recorder, stdin, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	// Defer wav cleanup AFTER we know the path — capture may have returned
	// an empty path on error and there's nothing to remove in that case.
	defer func() {
		if wavPath != "" {
			_ = os.Remove(wavPath)
		}
	}()

	// 8/10. Transcribe. The transcribe client uses a 20 s default timeout
	// internally; we wrap ctx so a future Ctrl+C in the caller bubbles
	// here too.
	tx := transcribe.NewClient(transcribe.Options{
		APIKey:   cfg.APIKey,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.TranscriptionModel,
		Language: cfg.Language,
	})
	raw, err := tx.Transcribe(ctx, wavPath)
	if err != nil {
		fmt.Fprintf(stderr, "transcribe: %v\n", err)
		return 1
	}
	// 11. Empty transcript -> hallucination-filtered OR genuinely silent.
	// Either way there's nothing to clean up; report and exit 0.
	if raw == "" {
		fmt.Fprintln(stderr, "(no speech detected)")
		return 0
	}

	// 12. Cleanup pass. Resolve the active prompt body from the config's
	// own [prompts] table — NOT from internal/prompts, which is only the
	// init-time embed. Users may have customized the prompt after
	// `config init`, and we honor that.
	systemPrompt, ok := cfg.Prompts[cfg.ActivePrompt]
	if !ok {
		// Defensive: Validate() already rejects this case during Load.
		fmt.Fprintf(stderr, "active_prompt %q has no body under [prompts]\n", cfg.ActivePrompt)
		return 1
	}
	cl := cleanup.NewClient(cleanup.Options{
		APIKey:        cfg.APIKey,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.CleanupModel,
		FallbackModel: cfg.CleanupFallbackModel,
	})
	cleaned, err := cl.Clean(ctx, systemPrompt, raw, "")
	if err != nil {
		fmt.Fprintf(stderr, "cleanup: %v\n", err)
		return 1
	}
	// 13. Empty cleaned text -> "EMPTY" sentinel from the LLM or genuine
	// blank-out. Same shape as no-speech: report and exit 0.
	if cleaned == "" {
		fmt.Fprintln(stderr, "(no output)")
		return 0
	}

	// 14. Output. The destinations triple comes from the validated
	// config; output.WriteWithStdout lets us route stdout to the injected
	// writer for tests (main wires it to os.Stdout).
	stdoutOn, clipboardOn, pasteOn := cfg.OutputDestinations()
	dests := output.Destinations{
		Stdout:    stdoutOn,
		Clipboard: clipboardOn,
		Paste:     pasteOn,
	}
	if err := output.WriteWithStdout(cleaned, dests, stdout); err != nil {
		fmt.Fprintf(stderr, "output: %v\n", err)
		return 1
	}

	// 15. Status line on stderr so stdout (which may have been piped to
	// another command) stays clean.
	fmt.Fprintf(stderr, "Done. (%d characters)\n", len(cleaned))
	return 0
}

// runCapture handles the trigger-mode-specific wiring around recorder
// Start/Stop. It returns the temp WAV path on success, "" + an error on
// failure. Split out from runRecord so the long pipeline stays readable.
//
// Enter mode: capture starts BEFORE the trigger blocks (the user hears the
// "Recording…" prompt and knows recording is live).
//
// PTT mode: capture starts in trigger.onStart, which fires on the key-down
// edge — this matches FreeFlow's hold-to-record gesture, where lifting the
// key both stops capture and uploads.
func runCapture(
	ctx context.Context,
	cfg *config.Config,
	recorder *audio.Recorder,
	stdin io.Reader,
	stderr io.Writer,
) (string, error) {
	switch cfg.Trigger {
	case "enter":
		// Recording must start first so the user's "Recording…" prompt
		// is truthful by the time it appears.
		if err := recorder.Start(); err != nil {
			return "", fmt.Errorf("start recorder: %w", err)
		}
		t := trigger.NewEnterTrigger(stdin, stderr, "Recording… press Enter to stop\n")
		// onStart is a no-op here — capture is already running. We
		// pass nil so the trigger's "fire on entry" path doesn't
		// double-start anything.
		if err := t.Run(ctx, nil); err != nil {
			// Best-effort stop on cancel so the temp WAV is at least
			// flushed before we surface the cancellation error.
			_, _ = recorder.Stop()
			return "", fmt.Errorf("trigger: %w", err)
		}
		path, err := recorder.Stop()
		if err != nil {
			return "", fmt.Errorf("stop recorder: %w", err)
		}
		return path, nil

	case "push-to-talk":
		t, err := trigger.NewPushToTalkTrigger(cfg.PTTKey)
		if err != nil {
			return "", err
		}
		// onStart begins capture on key-down. If Start fails, the
		// trigger Run returns that error and we surface it as the
		// pipeline error.
		onStart := func() error { return recorder.Start() }
		if err := t.Run(ctx, onStart); err != nil {
			_, _ = recorder.Stop()
			_ = t.Close()
			return "", fmt.Errorf("trigger: %w", err)
		}
		_ = t.Close()
		path, err := recorder.Stop()
		if err != nil {
			return "", fmt.Errorf("stop recorder: %w", err)
		}
		return path, nil

	default:
		// Validate() should have caught this; defensive guard for the
		// case where applyFlags overwrote Trigger with garbage.
		return "", fmt.Errorf("invalid trigger %q (want enter or push-to-talk)", cfg.Trigger)
	}
}
