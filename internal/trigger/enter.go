package trigger

import (
	"bufio"
	"context"
	"fmt"
	"io"
)

// EnterTrigger blocks on a single newline-terminated read from stdin. It is
// the default trigger because it has no permission requirements and works
// over any TTY, including ssh sessions and dev containers.
//
// EnterTrigger is single-use: a second Run call after the first returns will
// also read from stdin, but the reader is owned by the caller, so behavior
// depends on whether the caller passed a fresh reader. The CLI constructs a
// new trigger per recording session, so the lifecycle question rarely
// surfaces in practice.
type EnterTrigger struct {
	stdin  io.Reader
	stdout io.Writer
	msg    string
}

// Run prints the configured msg to stdout, fires onStart, then blocks until
// either (a) a newline appears on stdin or (b) the context is cancelled.
// The two cases produce nil and ctx.Err() respectively.
//
// Implementation detail: bufio.Reader.ReadString is not cancellable, so we
// run it on a dedicated goroutine and race its result against ctx.Done().
// On cancellation the goroutine remains parked inside the underlying Read
// call until stdin emits a byte or is closed; we accept that leak because
// the typical caller is the top-level CLI process and the goroutine dies
// with the process. Avoiding the leak entirely would require interrupting
// the read (e.g. closing os.Stdin), which is outside this package's scope.
func (e *EnterTrigger) Run(ctx context.Context, onStart func() error) error {
	if e.msg != "" {
		// Best-effort: a failed write to stdout shouldn't abort the
		// recording session. The user just won't see the prompt.
		_, _ = fmt.Fprint(e.stdout, e.msg)
	}

	if onStart != nil {
		if err := onStart(); err != nil {
			return err
		}
	}

	// Reading is delegated to a goroutine so we can race against context
	// cancellation. The channel is buffered so the goroutine can exit even
	// if we've already returned by the time it finishes.
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(e.stdin)
		_, err := reader.ReadString('\n')
		// io.EOF is treated as a stop signal too: piping a script in
		// closes stdin once the line is delivered, and we shouldn't
		// hang waiting for a newline that will never arrive.
		if err == io.EOF {
			err = nil
		}
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close is a no-op for EnterTrigger; the stdin reader is owned by the
// caller and not closed here.
func (e *EnterTrigger) Close() error {
	return nil
}
