package main

import (
	"flag"
	"io"

	"github.com/mazin-ahmed/flowstate/internal/config"
)

// recordFlags is the parsed CLI flags for the default record command. Each
// override field uses the standard flag package's pointer-return form so we
// can tell "user didn't pass --foo" (Visit() never visited it) from "user
// passed --foo with an empty string". applyFlags below uses that distinction
// to decide whether to overwrite the config field.
type recordFlags struct {
	configPath string
	prompt     string
	output     string
	device     string
	trigger    string
	pttKey     string
	model      string
	noMute     bool
	noColor    bool
	maxTime    int
	pasteDelay int

	// wavSource is a HIDDEN test affordance. When set to a non-empty path,
	// runRecord skips the audio capture step entirely and uses that WAV
	// file as the transcription input. Intended for the end-to-end test
	// (tests/e2e) so it can exercise the full binary pipeline against a
	// fake Groq without needing a real microphone or any OS audio
	// permissions. NOT documented in --help; production users have no
	// reason to use it.
	wavSource string

	// set is populated from fs.Visit so applyFlags can distinguish unset
	// from explicit-empty. Keys are the long flag names (without the
	// leading dashes), e.g. "prompt", "output", "no-mute".
	set map[string]bool
}

// newRecordFlagSet builds a flag.FlagSet for the record command. Errors and
// usage output go to the provided writers so tests can drive the function
// without polluting os.Stderr.
func newRecordFlagSet(stderr io.Writer) (*flag.FlagSet, *recordFlags) {
	fs := flag.NewFlagSet("flowstate", flag.ContinueOnError)
	fs.SetOutput(stderr)

	rf := &recordFlags{set: map[string]bool{}}

	fs.StringVar(&rf.configPath, "config", "", "Path to config file (overrides $FLOWSTATE_CONFIG and the platform default).")
	fs.StringVar(&rf.prompt, "prompt", "", "Override active_prompt (key under [prompts]).")
	fs.StringVar(&rf.output, "output", "", "Override output_mode (comma list of stdout,clipboard,paste, or \"all\").")
	fs.BoolVar(&rf.noMute, "no-mute", false, "Disable mute_while_recording for this run.")
	fs.StringVar(&rf.device, "device", "", "Override input_device (UID or name).")
	fs.StringVar(&rf.trigger, "trigger", "", "Override trigger (\"enter\" or \"push-to-talk\").")
	fs.StringVar(&rf.pttKey, "ptt-key", "", "Override ptt_key (push-to-talk mode only).")
	fs.StringVar(&rf.model, "model", "", "Override cleanup_model.")
	fs.BoolVar(&rf.noColor, "no-color", false, "Disable ANSI colors in status output (equivalent to colors=\"never\").")
	fs.IntVar(&rf.maxTime, "max-time", 0, "Auto-stop recording after N seconds and process. 0 disables (default).")
	fs.IntVar(&rf.pasteDelay, "paste-delay", 0, "Wait N seconds between clipboard seed and paste keystroke. 0 = immediate (default).")
	// --wav-source is a hidden test affordance. Registered with the
	// flag set so Parse accepts it, but absent from helpText so users
	// don't accidentally discover it. The fact-finding test under
	// tests/e2e drives the full binary through this path.
	fs.StringVar(&rf.wavSource, "wav-source", "", "")

	return fs, rf
}

// captureSetFlags records, after Parse, which flags the user actually
// supplied. flag.FlagSet exposes this only via Visit(), so we collect it
// once into a map for cheap repeated checks in applyFlags.
func (rf *recordFlags) captureSetFlags(fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		rf.set[f.Name] = true
	})
}

// applyFlags overlays the CLI overrides onto cfg. Only fields the user
// explicitly set are modified, so flag-less invocations leave the on-disk
// config untouched. The boolean --no-mute is special: its presence (not its
// value) is what flips the field, so we check `set["no-mute"]` rather than
// rf.noMute directly.
func applyFlags(cfg *config.Config, rf *recordFlags) {
	if rf.set["prompt"] {
		cfg.ActivePrompt = rf.prompt
	}
	if rf.set["output"] {
		cfg.OutputMode = rf.output
	}
	if rf.set["device"] {
		cfg.InputDevice = rf.device
	}
	if rf.set["trigger"] {
		cfg.Trigger = rf.trigger
	}
	if rf.set["ptt-key"] {
		cfg.PTTKey = rf.pttKey
	}
	if rf.set["model"] {
		cfg.CleanupModel = rf.model
	}
	if rf.set["no-mute"] {
		// --no-mute is a "force off" switch; it doesn't take a value, so
		// observing it set means the user wants mute_while_recording=false.
		cfg.MuteWhileRecording = false
	}
	if rf.set["max-time"] {
		// 0 explicitly disables; negative values are rejected by Validate
		// if they ever sneak through (the flag itself accepts negatives —
		// IntVar has no min bound — so we defer enforcement to the config
		// layer's check).
		cfg.MaxTimeSeconds = rf.maxTime
	}
	if rf.set["paste-delay"] {
		cfg.PasteDelaySeconds = rf.pasteDelay
	}
}
