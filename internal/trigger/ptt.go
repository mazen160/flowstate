package trigger

import (
	"context"
	"sync"

	hook "github.com/robotn/gohook"
)

// PushToTalkTrigger implements a hold-key-to-record gesture using
// robotn/gohook's global keyboard listener. The configured key serves as
// both the start and stop signal: key-down begins the session (onStart
// fires), key-up ends it (Run returns).
//
// The gohook library exposes a single process-wide event stream. We do not
// attempt to multiplex multiple PushToTalkTriggers — a second Run call
// while one is active will race for events. The Flowstate CLI constructs
// one trigger per recording session and Closes it before constructing
// another, so this is not a problem in practice but is worth noting for
// reusers of the package.
//
// Lifecycle:
//
//   - NewPushToTalkTrigger validates the key name and returns a trigger.
//     No global resources are claimed yet — that happens in Run, so a
//     constructed-but-never-Run trigger costs nothing.
//   - Run calls hook.Start (begins polling the OS) and hook.Register for
//     KeyDown and KeyUp on the configured key. It blocks until both edges
//     have been observed (or ctx is cancelled), then drains hook.End().
//   - Close also calls hook.End — safe to invoke twice because gohook's
//     End is idempotent in our usage (we guard with our own closed flag).
type PushToTalkTrigger struct {
	keyName string

	mu      sync.Mutex
	started bool // true once Run has invoked hook.Start
	closed  bool // true once hook.End has been called
}

// Run registers KeyDown/KeyUp callbacks for the configured key, starts the
// gohook poll loop, and blocks. Sequence:
//
//  1. Register KeyDown → signals a channel that the press has happened.
//  2. Register KeyUp   → signals a different channel for release.
//  3. hook.Start kicks off the poll goroutine.
//  4. Select on (down channel, ctx.Done). On down, fire onStart.
//  5. Select on (up channel, ctx.Done). On up, return nil.
//
// Any path returns through a deferred hook.End so the global poller is
// released even on context cancellation.
func (p *PushToTalkTrigger) Run(ctx context.Context, onStart func() error) error {
	// downCh and upCh are buffered length 1 — if multiple events arrive
	// before we read them, we only care about the first. The hook
	// callbacks are invoked from gohook's internal goroutine, so a
	// non-blocking send keeps them from stalling the poller.
	downCh := make(chan struct{}, 1)
	upCh := make(chan struct{}, 1)

	hook.Register(hook.KeyDown, []string{p.keyName}, func(hook.Event) {
		select {
		case downCh <- struct{}{}:
		default:
		}
	})
	hook.Register(hook.KeyUp, []string{p.keyName}, func(hook.Event) {
		select {
		case upCh <- struct{}{}:
		default:
		}
	})

	evChan := hook.Start()
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	go hook.Process(evChan)

	// Ensure the global hook is released no matter how we exit. We mark
	// closed so an explicit Close() after Run becomes a no-op.
	defer func() {
		p.mu.Lock()
		if !p.closed {
			hook.End()
			p.closed = true
		}
		p.mu.Unlock()
	}()

	// Phase 1: wait for key-down (or cancellation).
	select {
	case <-downCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	if onStart != nil {
		if err := onStart(); err != nil {
			return err
		}
	}

	// Phase 2: wait for key-up (or cancellation).
	select {
	case <-upCh:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

// Close releases the global keyboard hook. Safe to call multiple times and
// safe to call when Run was never invoked — the underlying hook.End is
// only called if Run actually started the global poller, otherwise the C
// library aborts on the unbalanced teardown.
func (p *PushToTalkTrigger) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.started {
		p.closed = true
		return nil
	}
	hook.End()
	p.closed = true
	return nil
}
