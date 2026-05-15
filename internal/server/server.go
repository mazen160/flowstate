// Package server implements the `flowstate web` HTTP server: a single-page
// vanilla-JS UI plus a thin JSON API that re-uses the existing transcribe +
// cleanup pipeline from the CLI. No transcripts are persisted on the
// server side — the browser owns history via localStorage.
//
// The package is structured for testability and minimal API surface:
//
//   - server.go    Server, Options, New, ListenAndServe, route wiring.
//   - handlers.go  /api/transcribe, /api/health, /api/info.
//   - auth.go      Bearer-token middleware (no-op when Token is empty).
//   - assets.go    //go:embed of web/* with Content-Type detection.
//
// All status output (startup URL, warnings, shutdown notices) flows through
// Options.Stderr so the caller controls where banners land.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/mazin-ahmed/flowstate/internal/cleanup"
	"github.com/mazin-ahmed/flowstate/internal/transcribe"
)

// shutdownTimeout is how long ListenAndServe waits for in-flight requests
// to drain after ctx is cancelled. Short by design: the only long-running
// request is /api/transcribe, and a stuck Groq call should not block Ctrl+C
// for more than this.
const shutdownTimeout = 5 * time.Second

// maxUploadBytes caps the multipart upload size to match Groq's documented
// 25 MiB transcription limit. Requests above this return 413 before we
// touch the upstream.
const maxUploadBytes = 25 * 1024 * 1024

// Options bundles the configuration a Server needs. Construction-time
// validation is light: the only field that can fail loudly later is
// ListenPort (out-of-range will surface when the listener binds).
type Options struct {
	// ListenHost is the interface to bind. Typically "127.0.0.1"; the
	// caller is responsible for printing a non-loopback warning when
	// appropriate.
	ListenHost string
	// ListenPort is the TCP port to bind. 0 means "let the kernel pick a
	// free port" — useful for tests via httptest-style usage.
	ListenPort int
	// Token, when non-empty, is the required Bearer auth token for all
	// /api/* routes. When empty, the auth middleware is a no-op.
	Token string
	// Transcribe is the Groq Whisper client. Must be non-nil for
	// /api/transcribe to succeed.
	Transcribe *transcribe.Client
	// Cleanup is the Groq chat-completions client. Must be non-nil for
	// /api/transcribe to succeed.
	Cleanup *cleanup.Client
	// SystemPrompt is the active cleanup prompt body (already augmented
	// by ApplyOutputLanguage / ApplyVocabulary by the caller).
	SystemPrompt string
	// ContextSummary is the AppContext string — always "" in v1, kept on
	// the struct so a future revision that ships an OS-side scraper can
	// pipe context through without changing the call site.
	ContextSummary string
	// Version is the build identifier surfaced in /api/health and
	// /api/info responses.
	Version string
	// Stderr is where startup/shutdown banners are written. nil falls
	// back to os.Stderr.
	Stderr io.Writer
}

// Server is the HTTP server constructed from Options. Use New + ListenAndServe.
type Server struct {
	opts    Options
	mux     *http.ServeMux
	handler http.Handler // mux wrapped in any global middleware
}

// New wires the routes (assets + JSON API) into a freshly-allocated
// ServeMux and returns a ready-to-listen Server.
func New(opts Options) *Server {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	s := &Server{opts: opts}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.mux = mux

	// All routes share the same outer handler today, but wrapping the
	// mux in a single chain leaves room to drop in request-logging or
	// gzip middleware without disturbing the route table below.
	s.handler = mux
	return s
}

// registerRoutes wires every URL handled by the server. Asset routes go
// through plain handlers (no auth); API routes are wrapped in the auth
// middleware so a non-empty Token gates them.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Static frontend assets — index.html at "/" and CSS/JS under
	// "/static/". Auth is intentionally NOT applied: the assets do not
	// expose any data, and gating them would break the "enter token in
	// the settings panel" UX (the page itself needs to load first).
	mux.HandleFunc("/", s.handleIndex)
	mux.Handle("/static/", http.StripPrefix("/static/", s.staticHandler()))

	// JSON API. The auth middleware is a no-op when Token is empty.
	mux.Handle("/api/health", s.authMiddleware(http.HandlerFunc(s.handleHealth)))
	mux.Handle("/api/info", s.authMiddleware(http.HandlerFunc(s.handleInfo)))
	mux.Handle("/api/transcribe", s.authMiddleware(http.HandlerFunc(s.handleTranscribe)))
}

// Addr returns the host:port string the server will (or did) bind to.
// Exposed for tests and the CLI startup banner.
func (s *Server) Addr() string {
	return net.JoinHostPort(s.opts.ListenHost, strconv.Itoa(s.opts.ListenPort))
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled
// or the server fails. On cancel, an http.Server.Shutdown with a 5s
// deadline runs to drain in-flight requests; any remaining requests are
// abruptly closed.
//
// The returned error is nil on a clean shutdown and non-nil only for
// unexpected listener failures. Callers should treat http.ErrServerClosed
// as a successful termination.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	hs := &http.Server{
		Addr:    s.Addr(),
		Handler: s.handler,
		// A modest read header timeout protects against slow-loris
		// clients on the loopback interface (the default workload).
		// The body read deadline is owned by handleTranscribe's
		// MaxBytesReader + the Groq client's own timeout.
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", hs.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", hs.Addr, err)
	}

	// Capture the resolved port back onto opts so a caller binding to
	// :0 (random port) can still print the real URL.
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		s.opts.ListenPort = tcpAddr.Port
	}

	// Serve in a goroutine so the main routine can watch ctx.
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- hs.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		// Graceful shutdown. We hand the kernel a deadline so a stuck
		// Groq client can't block Ctrl+C indefinitely.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := hs.Shutdown(shutdownCtx); err != nil {
			// Surface the error; the caller decides whether to log.
			return err
		}
		// Drain the serve goroutine so a late error isn't lost.
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-serveErr:
		// Serve returned before ctx was cancelled — usually a bind or
		// listener error. http.ErrServerClosed should not happen here,
		// but propagate cleanly if it does.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
