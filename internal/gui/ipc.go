package gui

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// The global hotkey needs a path back into the already-running window.
// gohook cannot provide it on Wayland, where the X11 hook only observes
// events while an XWayland window holds focus. Instead the window listens
// on a per-user Unix socket and the desktop's own shortcut system runs
// `flowstate ui --toggle`, which is a one-shot client for that socket.

// maxUnixSocketPath leaves headroom under sun_path, which is 104 bytes on
// macOS and 108 on Linux (including the terminating NUL). A deep home or
// temp directory overflows it and bind fails with "invalid argument".
const maxUnixSocketPath = 100

// toggleSocketPath is the per-user socket shared by the window and the
// `--toggle` client. XDG_RUNTIME_DIR is right when set (per-session, 0700,
// wiped at logout); the cache dir is the fallback on systems without it.
func toggleSocketPath() string {
	var candidates []string
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		candidates = append(candidates, filepath.Join(dir, "flowstate-ui.sock"))
	}
	if dir, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(dir, "flowstate-ui.sock"))
	}
	for _, p := range candidates {
		if len(p) <= maxUnixSocketPath {
			return p
		}
	}
	// Every candidate is too long for sun_path. Derive a short name from
	// them so separate sessions (and tests) still get separate sockets
	// instead of all colliding on one shared path.
	sum := sha256.Sum256([]byte(strings.Join(candidates, "|")))
	return filepath.Join(os.TempDir(), fmt.Sprintf("flowstate-ui-%x.sock", sum[:8]))
}

// serveToggle listens for toggle requests and calls onToggle (which must
// marshal to the UI thread itself). A leftover socket from a crashed run is
// replaced; a socket with a live peer is not, because unlinking it would
// silently steal the running window's hotkey. The returned func stops the
// listener and removes the socket.
func serveToggle(ctx context.Context, onToggle func()) (func(), error) {
	path := toggleSocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create toggle socket dir: %w", err)
	}
	// A live peer means another window already owns the hotkey. Refuse
	// rather than unlink its socket out from under it.
	if c, err := net.Dial("unix", path); err == nil {
		_ = c.Close()
		return nil, fmt.Errorf("another `flowstate ui` is already running (socket %s)", path)
	}
	// Dead socket from a previous run: bind() would fail with "address
	// already in use", so clear it.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on toggle socket: %w", err)
	}
	// Only this user should be able to drive the window.
	_ = os.Chmod(path, 0o600)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed on shutdown
			}
			go func(c net.Conn) {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				if strings.TrimSpace(line) == "toggle" {
					onToggle()
				}
			}(conn)
		}
	}()

	return func() {
		_ = ln.Close()
		_ = os.Remove(path)
	}, nil
}

// SendToggle asks the running window to start or stop recording. Used by
// `flowstate ui --toggle`, which is what a desktop shortcut invokes.
func SendToggle() error {
	conn, err := net.Dial("unix", toggleSocketPath())
	if err != nil {
		return fmt.Errorf("no running `flowstate ui` found (%s); start one first", toggleSocketPath())
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("toggle\n")); err != nil {
		return fmt.Errorf("send toggle: %w", err)
	}
	return nil
}
