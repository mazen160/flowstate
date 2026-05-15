// Package paste simulates the system-wide "paste from clipboard" keystroke
// for whichever window currently has focus.
//
// The clipboard must already contain the text; this package is solely
// responsible for the OS-specific keystroke that triggers a paste:
//
//   - macOS:   osascript → System Events → keystroke "v" with Cmd
//   - Linux:   wtype (Wayland) or xdotool (X11) → Ctrl+V
//   - Windows: powershell → WScript.Shell SendKeys '^v'
//
// Each platform's implementation lives in a build-tagged file. The
// exported [Paste] function is declared in each of those files; this file
// holds the package-level documentation only so godoc surfaces it on every
// platform.
package paste
