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
	"sync"
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

	// readyOnce guards a single close of ready + write of resolvedAddr.
	// ready closes once the listener has bound; WaitReady selects on it
	// to unblock callers that need the resolved address (e.g. tests
	// using ListenPort=0 to pick a random port).
	readyOnce    sync.Once
	ready        chan struct{}
	resolvedAddr string
}

// New wires the routes (assets + JSON API) into a freshly-allocated
// ServeMux and returns a ready-to-listen Server.
func New(opts Options) *Server {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	s := &Server{
		opts:  opts,
		ready: make(chan struct{}),
	}
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.mux = mux

	// All routes share the same outer handler today, but wrapping the
	// mux in a single chain leaves room to drop in request-logging or
	// gzip middleware without disturbing the route table below.
	s.handler = mux
	return s
}

// markReady is called exactly once by ListenAndServe after net.Listen
// succeeds. Stores the resolved address (so a ListenPort=0 caller can
// discover the real port) and closes ready so WaitReady can return.
//
// Wrapped in sync.Once because a second ListenAndServe call on the same
// Server is unsupported — we don't try to support recycling — and the
// once guard turns "called twice by mistake" from a panic into a no-op.
func (s *Server) markReady(addr string) {
	s.readyOnce.Do(func() {
		s.resolvedAddr = addr
		close(s.ready)
	})
}

// WaitReady blocks until ListenAndServe has bound a listener (so the
// resolved address is stable), or until ctx is cancelled. Returns the
// resolved "host:port" string.
//
// Tests should prefer WaitReady over polling: the previous polling
// pattern read a field that ListenAndServe was concurrently writing,
// which the race detector correctly flagged.
func (s *Server) WaitReady(ctx context.Context) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.ready:
		return s.resolvedAddr, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
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

// Addr returns the host:port string the server is bound to. Before the
// listener is up it returns the configured Host:Port; once
// ListenAndServe has bound (and markReady has run), it returns the
// resolved address — important when the caller passed ListenPort=0
// and wants the real port the kernel picked.
//
// The select on s.ready is non-blocking via the default case so this
// can be called from the pre-bind goroutine without deadlocking.
func (s *Server) Addr() string {
	select {
	case <-s.ready:
		// Listener is up — resolvedAddr is stable (set under
		// readyOnce before close) so reading it here is race-free.
		return s.resolvedAddr
	default:
		// Pre-bind: synthesize from the configured Options. This
		// is what http.Server.Addr originally consumed; the value
		// may have ListenPort=0 in which case the real port is
		// only knowable after WaitReady returns.
		return net.JoinHostPort(s.opts.ListenHost, strconv.Itoa(s.opts.ListenPort))
	}
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

	// Signal that the listener is up. markReady stores the resolved
	// address under sync.Once and closes the ready channel so WaitReady
	// (and a non-blocking Addr() probe) can unblock. Note that we do
	// NOT mutate s.opts.ListenPort here — opts is the caller's view of
	// the requested configuration and must stay read-only after New.
	// The race detector flagged the previous in-place write because
	// tests polled opts.ListenPort from another goroutine.
	s.markReady(listener.Addr().String())

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
