//go:build darwin

package mute

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// doMute shells out to osascript to set the output mute property on the
// default audio device. Failures are logged to stderr and swallowed —
// recording must not abort just because we can't mute (e.g. when
// Accessibility/Automation permission is denied).
func doMute() error {
	return runOsascript("set volume output muted true")
}

// doUnmute is the inverse of doMute. Same best-effort semantics.
func doUnmute() error {
	return runOsascript("set volume output muted false")
}

// runOsascript executes a single -e expression with osascript and logs
// any failure to stderr without returning an error. We return nil in
// every path because callers treat mute as best-effort.
func runOsascript(expr string) error {
	cmd := exec.Command("osascript", "-e", expr)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr,
			"mute: osascript failed (%s); continuing without mute\n",
			msg,
		)
	}
	return nil
}
