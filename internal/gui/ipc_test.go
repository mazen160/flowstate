package gui

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestServeToggle_RoundTrip drives the socket pair the desktop shortcut
// relies on: the window listens, `--toggle` connects and fires the handler.
func TestServeToggle_RoundTrip(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	got := make(chan struct{}, 4)
	stop, err := serveToggle(ctx, func() { got <- struct{}{} })
	if err != nil {
		t.Fatalf("serveToggle: %v", err)
	}
	defer stop()

	if err := SendToggle(); err != nil {
		t.Fatalf("SendToggle: %v", err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("toggle handler never fired")
	}
}

// TestServeToggle_ReplacesStaleSocket pins the crash-recovery path: a
// leftover socket file (no listener behind it) must not block a fresh
// window from listening.
func TestServeToggle_ReplacesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	// A crashed run leaves the file behind with nobody accepting on it.
	if err := os.WriteFile(toggleSocketPath(), nil, 0o600); err != nil {
		t.Fatalf("seed stale socket file: %v", err)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	got := make(chan struct{}, 1)
	stop2, err := serveToggle(ctx2, func() { got <- struct{}{} })
	if err != nil {
		t.Fatalf("serveToggle over stale file: %v", err)
	}
	defer stop2()

	if err := SendToggle(); err != nil {
		t.Fatalf("SendToggle after restart: %v", err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never fired after socket replacement")
	}
}

// TestSendToggle_NoInstance checks the error path the CLI surfaces when no
// window is running.
func TestSendToggle_NoInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if err := SendToggle(); err == nil {
		t.Fatal("expected an error with no listener")
	}
}

// TestAccelerator pins the gohook-name -> GTK accelerator mapping.
func TestAccelerator(t *testing.T) {
	cases := map[string]string{
		"f9":              "F9",
		"F12":             "F12",
		"space":           "space", // GTK spells it lowercase
		"tab":             "Tab",
		"pageup":          "Page_Up",
		"r":               "R",
		"<Control><Alt>r": "<Control><Alt>r",
		"<Super>d":        "<Super>d",
		"":                "F9",
	}
	for in, want := range cases {
		if got := Accelerator(in); got != want {
			t.Errorf("Accelerator(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestQuotedItems covers the GVariant list parser used to preserve the
// user's existing custom shortcuts.
func TestQuotedItems(t *testing.T) {
	got := quotedItems("['/a/x/', '/b/y/']")
	if len(got) != 2 || got[0] != "/a/x/" || got[1] != "/b/y/" {
		t.Fatalf("quotedItems = %v", got)
	}
	if items := quotedItems("@as []"); len(items) != 0 {
		t.Fatalf("empty list parsed as %v", items)
	}
	if list := gvariantList([]string{"/a/", "/b/"}); list != "['/a/', '/b/']" {
		t.Fatalf("gvariantList = %q", list)
	}
}

// TestServeToggle_RefusesSecondInstance pins the fix for a silent takeover:
// a second window must not unlink the running window's socket, or the first
// window's desktop hotkey would stop working with no explanation.
func TestServeToggle_RefusesSecondInstance(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	stop1, err := serveToggle(ctx1, func() {})
	if err != nil {
		t.Fatalf("first serveToggle: %v", err)
	}
	defer stop1()

	if _, err := serveToggle(context.Background(), func() {}); err == nil {
		t.Fatal("second instance should be refused while the first is listening")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("unexpected error: %v", err)
	}

	// The first instance must still be reachable after the refusal.
	got := make(chan struct{}, 1)
	cancel1()
	stop1()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	stop2, err := serveToggle(ctx2, func() { got <- struct{}{} })
	if err != nil {
		t.Fatalf("serveToggle after owner exits: %v", err)
	}
	defer stop2()
	if err := SendToggle(); err != nil {
		t.Fatalf("SendToggle: %v", err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never fired")
	}
}
