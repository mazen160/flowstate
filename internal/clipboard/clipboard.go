// Package clipboard provides persistent system-clipboard read/write that
// survives this process exiting.
//
// This matters because flowstate is a one-shot CLI: it records, transcribes,
// copies the result, and exits. On Linux the X11/Wayland clipboard model
// keeps clipboard data inside the *owning process* — whoever called
// "set selection" must stay running to serve paste requests. An in-process
// clipboard library (e.g. golang.design/x/clipboard) therefore loses the
// data the instant flowstate exits, so a plain `output_mode = clipboard`
// run leaves the user with an empty clipboard.
//
// To avoid that, the Linux implementation shells out to a real clipboard
// utility (wl-copy / xclip / xsel) that forks a background daemon to retain
// the selection after the foreground command returns — exactly the same
// "shell out to whatever the session provides" strategy used by
// [internal/paste] for the paste keystroke.
//
// macOS and Windows expose a persistent system pasteboard, so on those
// platforms the package keeps using the in-process golang.design/x/clipboard
// backend (see clipboard_other.go), which works correctly across process
// exit.
//
// The exported surface — [Write] and [Read] — is declared per-platform in
// the build-tagged files; this file holds only the package documentation so
// godoc surfaces it everywhere.
package clipboard
