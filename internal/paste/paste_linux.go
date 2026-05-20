//go:build linux

package paste

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Paste sends Ctrl+V by trying each known autotype tool in order until one
// either succeeds or fails with an error the user has to fix manually.
//
// Ordering is keyed off $WAYLAND_DISPLAY: on Wayland we prefer the native
// tools (wtype → ydotool) and treat xdotool as an XWayland-only last
// resort; on X11 we go straight to xdotool with ydotool as a backup.
//
// Two distinct failure modes matter on Wayland:
//
//   - Tool not installed. Handled by skipping the candidate at selection
//     time (pasteCandidates filters via exec.LookPath).
//   - Tool installed but rejected at runtime. The canonical case is
//     `wtype` on GNOME or KDE, where the compositor doesn't implement
//     `wlr-virtual-keyboard-unstable-v1`, so the binary exits non-zero
//     with "Compositor does not support the virtual keyboard protocol".
//     isFallbackTrigger detects these patterns; we then move on to the
//     next candidate instead of surfacing an error the user can't act on.
//
// When every candidate fails we surface the last error verbatim, since
// that's the one most likely to contain the actionable detail (a missing
// daemon socket, a permission problem, etc.).
func Paste() error {
	env := map[string]string{
		"WAYLAND_DISPLAY": os.Getenv("WAYLAND_DISPLAY"),
	}
	candidates := pasteCandidates(env, exec.LookPath)
	if len(candidates) == 0 {
		return fmt.Errorf(
			"paste failed: none of wtype, ydotool, or xdotool are installed; install one with your package manager",
		)
	}

	var lastErr error
	for i, c := range candidates {
		err := runPasteCommand(c.name, c.args)
		if err == nil {
			return nil
		}
		lastErr = err
		// Only fall through on errors that mean "this tool is the wrong
		// match for this environment" — not on errors that mean "this
		// tool is broken in a way the user needs to fix" (e.g. permission
		// denied on /dev/uinput for ydotool).
		more := i < len(candidates)-1
		if !more || !isFallbackTrigger(c.name, err.Error()) {
			return err
		}
	}
	return lastErr
}

// pasteCandidate is a (binary name, argv) pair the runner can invoke.
type pasteCandidate struct {
	name string
	args []string
}

// pasteCommands holds the literal argv for each supported tool. ydotool
// takes raw evdev keycodes — KEY_LEFTCTRL=29 and KEY_V=47 from
// linux/input-event-codes.h — so the sequence is press-ctrl, press-v,
// release-v, release-ctrl. Kept as a package var (not a const map) so
// tests can read it without duplicating the strings.
var pasteCommands = map[string][]string{
	"wtype":   {"-M", "ctrl", "-k", "v", "-m", "ctrl"},
	"ydotool": {"key", "29:1", "47:1", "47:0", "29:0"},
	"xdotool": {"key", "ctrl+v"},
}

// pasteCandidates returns the ordered list of installed paste tools to
// try for the current session, most-preferred first. Tools missing from
// PATH are silently skipped so the caller only ever iterates over things
// that can plausibly run.
func pasteCandidates(
	env map[string]string,
	lookPath func(string) (string, error),
) []pasteCandidate {
	var preference []string
	if env["WAYLAND_DISPLAY"] != "" {
		// Native Wayland tools first; xdotool only covers XWayland
		// clients so it goes last as a partial fallback.
		preference = []string{"wtype", "ydotool", "xdotool"}
	} else {
		preference = []string{"xdotool", "ydotool", "wtype"}
	}

	out := make([]pasteCandidate, 0, len(preference))
	for _, name := range preference {
		if _, err := lookPath(name); err != nil {
			continue
		}
		args, ok := pasteCommands[name]
		if !ok {
			continue
		}
		out = append(out, pasteCandidate{name: name, args: args})
	}
	return out
}

// isFallbackTrigger reports whether a non-zero exit from `name` indicates
// the tool is the wrong match for the current environment rather than a
// genuine user-actionable failure. Substring matching is deliberately
// narrow so we don't swallow real problems.
func isFallbackTrigger(name, stderr string) bool {
	switch name {
	case "wtype":
		// GNOME/KDE refuse wlr-virtual-keyboard-unstable-v1; the message
		// is stable across wtype releases.
		return strings.Contains(stderr, "Compositor does not support the virtual keyboard protocol")
	case "ydotool":
		// ydotoold isn't running or the socket isn't reachable. Both
		// strings come from ydotool's own error paths.
		return strings.Contains(stderr, "failed to connect socket") ||
			strings.Contains(stderr, "No such file or directory")
	case "xdotool":
		// Running on Wayland with no XWayland: xdotool can't reach an
		// X display. Worth falling back to anything still untried.
		return strings.Contains(stderr, "Can't open display") ||
			strings.Contains(stderr, "BadValue") // some Wayland XWayland setups
	}
	return false
}

// runPasteCommand executes a single candidate and wraps its stderr into a
// friendly error if it fails. Split out so the fallback loop in Paste()
// stays focused on the selection logic.
func runPasteCommand(name string, args []string) error {
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
