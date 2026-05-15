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

	// 7/8. Construct trigger and start capture. runCapture returns the
	// duration spent actually recording so the final Done line can split
	// "rec X · proc Y · total Z". Anything before runCapture (config load,
	// mute, recorder construction) is fast enough not to need its own
	// bucket; we treat it as effectively zero.
	wavPath, recDur, err := runCapture(ctx, cfg, recorder, reporter, stdin, stderr)
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

	// procStart bookends the processing phase: transcribe + cleanup +
	// output. It begins the moment recording stops, so the user-facing
	// "proc" timing reflects what they waited through after the Enter
	// keystroke (or auto-stop).
	procStart := time.Now()

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
	// Setting --paste-delay (or paste_delay_seconds) without enabling
	// paste in output_mode is a common foot-gun: the user expects the
	// delayed paste behavior but the keystroke step never fires because
	// pasteOn is false. Honor their intent — turn paste on for this run
	// and tell them what we did so they can pin it in config if they
	// want it persistent.
	if cfg.PasteDelaySeconds > 0 && !pasteOn {
		reporter.Warning("paste_delay_seconds is %d but paste is not in output_mode; auto-enabling paste for this run (set output_mode to include \"paste\" to silence this).", cfg.PasteDelaySeconds)
		pasteOn = true
	}
	dests := output.Destinations{
		Stdout:                 stdoutOn,
		Clipboard:              clipboardOn,
		Paste:                  pasteOn,
		PreservePriorClipboard: cfg.PreserveClipboardAfterPaste,
		PasteDelay:             time.Duration(cfg.PasteDelaySeconds) * time.Second,
	}
	// Tell the user we're about to wait so they can refocus before the
	// keystroke fires. Only shown when both Paste is enabled and a delay
	// is configured — silent on the immediate-paste path.
	if pasteOn && cfg.PasteDelaySeconds > 0 {
		reporter.Step(fmt.Sprintf("Pasting in %ds — switch to destination window", cfg.PasteDelaySeconds))
	}
	if err := output.WriteWithStdout(cleaned, dests, stdout); err != nil {
		reporter.Error("output: %v", err)
		return 1
	}

	// 14. Status line on stderr so stdout (which may have been piped to
	// another command) stays clean. Routed through the reporter so colors
	// + the duration tail come along for the ride.
	reporter.Done(ui.DoneStats{
		Chars:       len(cleaned),
		Words:       ui.CountWords(cleaned),
		Tokens:      ui.EstimateTokens(cleaned),
		RecordTime:  recDur,
		ProcessTime: time.Since(procStart),
	})
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
) (wavPath string, recDur time.Duration, err error) {
	// Derive an effective context. When max_time_seconds > 0, layer a
	// timeout on top of the caller's ctx. The timer becomes a clean stop
	// signal, NOT an error: when the trigger returns DeadlineExceeded and
	// we set the timeout ourselves, we still process the recording as
	// usual (transcribe → cleanup → output → exit 0).
	effCtx := ctx
	cancel := context.CancelFunc(func() {})
	if cfg.MaxTimeSeconds > 0 {
		effCtx, cancel = context.WithTimeout(ctx, time.Duration(cfg.MaxTimeSeconds)*time.Second)
	}
	defer cancel()
	timedAutoStop := func(err error) bool {
		return cfg.MaxTimeSeconds > 0 && errors.Is(err, context.DeadlineExceeded)
	}
	// User-facing hint for the recording prompt. "" lets the reporter use
	// its default "press Enter to stop"; with a deadline we substitute a
	// duration-aware message.
	hint := ""
	if cfg.MaxTimeSeconds > 0 {
		hint = fmt.Sprintf("auto-stop in %ds (or press Enter)", cfg.MaxTimeSeconds)
	}

	switch cfg.Trigger {
	case "enter":
		// Recording must start first so the user's "Recording…" prompt
		// is truthful by the time it appears.
		if err := recorder.Start(); err != nil {
			return "", 0, fmt.Errorf("start recorder: %w", err)
		}
		recStart := time.Now()
		// Kick off the meter (or print the static prompt when stderr is
		// not a TTY). stop must run before any subsequent reporter
		// output so the meter line is cleared cleanly.
		stop := reporter.Recording(recorder.PeakHistory, hint)
		// EnterTrigger's stdout writer is io.Discard here because the
		// reporter already printed the prompt. We pass a sentinel
		// io.Writer that swallows the trigger's own write so we don't
		// double-print "Recording…".
		t := trigger.NewEnterTrigger(stdin, io.Discard, "")
		if err := t.Run(effCtx, nil); err != nil && !timedAutoStop(err) {
			stop()
			// Best-effort stop on cancel so the temp WAV is at least
			// flushed before we surface the cancellation error.
			_, _ = recorder.Stop()
			return "", 0, fmt.Errorf("trigger: %w", err)
		}
		stop()
		recDur := time.Since(recStart)
		path, err := recorder.Stop()
		if err != nil {
			return "", 0, fmt.Errorf("stop recorder: %w", err)
		}
		return path, recDur, nil

	case "push-to-talk":
		t, err := trigger.NewPushToTalkTrigger(cfg.PTTKey)
		if err != nil {
			return "", 0, err
		}
		// onStart begins capture on key-down. If Start fails, the
		// trigger Run returns that error and we surface it as the
		// pipeline error. We print a static one-liner on key-down via
		// reporter.Step so the user sees that capture is live, and
		// stop is a no-op on the non-meter PTT path.
		ptHint := "Recording — release key to stop"
		if cfg.MaxTimeSeconds > 0 {
			ptHint = fmt.Sprintf("Recording — auto-stop in %ds (or release key)", cfg.MaxTimeSeconds)
		}
		// recStart is captured inside onStart so PTT-mode timing is the
		// "held the key" wall time, not "since runCapture began".
		var recStart time.Time
		onStart := func() error {
			if err := recorder.Start(); err != nil {
				return err
			}
			recStart = time.Now()
			reporter.Step(ptHint)
			return nil
		}
		if err := t.Run(effCtx, onStart); err != nil && !timedAutoStop(err) {
			_, _ = recorder.Stop()
			_ = t.Close()
			return "", 0, fmt.Errorf("trigger: %w", err)
		}
		_ = t.Close()
		recDur := time.Duration(0)
		if !recStart.IsZero() {
			recDur = time.Since(recStart)
		}
		path, err := recorder.Stop()
		if err != nil {
			return "", 0, fmt.Errorf("stop recorder: %w", err)
		}
		return path, recDur, nil

	default:
		// Validate() should have caught this; defensive guard for the
		// case where applyFlags overwrote Trigger with garbage.
		return "", 0, fmt.Errorf("invalid trigger %q (want enter or push-to-talk)", cfg.Trigger)
	}
}
