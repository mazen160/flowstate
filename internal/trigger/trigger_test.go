package trigger

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnterTrigger_StopsOnNewline verifies the happy path: a newline on
// stdin causes Run to return nil after printing the prompt to stdout.
func TestEnterTrigger_StopsOnNewline(t *testing.T) {
	stdin := strings.NewReader("\n")
	stdout := &bytes.Buffer{}
	tr := NewEnterTrigger(stdin, stdout, "Recording… press Enter to stop\n")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err := tr.Run(ctx, nil); err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
	if got := stdout.String(); got != "Recording… press Enter to stop\n" {
		t.Fatalf("stdout = %q; want the prompt msg", got)
	}
}

// TestEnterTrigger_OnStartFires verifies the onStart callback is invoked
// before Run blocks for the newline. We assert two things: the counter is
// non-zero by the time Run returns, and Run returns successfully.
func TestEnterTrigger_OnStartFires(t *testing.T) {
	stdin := strings.NewReader("\n")
	stdout := io.Discard
	tr := NewEnterTrigger(stdin, stdout, "")

	var started atomic.Int32
	cb := func() error {
		started.Add(1)
		return nil
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err := tr.Run(ctx, cb); err != nil {
		t.Fatalf("Run returned %v; want nil", err)
	}
	if got := started.Load(); got != 1 {
		t.Fatalf("onStart fired %d times; want 1", got)
	}
}

// TestEnterTrigger_OnStartError verifies that a non-nil error from onStart
// is propagated by Run without waiting for a stop signal. We use a reader
// that would block forever (empty pipe) to prove Run didn't wait on stdin.
func TestEnterTrigger_OnStartError(t *testing.T) {
	pr, pw := io.Pipe()
	// Pipe will block reads until we write or close — we do neither.
	defer func() { _ = pw.Close() }()
	defer func() { _ = pr.Close() }()

	tr := NewEnterTrigger(pr, io.Discard, "")
	wantErr := errors.New("capture init failed")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	gotErr := tr.Run(ctx, func() error { return wantErr })
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("Run returned %v; want %v", gotErr, wantErr)
	}
}

// TestEnterTrigger_ContextCancellation verifies that an empty stdin (which
// would otherwise block forever in ReadString) does not stall Run when the
// context is cancelled. We use a pipe so reads truly block, then cancel.
func TestEnterTrigger_ContextCancellation(t *testing.T) {
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	defer func() { _ = pr.Close() }()

	tr := NewEnterTrigger(pr, io.Discard, "")

	ctx, cancel := context.WithCancel(t.Context())
	errCh := make(chan error, 1)
	go func() {
		errCh <- tr.Run(ctx, nil)
	}()

	// Give Run a moment to enter the select, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s after ctx cancel")
	}
}

// TestEnterTrigger_StopsOnEOF verifies that stdin closing (EOF without a
// newline) is treated as a stop signal. This matters when input is piped
// in: a script can emit nothing and just close, and we should stop rather
// than hang.
func TestEnterTrigger_StopsOnEOF(t *testing.T) {
	stdin := strings.NewReader("") // immediate EOF
	tr := NewEnterTrigger(stdin, io.Discard, "")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err := tr.Run(ctx, nil); err != nil {
		t.Fatalf("Run returned %v; want nil on EOF", err)
	}
}

// TestEnterTrigger_Close_NoOp pins the documented behavior: Close on an
// EnterTrigger does nothing and returns nil. Tests guard against a future
// refactor that quietly adds shutdown logic without callers noticing.
func TestEnterTrigger_Close_NoOp(t *testing.T) {
	tr := NewEnterTrigger(strings.NewReader(""), io.Discard, "")
	if err := tr.Close(); err != nil {
		t.Fatalf("Close returned %v; want nil", err)
	}
	// Second call is also a no-op.
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close returned %v; want nil", err)
	}
}

// TestTriggerInterface_Compliance is a compile-time and runtime check that
// both concrete types satisfy the Trigger interface. The compile-time check
// lives as `var _ Trigger = ...` in trigger.go; this test ensures the
// interface is usable from outside via the constructors.
func TestTriggerInterface_Compliance(t *testing.T) {
	var enter Trigger = NewEnterTrigger(strings.NewReader(""), io.Discard, "")
	if enter == nil {
		t.Fatalf("NewEnterTrigger returned nil")
	}
	// We don't construct a PushToTalkTrigger here because doing so would
	// not register a hook (that happens in Run), but it would still be a
	// concrete *PushToTalkTrigger, which is exercised in ptt_test.go.
}
