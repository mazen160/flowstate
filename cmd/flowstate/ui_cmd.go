package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/mazen160/flowstate/internal/config"
	"github.com/mazen160/flowstate/internal/gui"
)

// runUI implements `flowstate ui`: the native desktop UI (Fyne, no browser,
// no Electron). Flag parsing mirrors the record pipeline's shape — config
// load, CLI overrides, API-key resolution — then hands the live config to
// gui.Run, which owns the window, the recording state machine, and the
// global hotkey for the rest of the process.
func runUI(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("flowstate ui", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		configPath string
		hotkey     string
		noHotkey   bool
		device     string
		prompt     string
		output     string
		toggle     bool
		installKey bool
	)
	fs.StringVar(&configPath, "config", "", "Path to config file (overrides $FLOWSTATE_CONFIG and the platform default).")
	fs.StringVar(&hotkey, "hotkey", "f9", "Key used for the record/stop hotkey (e.g. f9, f12, space).")
	fs.BoolVar(&noHotkey, "no-hotkey", false, "Disable the hotkey entirely.")
	fs.BoolVar(&toggle, "toggle", false, "Tell a running flowstate ui to start/stop recording, then exit. Bind this to a desktop shortcut.")
	fs.BoolVar(&installKey, "install-shortcut", false, "Register --hotkey as a GNOME shortcut running 'flowstate ui --toggle' (works on Wayland).")
	fs.StringVar(&device, "device", "", "Override input_device (UID or name).")
	fs.StringVar(&prompt, "prompt", "", "Override active_prompt (key under [prompts]).")
	fs.StringVar(&output, "output", "", "Override output_mode (comma list of clipboard,paste, or \"all\").")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// Client modes: neither needs a display, a config, or an API key.
	if toggle {
		if err := gui.SendToggle(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if installKey {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(stderr, "resolve executable: %v\n", err)
			return 1
		}
		accel := gui.Accelerator(hotkey)
		if err := gui.InstallGNOMEShortcut(self, accel); err != nil {
			fmt.Fprintf(stderr, "install shortcut: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "✓ %s now toggles recording from any app (edit it under Settings → Keyboard → Custom Shortcuts)\n", accel)
		return 0
	}

	// The GUI is a desktop surface: fail fast with a plain-English error
	// instead of letting Fyne/GLFW panic on a missing display.
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		fmt.Fprintln(stderr, "no display found (DISPLAY and WAYLAND_DISPLAY are both unset); `flowstate ui` needs a graphical session")
		return 1
	}

	path := config.ResolvePath(configPath)
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

	// Session overrides. Unlike the record command these are applied
	// straight onto the struct (the GUI has no recordFlags plumbing) and
	// validated below so a typo fails here, not mid-recording.
	if device != "" {
		cfg.InputDevice = device
	}
	if prompt != "" {
		cfg.ActivePrompt = prompt
	}
	if output != "" {
		cfg.OutputMode = output
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(stderr, "config: %v\n", err)
		return 1
	}

	apiKey, err := config.ResolveAPIKey()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if noHotkey {
		hotkey = ""
	}
	// Ctrl+C in the launching terminal must tear the window down the same
	// way closing it does, so a mid-recording exit restores the speakers.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := gui.Run(sigCtx, gui.Options{
		Version:    version,
		Config:     cfg,
		ConfigPath: path,
		APIKey:     apiKey,
		Hotkey:     hotkey,
	}); err != nil {
		fmt.Fprintf(stderr, "ui: %v\n", err)
		return 1
	}
	return 0
}
