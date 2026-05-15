// Package trigger abstracts the user-driven start/stop signal that bookends
// a single recording session. Flowstate ships two implementations:
//
//   - [EnterTrigger] — the default. Capture is assumed to already be running
//     when [Trigger.Run] is called; the trigger prints a prompt to stdout and
//     blocks until the user presses Enter on stdin (or the context is
//     cancelled). No permissions required.
//   - [PushToTalkTrigger] — a global keyboard hook via robotn/gohook. Run
//     blocks until the configured key goes DOWN (that edge IS the start
//     signal — the caller's onStart callback fires before Run begins waiting
//     for key-up), then returns when the key goes UP.
//
// Known limitation: the push-to-talk implementation imports
// github.com/robotn/gohook, which is cgo and links against system libraries
// (Cocoa on macOS, libX11/libXtst/libxkbcommon on Linux, user32 on Windows).
// On hosts missing those development headers the entire package fails to
// compile. The Enter trigger does not depend on gohook at runtime but the
// package builds as a unit; there is no `enter`-only minimal build.
//
// Permission notes for push-to-talk:
//
//   - macOS: requires Accessibility permission on the terminal binary.
//   - Linux: works on X11 out of the box; Wayland support depends on the
//     compositor.
//   - Windows: works out of the box.
//
// Construction of [PushToTalkTrigger] does NOT detect a missing permission
// directly — gohook silently produces no events when denied. Callers that
// observe Run blocking indefinitely should advise the user to switch to
// trigger="enter" or grant the permission and retry.
package trigger

import (
	"context"
	"fmt"
	"io"

	hook "github.com/robotn/gohook"
)

// Trigger is the user-driven start/stop signal for a recording session. See
// the package doc for the per-implementation semantics of "start".
type Trigger interface {
	// Run blocks until a stop signal is detected, then returns. Context
	// cancellation causes Run to return ctx.Err(). The onStart callback
	// fires when the trigger considers the session to have "started":
	//
	//   - EnterTrigger: onStart fires immediately on entry, before
	//     blocking on stdin. Capture is assumed already running.
	//   - PushToTalkTrigger: onStart fires after the configured key goes
	//     DOWN, so the caller can start capture on the key-down edge.
	//     Run then blocks until the same key goes UP.
	//
	// If onStart returns a non-nil error, Run returns that error without
	// waiting for the stop signal. onStart may be nil, in which case it is
	// skipped.
	Run(ctx context.Context, onStart func() error) error

	// Close releases any held resources. For [EnterTrigger] this is a
	// no-op; for [PushToTalkTrigger] this unregisters the global hook
	// via hook.End(). Close is idempotent.
	Close() error
}

// resolveKeyName maps a user-friendly key name (as written in the config's
// ptt_key field) to the string gohook expects in hook.Register's cmds slice.
// gohook's underlying keycode table is case-sensitive and uses lowercase
// single-letter names for alphabetic keys, "space"/"f12"/etc. for special
// keys. We trim whitespace and lowercase the input so "Space", "SPACE", and
// " space " all resolve to "space".
//
// Names not present in gohook's table (e.g. "fn", which lacks a portable
// keycode) pass through unchanged. hook.Register will silently produce no
// events for an unknown name; we surface that as a construction-time hint by
// checking the table in [NewPushToTalkTrigger].
//
// resolveKeyName is exposed for testing (the table-driven test in
// trigger_test.go pins down the mapping).
func resolveKeyName(name string) string {
	trimmed := trimSpace(name)
	lower := toLower(trimmed)
	return lower
}

// isKnownKey reports whether resolveKeyName(name) corresponds to an entry in
// gohook's keycode table. Used during construction to give a friendlier
// error than "the hook silently never fires".
func isKnownKey(name string) bool {
	resolved := resolveKeyName(name)
	if resolved == "" {
		return false
	}
	_, ok := hook.Keycode[resolved]
	return ok
}

// trimSpace strips leading and trailing ASCII whitespace from s. We avoid
// importing strings here because the rest of this package is small and a
// localized helper keeps the dependency surface clear.
func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

// toLower returns s with ASCII A-Z lowercased. Non-ASCII runes pass through
// unchanged, which is fine: key names in gohook's table are all ASCII.
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// Compile-time interface satisfaction checks. These catch a refactor that
// accidentally breaks the interface contract.
var (
	_ Trigger = (*EnterTrigger)(nil)
	_ Trigger = (*PushToTalkTrigger)(nil)
)

// NewEnterTrigger constructs a Trigger that reads a single newline-terminated
// line from stdin to signal stop. The msg is printed to stdout when Run
// starts (typically: "Recording… press Enter to stop\n").
func NewEnterTrigger(stdin io.Reader, stdout io.Writer, msg string) Trigger {
	return &EnterTrigger{
		stdin:  stdin,
		stdout: stdout,
		msg:    msg,
	}
}

// NewPushToTalkTrigger registers a global keyboard hook for the given
// user-facing key name (e.g. "space", "f12", "a"). The returned trigger's
// Run blocks until the key goes DOWN, fires onStart, then blocks until the
// key goes UP.
//
// If the key name does not map to a gohook keycode, NewPushToTalkTrigger
// returns an error pointing the user back at the trigger=enter fallback —
// otherwise the hook would silently never fire and the user would see Run
// block forever. Other failure modes (Accessibility denied on macOS,
// unsupported Wayland compositor on Linux) are NOT detectable from inside
// gohook's API; on those hosts Run will simply never observe a key-down.
// Callers should document the fallback path.
func NewPushToTalkTrigger(keyName string) (Trigger, error) {
	if !isKnownKey(keyName) {
		return nil, fmt.Errorf("push-to-talk key %q is not a recognized key name; push-to-talk requires Accessibility permission on macOS or a supported X11/Wayland compositor on Linux; failing fallback: set trigger=\"enter\" in your config", keyName)
	}
	return &PushToTalkTrigger{
		keyName: resolveKeyName(keyName),
	}, nil
}
