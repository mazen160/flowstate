package main

import (
	"fmt"
	"io"

	"github.com/mazin-ahmed/flowstate/internal/audio"
)

// runDevices implements `flowstate devices`. It enumerates input devices via
// malgo and prints `<UID>\t<Name>` lines to stdout, one per device. The
// tab-separated format is easy to feed into `cut -f2` or similar.
//
// Headless or device-less hosts produce a friendly "no input devices found"
// on stderr and exit 1 — non-zero so CI smoke tests treat it as a real
// failure when audio is expected to be present, while still letting
// developers run `--help` and `config path` on the same host without
// needing a microphone.
func runDevices(stdout, stderr io.Writer) int {
	devs, err := audio.ListInputDevices()
	if err != nil {
		fmt.Fprintf(stderr, "devices: %v\n", err)
		return 1
	}
	if len(devs) == 0 {
		fmt.Fprintln(stderr, "no input devices found")
		return 1
	}
	for _, d := range devs {
		fmt.Fprintf(stdout, "%s\t%s\n", d.UID, d.Name)
	}
	return 0
}
