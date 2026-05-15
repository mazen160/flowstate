package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/mazin-ahmed/flowstate/internal/cleanup"
	"github.com/mazin-ahmed/flowstate/internal/config"
	"github.com/mazin-ahmed/flowstate/internal/prompts"
	"github.com/mazin-ahmed/flowstate/internal/server"
	"github.com/mazin-ahmed/flowstate/internal/transcribe"
)

// runWeb implements the `flowstate web` subcommand. It parses its three
// flags, resolves the config + API key the same way the record pipeline
// does, prints a non-loopback-no-token warning when applicable, and then
// blocks in server.ListenAndServe until ctx is cancelled (Ctrl+C).
//
// The function is split into smaller pieces only where unit tests need
// independent coverage (isLoopbackHost). The startup banner intentionally
// uses stderr so a future caller piping the binary's output stays clean.
func runWeb(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("flowstate web", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		listenHost string
		listenPort int
		token      string
		configPath string
	)
	fs.StringVar(&listenHost, "web-interface-listen", "127.0.0.1",
		"Interface to bind (default 127.0.0.1).")
	fs.IntVar(&listenPort, "web-port", 8585,
		"TCP port to listen on (default 8585).")
	fs.StringVar(&token, "web-token", "",
		"Optional Bearer auth token. Required only if set; FLOWSTATE_WEB_TOKEN env var is the fallback.")
	fs.StringVar(&configPath, "config", "",
		"Path to config file (overrides $FLOWSTATE_CONFIG).")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	// 1. Resolve and load config. Mirrors record.go's first-run check —
	//    a missing file points the user at `flowstate config init`
	//    rather than bubbling up the raw os.ErrNotExist.
	path := config.ResolvePath(configPath)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "no config found at %s; run `flowstate config init` to create one\n", path)
		return 1
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(stderr, "load config: %v\n", err)
		return 1
	}

	// 2. Surface any soft warnings the config layer collected (unknown
	//    keys, etc.). We don't have a Reporter here — plain stderr is
	//    fine for the web subcommand.
	for _, w := range cfg.Warnings() {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}

	// 3. Resolve the Groq API key BEFORE binding the socket so a user
	//    who hasn't exported one gets a fast error instead of seeing the
	//    server URL print and then failing on the first request.
	apiKey, err := config.ResolveAPIKey()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	// 4. Token resolution: flag wins, env var is the fallback, "" means
	//    auth-disabled. Mirrors the documented precedence on the task.
	if token == "" {
		token = os.Getenv("FLOWSTATE_WEB_TOKEN")
	}

	// 5. Loud warning when binding to a non-loopback interface with no
	//    token. The user owns the decision; we don't refuse to start.
	if !isLoopbackHost(listenHost) && token == "" {
		fmt.Fprintf(stderr,
			"⚠  WARNING: binding %s:%d without --web-token. Any device on the network can use the API without authentication.\n",
			listenHost, listenPort)
	}

	// 6. Construct transcribe + cleanup clients the same way record.go
	//    does so wire-level behavior is identical between CLI and web.
	tx := transcribe.NewClient(transcribe.Options{
		APIKey:   apiKey,
		BaseURL:  cfg.BaseURL,
		Model:    cfg.TranscriptionModel,
		Language: cfg.Language,
	})
	cl := cleanup.NewClient(cleanup.Options{
		APIKey:        apiKey,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.CleanupModel,
		FallbackModel: cfg.CleanupFallbackModel,
	})

	// 7. Resolve the active system prompt and apply the same FreeFlow-
	//    parity augmentations as record.go (output language directive +
	//    high-priority vocabulary block).
	systemPrompt, ok := cfg.Prompts[cfg.ActivePrompt]
	if !ok {
		fmt.Fprintf(stderr, "active_prompt %q has no body under [prompts]\n", cfg.ActivePrompt)
		return 1
	}
	systemPrompt = prompts.ApplyOutputLanguage(systemPrompt, cfg.OutputLanguage)
	systemPrompt = prompts.ApplyVocabulary(systemPrompt, cfg.CustomVocabulary)

	// 8. Build the server. ListenAndServe blocks until ctx is cancelled
	//    and returns nil on a clean shutdown.
	srv := server.New(server.Options{
		ListenHost:     listenHost,
		ListenPort:     listenPort,
		Token:          token,
		Transcribe:     tx,
		Cleanup:        cl,
		SystemPrompt:   systemPrompt,
		ContextSummary: "",
		Version:        version,
		Stderr:         stderr,
	})

	fmt.Fprintf(stderr, "✓ flowstate web listening on http://%s\n", srv.Addr())

	// 9. Wire SIGINT/SIGTERM to ctx so Ctrl+C drives the graceful
	//    shutdown path documented on Server.ListenAndServe.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.ListenAndServe(sigCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// isLoopbackHost returns true if the given host string designates the
// local machine. Both IP literals (127.0.0.1, ::1) and the hostname
// "localhost" qualify. "0.0.0.0" deliberately does NOT — binding to all
// interfaces is exactly the case the non-loopback warning is meant to
// catch.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost"
}
