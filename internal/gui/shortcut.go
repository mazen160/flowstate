package gui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// gnomeKeysSchema is the GNOME settings schema that holds custom global
// shortcuts. One entry is a path under custom-keybindings/ with name,
// command, and binding keys.
const (
	gnomeKeysSchema = "org.gnome.settings-daemon.plugins.media-keys"
	gnomeKeyList    = "custom-keybindings"
	gnomeCustomPath = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/flowstate/"
	// gnomeCustomEntry is the relocatable schema id for one shortcut.
	gnomeCustomEntry = gnomeKeysSchema + ".custom-keybinding:" + gnomeCustomPath
)

// OnWayland reports whether this is a Wayland session, where gohook's X11
// hook cannot see keys typed into other apps.
func OnWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("XDG_SESSION_TYPE") == "wayland"
}

// IsGNOME reports whether the desktop shell is GNOME (the one whose
// shortcut store we know how to write).
func IsGNOME() bool {
	return strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "GNOME")
}

// Accelerator converts a gohook-style key name ("f9", "space") into a
// GTK accelerator for the desktop shortcut. Anything already in accelerator
// form ("<Control><Alt>r", "F9") passes through untouched. Named keys use
// GTK's spellings, which differ from gohook's (space, Tab, Insert).
func Accelerator(key string) string {
	k := strings.TrimSpace(key)
	if k == "" {
		return "F9"
	}
	if strings.ContainsAny(k, "<+") {
		return k
	}
	if named, ok := gtkKeyNames[strings.ToLower(k)]; ok {
		return named
	}
	if len(k) == 1 {
		return strings.ToUpper(k)
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

// gtkKeyNames maps the key names whose GTK spelling isn't just a capitalised
// copy of the gohook name.
var gtkKeyNames = map[string]string{
	"space":     "space",
	"tab":       "Tab",
	"enter":     "Return",
	"return":    "Return",
	"escape":    "Escape",
	"backspace": "BackSpace",
	"delete":    "Delete",
	"insert":    "Insert",
	"home":      "Home",
	"end":       "End",
	"pageup":    "Page_Up",
	"pagedown":  "Page_Down",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
}

// InstallGNOMEShortcut registers (or replaces) a GNOME custom shortcut that
// runs `binPath ui --toggle` on accel. This is the Wayland-safe global
// hotkey: GNOME owns the key grab, so it fires no matter which app has
// focus, and the user can rebind it in Settings → Keyboard → Custom
// Shortcuts.
func InstallGNOMEShortcut(binPath, accel string) error {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return fmt.Errorf("gsettings not found; this install path is GNOME-only")
	}
	if !IsGNOME() {
		return fmt.Errorf("not a GNOME session (XDG_CURRENT_DESKTOP=%q); add a custom shortcut in your desktop's settings that runs %q",
			os.Getenv("XDG_CURRENT_DESKTOP"), binPath+" ui --toggle")
	}

	entry := gnomeCustomPath
	out, err := exec.Command("gsettings", "get", gnomeKeysSchema, gnomeKeyList).Output()
	if err != nil {
		return fmt.Errorf("read existing shortcuts: %w", err)
	}
	paths := quotedItems(string(out))
	if !contains(paths, entry) {
		paths = append(paths, entry)
		if err := exec.Command("gsettings", "set", gnomeKeysSchema, gnomeKeyList,
			gvariantList(paths)).Run(); err != nil {
			return fmt.Errorf("register shortcut path: %w", err)
		}
	}

	command := binPath + " ui --toggle"
	for _, kv := range [][2]string{
		{"name", "flowstate"},
		{"command", command},
		{"binding", accel},
	} {
		if err := exec.Command("gsettings", "set", gnomeCustomEntry, kv[0], kv[1]).Run(); err != nil {
			return fmt.Errorf("set shortcut %s: %w", kv[0], err)
		}
	}
	return nil
}

// quotedItems pulls the single-quoted strings out of a GVariant array
// literal, e.g. "['/a/', '/b/']" -> ["/a/", "/b/"].
func quotedItems(s string) []string {
	var out []string
	rest := s
	for {
		start := strings.IndexByte(rest, '\'')
		if start < 0 {
			return out
		}
		rest = rest[start+1:]
		end := strings.IndexByte(rest, '\'')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		rest = rest[end+1:]
	}
}

// gvariantList renders paths as the GVariant array literal gsettings wants.
func gvariantList(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = "'" + p + "'"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}
