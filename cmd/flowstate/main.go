// Package main is the flowstate CLI entry point. It does the bare minimum:
// dispatch on the first positional arg to a subcommand, falling through to
// the default "record" pipeline. Flag parsing happens inside each subcommand
// (see flags.go) using stdlib flag.NewFlagSet — no external CLI framework.
//
// Exit codes:
//
//	0 — success (including "no speech detected" / "no output" — those are
//	    expected outcomes of a recording that produced nothing usable).
//	1 — any user-visible error (config missing, API key missing, transcribe
//	    or cleanup failure, device enumeration empty, etc.).
//	2 — flag parsing error from flag.NewFlagSet's default ContinueOnError +
//	    --help (flag.ErrHelp). Set by the stdlib; we let it bubble.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
)

// version is the build identifier printed by `flowstate version`. The
// 0.0.x-dev string here is the M9 placeholder; release builds will override
// it via -ldflags="-X main.version=..." once the release pipeline lands.
var version = "0.0.1-dev"

// helpText is what `flowstate --help` (and `flowstate -h`) prints. Short by
// design: details live in the man-page-style README, not in --help.
const helpText = `flowstate — record, transcribe, and clean up speech with Groq.

Usage:
  flowstate [flags]                  Record (default).
  flowstate version                  Print version.
  flowstate config init [--force]    Write a default config file.
  flowstate config path              Print the resolved config path.
  flowstate devices                  List input devices.
  flowstate --help                   Show this help.

Record flags:
  --config <path>          Override the config file path.
  --prompt <name>          Override active_prompt.
  --output <list>          Override output_mode (comma list or "all").
  --no-mute                Disable mute_while_recording for this run.
  --device <uid-or-name>   Override input_device.
  --trigger <mode>         "enter" or "push-to-talk".
  --ptt-key <name>         Override ptt_key (push-to-talk only).
  --model <name>           Override cleanup_model.
  --no-color               Disable ANSI colors on stderr (same as NO_COLOR=1
                           or colors = "never" in config).
  --max-time <seconds>     Auto-stop recording after N seconds and process
                           normally (exit 0). 0 = disabled (default).
`

func main() {
	// run() returns an exit code so main() stays a one-liner and tests can
	// drive the same code path with substituted streams/args.
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the testable seam for main. It parses the first positional arg as
// a subcommand and dispatches, falling through to the record pipeline when
// no subcommand matches. The stdin/stdout/stderr arguments are injected so
// tests can drive subcommands against bytes.Buffer.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// `flowstate` (no args) -> default record pipeline.
	if len(args) == 0 {
		return runRecord(context.Background(), args, stdin, stdout, stderr)
	}

	// `--help` / `-h` are honored before subcommand dispatch so they work
	// regardless of position. The record pipeline also handles them via
	// flag.NewFlagSet, but a top-level help should never start recording.
	switch args[0] {
	case "--help", "-h", "help":
		fmt.Fprint(stdout, helpText)
		return 0
	case "version", "--version":
		runVersion(stdout)
		return 0
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "devices":
		return runDevices(stdout, stderr)
	}

	// Anything else (flags like --config=foo or unrecognized positional)
	// falls into the record pipeline, which owns the full flag set.
	return runRecord(context.Background(), args, stdin, stdout, stderr)
}

// runVersion prints the build identifier and Go toolchain version. Kept tiny
// and on stdout so it's easy to capture in CI smoke tests.
func runVersion(stdout io.Writer) {
	fmt.Fprintf(stdout, "flowstate %s (go %s)\n", version, runtime.Version())
}
