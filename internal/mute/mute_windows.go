//go:build windows

package mute

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// doMute sends VK_VOLUME_MUTE (173) via PowerShell's WScript.Shell.
// VK_VOLUME_MUTE is a *toggle*, not an absolute set: pressing it flips
// the current state. The exported Mute()/Unmute() wrappers in mute.go
// guard against repeated calls using the mutedByUs flag, so each call
// from this file actually represents a desired transition.
func doMute() error {
	return sendVolumeMuteKey()
}

// doUnmute on Windows is exactly the same keystroke as doMute — the key
// is a toggle. The wrapper layer guarantees we only send it when a
// transition is required.
func doUnmute() error {
	return sendVolumeMuteKey()
}

// sendVolumeMuteKey is the shared PowerShell invocation for the
// volume-mute virtual key. Failures are logged to stderr and swallowed
// because callers treat mute as best-effort.
func sendVolumeMuteKey() error {
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-Command",
		`(New-Object -ComObject WScript.Shell).SendKeys([char]173)`,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr,
			"mute: powershell SendKeys failed (%s); continuing without mute\n",
			msg,
		)
	}
	return nil
}
