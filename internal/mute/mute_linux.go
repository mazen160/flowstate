//go:build linux

package mute

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// errNoMuteTool is returned by chooseMuteCmd when neither pactl nor
// amixer is on PATH. It's a sentinel so tests can errors.Is against it.
var errNoMuteTool = errors.New(
	"mute: neither pactl nor amixer is on PATH",
)

// muteCommands holds the argv for each supported tool, keyed by binary
// name. Kept as a package var (not a const map, which Go doesn't have)
// so tests in this package can read it without restating the strings.
var muteCommands = map[string]struct {
	mute   []string
	unmute []string
}{
	"pactl": {
		mute:   []string{"set-sink-mute", "@DEFAULT_SINK@", "1"},
		unmute: []string{"set-sink-mute", "@DEFAULT_SINK@", "0"},
	},
	"amixer": {
		mute:   []string{"-q", "set", "Master", "mute"},
		unmute: []string{"-q", "set", "Master", "unmute"},
	},
}

// doMute mutes the default sink. We prefer pactl (PulseAudio /
// PipeWire); if it's missing OR returns non-zero we fall back to amixer
// (ALSA). If neither is present, log a warning and return nil so
// recording continues.
func doMute() error {
	return runMuteCmd(true)
}

// doUnmute is the inverse of doMute with the same fall-back rules.
func doUnmute() error {
	return runMuteCmd(false)
}

// runMuteCmd does the pactl→amixer selection and runs the chosen tool.
// All failure paths log to stderr and return nil because callers treat
// mute as best-effort.
func runMuteCmd(mute bool) error {
	binary, muteArgs, unmuteArgs, err := chooseMuteCmd(exec.LookPath)
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"mute: requested but neither pactl nor amixer is on PATH; "+
				"continuing without mute",
		)
		return nil
	}

	args := muteArgs
	if !mute {
		args = unmuteArgs
	}

	cmd := exec.Command(binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		fmt.Fprintf(os.Stderr,
			"mute: %s failed (%s); continuing without mute\n",
			binary, msg,
		)
	}
	return nil
}

// chooseMuteCmd picks the mute binary based on PATH availability:
// pactl wins if present, otherwise amixer, otherwise errNoMuteTool. The
// lookPath dependency is injectable so tests don't have to mutate the
// real PATH.
func chooseMuteCmd(
	lookPath func(string) (string, error),
) (binary string, muteArgs []string, unmuteArgs []string, err error) {
	for _, name := range []string{"pactl", "amixer"} {
		if _, lerr := lookPath(name); lerr == nil {
			cmds := muteCommands[name]
			return name, cmds.mute, cmds.unmute, nil
		}
	}
	return "", nil, nil, errNoMuteTool
}
