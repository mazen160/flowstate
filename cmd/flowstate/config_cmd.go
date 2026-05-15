package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mazen160/flowstate/internal/config"
)

// runConfig dispatches `flowstate config <sub>` to its handler. Returns
// the process exit code. Unknown sub-commands print to stderr and return 2
// (matching flag-error convention).
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "config: missing subcommand (init|path)")
		return 2
	}
	switch args[0] {
	case "init":
		return runConfigInit(args[1:], stderr)
	case "path":
		return runConfigPath(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "config: unknown subcommand %q (want init|path)\n", args[0])
		return 2
	}
}

// runConfigInit handles `flowstate config init [--force] [--config <path>]`.
// It resolves the target path via the same precedence used by the record
// pipeline (flag > $FLOWSTATE_CONFIG > platform default) and writes the
// canonical default config. The wrote-to-X line goes to stderr so callers
// scripting around the command see clean stdout.
func runConfigInit(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("flowstate config init", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		force      bool
		configPath string
	)
	fs.BoolVar(&force, "force", false, "Overwrite an existing config file.")
	fs.StringVar(&configPath, "config", "", "Path to write (overrides $FLOWSTATE_CONFIG and the platform default).")

	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp prints usage via fs's output; everything else is
		// already on stderr. Use exit 2 to mirror stdlib flag convention.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	path := config.ResolvePath(configPath)
	if err := config.Init(path, force); err != nil {
		fmt.Fprintf(stderr, "config init: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "wrote config to %s\n", path)
	return 0
}

// runConfigPath handles `flowstate config path [--config <path>]`. It prints
// the resolved path to stdout (no extra formatting) so shell scripts can
// embed the value: `cat "$(flowstate config path)"`.
func runConfigPath(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("flowstate config path", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var configPath string
	fs.StringVar(&configPath, "config", "", "Path to print (overrides $FLOWSTATE_CONFIG and the platform default).")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	fmt.Fprintln(stdout, config.ResolvePath(configPath))
	return 0
}

// resolveConfigPathFromEnv is a thin wrapper around config.ResolvePath that
// also consults $FLOWSTATE_CONFIG, matching the spec's precedence. It exists
// so the record pipeline and `config path` share one implementation. Direct
// callers can pass the empty string to mean "no --config flag was set."
//
// Note: config.ResolvePath already consults FLOWSTATE_CONFIG, so this
// wrapper is currently a one-liner. It's kept named to make the call site
// in record.go read clearly.
func resolveConfigPathFromEnv(explicit string) string {
	// Belt-and-braces: if FLOWSTATE_CONFIG is set to an empty string the
	// stdlib os.Getenv returns "" and config.ResolvePath falls through to
	// DefaultPath. That matches the spec ("explicit > env > default") so
	// we just delegate.
	return config.ResolvePath(explicit)
}

// envConfigPath returns the current value of FLOWSTATE_CONFIG. Pulled out so
// tests can swap it via os.Setenv without touching config internals.
func envConfigPath() string { return os.Getenv(config.EnvVar) }
