package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mazin-ahmed/flowstate/internal/audio"
	"github.com/mazin-ahmed/flowstate/internal/cleanup"
	"github.com/mazin-ahmed/flowstate/internal/config"
	"github.com/mazin-ahmed/flowstate/internal/mute"
	"github.com/mazin-ahmed/flowstate/internal/output"
	"github.com/mazin-ahmed/flowstate/internal/prompts"
	"github.com/mazin-ahmed/flowstate/internal/transcribe"
	"github.com/mazin-ahmed/flowstate/internal/trigger"
	"github.com/mazin-ahmed/flowstate/internal/ui"
)

// resolveColorMode collapses the four color inputs into a single
// [ui.ColorMode]. Precedence (highest first):
//
//  1. --no-color flag → never.
//  2. NO_COLOR env var (any non-empty value) → never.
//  3. cfg.Colors == "never" → never.
//  4. cfg.Colors == "always" → always.
//  5. cfg.Colors == "auto" or "" or any unknown value → auto.
//
// Extracted as a top-level helper so tests can exercise the precedence
// logic without spinning up the full record pipeline.
func resolveColorMode(cfgColors string, noColorFlag bool, env func(string) string) ui.ColorMode {
	if noColorFlag {
		return ui.ColorNever
	}
	if env("NO_COLOR") != "" {
		return ui.ColorNever
	}
	switch cfgColors {
	case "never":
		return ui.ColorNever
	case "always":
		return ui.ColorAlways
	default:
		// "auto", "" (legacy), or anything else not caught by Validate
		// falls through to the auto path. Validate normally rejects
		// unknown values, but a defensive default keeps a malformed
		// runtime override from crashing the pipeline.
		return ui.ColorAuto
	}
}

// runRecord is the default pipeline. It parses record-mode flags, loads the
// config, resolves the Groq API key from the environment (with a friendly
// error if it's not set), and then walks the
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
	//    A non-existent config is a pre-Reporter error path — we don't
	//    have a config to drive color resolution yet, so emit plain text.
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

	// 2. Apply CLI overrides BEFORE Reporter construction so any --trigger
	//    or --output override is reflected by the time we wire status
	//    messages. The --no-color flag is read directly in
	//    resolveColorMode; applyFlags doesn't touch cfg.Colors.
	applyFlags(cfg, rf)

	// 3. Construct the reporter now that we know the color mode. From
	//    here on, all stderr-bound status text is routed through it —
	//    plain prints are reserved for pre-config errors above.
	reporter := ui.NewReporter(stderr, resolveColorMode(cfg.Colors, rf.noColor, os.Getenv))

	for _, w := range cfg.Warnings() {
		reporter.Warning("%s", w)
	}

	// 4. Resolve the Groq API key from the environment. Done BEFORE mute
	//    and audio capture so a user who hasn't exported a key gets a
	//    fast, friendly error instead of seeing "Recording…" and then
	//    being told their key is missing after they've spoken.
	apiKey, err := config.ResolveAPIKey()
	if err != nil {
		reporter.Error("%v", err)
		return 1
	}

	// 5. Mute (best-effort). Always pair with a deferred Unmute so even a
	//    later error path restores the user's audio output.
	if cfg.MuteWhileRecording {
		if mErr := mute.Mute(); mErr != nil {
			reporter.Warning("mute failed: %v", mErr)
		}
		defer func() { _ = mute.Unmute() }()
	}

	// 6. Construct recorder. Open happens in Start, so this only fails on
	//    "internal precondition" errors that the package never returns in
	//    practice — we still check for completeness.
	recorder, err := audio.NewRecorder(cfg.InputDevice)
	if err != nil {
		reporter.Error("audio: %v", err)
		return 1
	}

	// t0 marks the start of the user-visible pipeline. We measure to the
	// end of cleanup + output for the "Done" line so the reported duration
	// matches "press Enter → see cleaned text" wall-clock time.
	t0 := time.Now()

	// 7/8. Construct trigger and start capture. The two trigger modes have
	// different start semantics: Enter assumes capture is already running
	// when Run blocks for the keystroke; PTT starts capture inside onStart
	// when the key goes DOWN. Pick the right wiring per cfg.Trigger.
	wavPath, err := runCapture(ctx, cfg, recorder, reporter, stdin, stderr)
	if err != nil {
		reporter.Error("%v", err)
		return 1
	}
	// Defer wav cleanup AFTER we know the path — capture may have returned
	// an empty path on error and there's nothing to remove in that case.
	defer func() {
		if wavPath != "" {
			_ = os.Remove(wavPath)
		}
	}()

	// 9. Transcribe. The transcribe client uses a 20 s default timeout
	// internally; we wrap ctx so a future Ctrl+C in the caller bubbles
	// here too.
	reporter.Step("Transcribing")
	tx := transcribe.NewClient(transcribe.Options{
		APIKey:   apiKey,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.TranscriptionModel,
		Language: cfg.Language,
	})
	raw, err := tx.Transcribe(ctx, wavPath)
	if err != nil {
		reporter.Error("transcribe: %v", err)
		return 1
	}
	// 10. Empty transcript -> hallucination-filtered OR genuinely silent.
	// Either way there's nothing to clean up; report and exit 0.
	if raw == "" {
		reporter.Warning("(no speech detected)")
		return 0
	}

	// 11. Cleanup pass. Resolve the active prompt body from the config's
	// own [prompts] table — NOT from internal/prompts, which is only the
	// init-time embed. Users may have customized the prompt after
	// `config init`, and we honor that.
	systemPrompt, ok := cfg.Prompts[cfg.ActivePrompt]
	if !ok {
		// Defensive: Validate() already rejects this case during Load.
		reporter.Error("active_prompt %q has no body under [prompts]", cfg.ActivePrompt)
		return 1
	}
	// FreeFlow-parity augmentations: translation directive and
	// high-priority vocabulary block. Both are no-ops when their
	// driving config field is empty, so the un-customized prompt is
	// byte-for-byte identical to the [prompts] entry.
	systemPrompt = prompts.ApplyOutputLanguage(systemPrompt, cfg.OutputLanguage)
	systemPrompt = prompts.ApplyVocabulary(systemPrompt, cfg.CustomVocabulary)

	reporter.Step("Cleaning up")
	cl := cleanup.NewClient(cleanup.Options{
		APIKey:        apiKey,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.CleanupModel,
		FallbackModel: cfg.CleanupFallbackModel,
	})
	cleaned, err := cl.Clean(ctx, systemPrompt, raw, "")
	if err != nil {
		reporter.Error("cleanup: %v", err)
		return 1
	}
	// 12. Empty cleaned text -> "EMPTY" sentinel from the LLM or genuine
	// blank-out. Same shape as no-speech: report and exit 0.
	if cleaned == "" {
		reporter.Warning("(no output)")
		return 0
	}

	// 13. Output. The destinations triple comes from the validated
	// config; output.WriteWithStdout lets us route stdout to the injected
	// writer for tests (main wires it to os.Stdout).
	stdoutOn, clipboardOn, pasteOn := cfg.OutputDestinations()
	dests := output.Destinations{
		Stdout:                 stdoutOn,
		Clipboard:              clipboardOn,
		Paste:                  pasteOn,
		PreservePriorClipboard: cfg.PreserveClipboardAfterPaste,
	}
	if err := output.WriteWithStdout(cleaned, dests, stdout); err != nil {
		reporter.Error("output: %v", err)
		return 1
	}

	// 14. Status line on stderr so stdout (which may have been piped to
	// another command) stays clean. Routed through the reporter so colors
	// + the duration tail come along for the ride.
	reporter.Done(len(cleaned), time.Since(t0))
	return 0
}

// runCapture handles the trigger-mode-specific wiring around recorder
// Start/Stop. It returns the temp WAV path on success, "" + an error on
// failure. Split out from runRecord so the long pipeline stays readable.
//
// Enter mode: capture starts BEFORE the trigger blocks (the user hears the
// "Recording…" prompt and knows recording is live). The reporter drives
// the animated meter when stderr is a TTY.
//
// PTT mode: capture starts in trigger.onStart, which fires on the key-down
// edge — this matches FreeFlow's hold-to-record gesture, where lifting the
// key both stops capture and uploads. We deliberately skip the animated
// meter here — gohook's global event loop and a redraw goroutine fighting
// over stderr in the same terminal cell tend to produce flicker, and the
// user already has tactile feedback from holding the key.
func runCapture(
	ctx context.Context,
	cfg *config.Config,
	recorder *audio.Recorder,
	reporter *ui.Reporter,
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
		// Kick off the meter (or print the static prompt when stderr is
		// not a TTY). stop must run before any subsequent reporter
		// output so the meter line is cleared cleanly.
		stop := reporter.Recording(recorder.PeakHistory)
		// EnterTrigger's stdout writer is io.Discard here because the
		// reporter already printed the prompt. We pass a sentinel
		// io.Writer that swallows the trigger's own write so we don't
		// double-print "Recording…".
		t := trigger.NewEnterTrigger(stdin, io.Discard, "")
		if err := t.Run(ctx, nil); err != nil {
			stop()
			// Best-effort stop on cancel so the temp WAV is at least
			// flushed before we surface the cancellation error.
			_, _ = recorder.Stop()
			return "", fmt.Errorf("trigger: %w", err)
		}
		stop()
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
		// pipeline error. We print a static one-liner on key-down via
		// reporter.Step so the user sees that capture is live, and
		// stop is a no-op on the non-meter PTT path.
		onStart := func() error {
			if err := recorder.Start(); err != nil {
				return err
			}
			reporter.Step("Recording — release key to stop")
			return nil
		}
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
