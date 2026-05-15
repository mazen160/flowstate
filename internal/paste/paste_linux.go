//go:build linux

package paste

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Paste sends Ctrl+V via wtype (Wayland) or xdotool (X11). We prefer the
// tool that matches the active display server, then fall back to the
// other if the preferred one isn't on PATH. If neither is installed we
// return a friendly error naming both options so the user knows what to
// install.
func Paste() error {
	env := map[string]string{
		"WAYLAND_DISPLAY": os.Getenv("WAYLAND_DISPLAY"),
	}
	name, args, err := selectPasteCommand(env, exec.LookPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("paste failed: %s exited non-zero: %s", name, msg)
	}
	return nil
}

// pasteCommands holds the literal argv for each supported tool. Kept as a
// package var (not a const map) only so tests can read it without
// duplicating the strings.
var pasteCommands = map[string][]string{
	"wtype":   {"-M", "ctrl", "-k", "v", "-m", "ctrl"},
	"xdotool": {"key", "ctrl+v"},
}

// selectPasteCommand decides which paste binary to invoke. It is exposed
// (lowercase, but used by paste_test.go in this package) so tests can
// inject a fake environment and PATH-lookup function without needing to
// mutate process state.
//
// Selection rules:
//  1. If $WAYLAND_DISPLAY is non-empty, prefer wtype. Fall back to xdotool
//     if wtype is missing.
//  2. Otherwise, prefer xdotool. Fall back to wtype if xdotool is missing.
//  3. If neither is on PATH, return an error pointing to both options.
func selectPasteCommand(
	env map[string]string,
	lookPath func(string) (string, error),
) (name string, args []string, err error) {
	var primary, secondary string
	if env["WAYLAND_DISPLAY"] != "" {
		primary, secondary = "wtype", "xdotool"
	} else {
		primary, secondary = "xdotool", "wtype"
	}

	if _, err := lookPath(primary); err == nil {
		return primary, pasteCommands[primary], nil
	}
	if _, err := lookPath(secondary); err == nil {
		return secondary, pasteCommands[secondary], nil
	}
	return "", nil, fmt.Errorf(
		"paste failed: neither wtype nor xdotool is installed; install one with your package manager",
	)
}
