//go:build linux

package clipboard

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
)

// Write copies text to the system clipboard so it survives flowstate exiting.
//
// It tries each installed clipboard utility in session-appropriate order
// until one succeeds. Unlike an in-process X11 clipboard owner, these tools
// fork a background daemon that retains the selection after the foreground
// command returns — so the copied text is still there when the user pastes,
// long after flowstate has exited.
//
// Ordering is keyed off $WAYLAND_DISPLAY: on Wayland we prefer the native
// wl-copy and treat xclip/xsel as XWayland fallbacks; on X11 we go straight
// to xclip/xsel with wl-copy as a last resort. A tool that isn't installed is
// skipped at selection time; a tool that runs but fails (e.g. xclip with no
// reachable X display) drops through to the next candidate, and the last
// error is surfaced only if every candidate fails.
func Write(text []byte) error {
	cands := writeCandidates(os.Getenv, exec.LookPath)
	if len(cands) == 0 {
		return fmt.Errorf(
			"clipboard write failed: none of wl-copy, xclip, or xsel are installed; " +
				"install one with your package manager (e.g. `wl-clipboard` on Wayland, `xclip` on X11)",
		)
	}

	var lastErr error
	for _, c := range cands {
		if err := runClipboardWrite(c.name, c.args, text); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

// Read returns the current clipboard text, or nil if the clipboard is empty.
// Like [Write] it tries each installed reader in session-appropriate order;
// an empty clipboard is reported as (nil, nil), not an error.
func Read() ([]byte, error) {
	cands := readCandidates(os.Getenv, exec.LookPath)
	if len(cands) == 0 {
		return nil, fmt.Errorf(
			"clipboard read failed: none of wl-paste, xclip, or xsel are installed; " +
				"install one with your package manager (e.g. `wl-clipboard` on Wayland, `xclip` on X11)",
		)
	}

	var lastErr error
	for _, c := range cands {
		out, err := runClipboardRead(c.name, c.args)
		if err != nil {
			lastErr = err
			continue
		}
		return out, nil
	}
	return nil, lastErr
}

// clipTool is a (binary name, argv) pair the runner can invoke.
type clipTool struct {
	name string
	args []string
}

// writeCommands / readCommands hold the literal argv for each supported tool.
// wl-clipboard splits write and read across two binaries (wl-copy / wl-paste);
// xclip and xsel use the same binary with different flags. `wl-paste -n`
// suppresses the trailing newline wl-paste would otherwise append, so reads
// round-trip byte-for-byte with writes. Kept as package vars (not consts) so
// tests can read them without duplicating the strings.
var (
	writeCommands = map[string][]string{
		"wl-copy": {},                          // reads payload from stdin
		"xclip":   {"-selection", "clipboard"}, // reads payload from stdin
		"xsel":    {"--clipboard", "--input"},  // reads payload from stdin
	}
	readCommands = map[string][]string{
		"wl-paste": {"-n"},
		"xclip":    {"-selection", "clipboard", "-o"},
		"xsel":     {"--clipboard", "--output"},
	}
)

// writePreference / readPreference return the ordered binary names to try for
// the current session, most-preferred first.
func writePreference(env map[string]string) []string {
	if env["WAYLAND_DISPLAY"] != "" {
		return []string{"wl-copy", "xclip", "xsel"}
	}
	return []string{"xclip", "xsel", "wl-copy"}
}

func readPreference(env map[string]string) []string {
	if env["WAYLAND_DISPLAY"] != "" {
		return []string{"wl-paste", "xclip", "xsel"}
	}
	return []string{"xclip", "xsel", "wl-paste"}
}

func writeCandidates(getenv func(string) string, lookPath func(string) (string, error)) []clipTool {
	env := map[string]string{"WAYLAND_DISPLAY": getenv("WAYLAND_DISPLAY")}
	return clipboardCandidates(writePreference(env), writeCommands, lookPath)
}

func readCandidates(getenv func(string) string, lookPath func(string) (string, error)) []clipTool {
	env := map[string]string{"WAYLAND_DISPLAY": getenv("WAYLAND_DISPLAY")}
	return clipboardCandidates(readPreference(env), readCommands, lookPath)
}

// clipboardCandidates filters the preference order down to tools that are
// actually on PATH, preserving order. Tools missing from PATH are silently
// skipped so the caller only ever iterates over things that can plausibly run.
func clipboardCandidates(
	preference []string,
	commands map[string][]string,
	lookPath func(string) (string, error),
) []clipTool {
	out := make([]clipTool, 0, len(preference))
	for _, name := range preference {
		if _, err := lookPath(name); err != nil {
			continue
		}
		args, ok := commands[name]
		if !ok {
			continue
		}
		out = append(out, clipTool{name: name, args: args})
	}
	return out
}

// runClipboardWrite pipes payload to the tool's stdin and runs it.
//
// Critically, stdout and stderr are wired to real files (os.DevNull and a
// temp file), NOT to in-memory buffers. wl-copy/xclip/xsel fork a background
// daemon that inherits the parent's stdout/stderr; if those were os/exec
// pipes, Cmd.Wait would block until the daemon closed them — i.e. forever —
// which is the classic "xclip hangs in a script" deadlock. Using *os.File
// hands the descriptors straight to the child so Wait returns as soon as the
// foreground process exits, while the temp file still lets us recover stderr
// for an actionable error message.
func runClipboardWrite(name string, args []string, payload []byte) error {
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("clipboard write failed: open %s: %w", os.DevNull, err)
	}
	defer devnull.Close()

	errFile, err := os.CreateTemp("", "flowstate-clip-*.err")
	if err != nil {
		return fmt.Errorf("clipboard write failed: create temp: %w", err)
	}
	defer os.Remove(errFile.Name())
	defer errFile.Close()

	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Stdout = devnull
	cmd.Stderr = errFile
	if err := cmd.Run(); err != nil {
		msg := readTrimmed(errFile)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("clipboard write failed: %s exited non-zero: %s", name, msg)
	}
	return nil
}

// runClipboardRead runs the reader and returns its stdout. Read tools print
// the selection and exit (no daemon fork), so in-memory pipes are safe here.
func runClipboardRead(name string, args []string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := bytes.TrimSpace(errb.Bytes())
		if len(msg) == 0 {
			msg = []byte(err.Error())
		}
		return nil, fmt.Errorf("clipboard read failed: %s exited non-zero: %s", name, msg)
	}
	if out.Len() == 0 {
		return nil, nil
	}
	return out.Bytes(), nil
}

// readTrimmed rewinds the temp file and returns its trimmed contents.
func readTrimmed(f *os.File) string {
	if _, err := f.Seek(0, 0); err != nil {
		return ""
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(b))
}
