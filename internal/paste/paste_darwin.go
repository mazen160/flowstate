//go:build darwin

package paste

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Paste shells out to osascript and asks System Events to deliver Cmd+V.
//
// This requires the controlling terminal (or the binary itself) to have
// been granted Accessibility permission in System Settings → Privacy &
// Security → Accessibility. If that permission is missing osascript
// returns a non-zero exit; we surface its stderr verbatim and add a hint
// about Accessibility, which is the cause >95% of the time.
func Paste() error {
	cmd := exec.Command(
		"osascript",
		"-e", `tell application "System Events" to keystroke "v" using command down`,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf(
			"paste failed: %s "+
				"(grant Accessibility permission to your terminal in "+
				"System Settings → Privacy & Security → Accessibility, "+
				"then retry)",
			msg,
		)
	}
	return nil
}
