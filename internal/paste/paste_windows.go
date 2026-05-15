//go:build windows

package paste

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Paste invokes PowerShell's built-in WScript.Shell automation to send the
// Ctrl+V keystroke to the focused window. WScript.Shell ships with every
// supported Windows install, so we never need a third-party tool.
//
// Failures bubble up the captured stderr from PowerShell — most often
// these are execution-policy blocks, which `-NoProfile` plus the inline
// `-Command` form is designed to avoid.
func Paste() error {
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-Command",
		`(New-Object -ComObject WScript.Shell).SendKeys('^v')`,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("paste failed: %s", msg)
	}
	return nil
}
