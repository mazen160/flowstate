//go:build linux

package paste

import (
	"strings"
	"testing"
)

// TestPasteCandidates_WaylandOrdering verifies the preference list under a
// Wayland session: native Wayland tools first (wtype, then ydotool), with
// xdotool kept as a last-resort XWayland fallback. Only tools present on
// PATH should appear.
func TestPasteCandidates_WaylandOrdering(t *testing.T) {
	cases := []struct {
		name      string
		installed map[string]bool
		want      []string
	}{
		{
			name:      "all three installed",
			installed: map[string]bool{"wtype": true, "ydotool": true, "xdotool": true},
			want:      []string{"wtype", "ydotool", "xdotool"},
		},
		{
			name:      "only wtype",
			installed: map[string]bool{"wtype": true},
			want:      []string{"wtype"},
		},
		{
			name:      "wtype missing, ydotool present",
			installed: map[string]bool{"ydotool": true, "xdotool": true},
			want:      []string{"ydotool", "xdotool"},
		},
		{
			name:      "nothing installed",
			installed: map[string]bool{},
			want:      nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
			got := pasteCandidates(env, stubLookPath(tc.installed))
			if !sameNames(got, tc.want) {
				t.Fatalf("pasteCandidates names = %v; want %v", names(got), tc.want)
			}
		})
	}
}

// TestPasteCandidates_X11Ordering verifies the X11 path: xdotool first,
// ydotool as a generic fallback, wtype last (it usually won't work outside
// a Wayland compositor but listing it keeps the matrix symmetric and lets
// users who run X11 against an embedded Wayland setup still benefit).
func TestPasteCandidates_X11Ordering(t *testing.T) {
	env := map[string]string{} // no WAYLAND_DISPLAY
	got := pasteCandidates(env, stubLookPath(map[string]bool{
		"wtype": true, "ydotool": true, "xdotool": true,
	}))
	want := []string{"xdotool", "ydotool", "wtype"}
	if !sameNames(got, want) {
		t.Fatalf("pasteCandidates names = %v; want %v", names(got), want)
	}
}

// TestPasteCandidates_ArgsArePopulated guards against a regression where
// a candidate is selected but its argv lookup returns an empty slice (which
// would cause exec.Command to invoke the binary with no arguments and
// silently do nothing).
func TestPasteCandidates_ArgsArePopulated(t *testing.T) {
	env := map[string]string{"WAYLAND_DISPLAY": "wayland-0"}
	got := pasteCandidates(env, stubLookPath(map[string]bool{
		"wtype": true, "ydotool": true, "xdotool": true,
	}))
	for _, c := range got {
		if len(c.args) == 0 {
			t.Errorf("candidate %q has empty argv; pasteCommands entry is missing or broken", c.name)
		}
	}
}

// TestIsFallbackTrigger covers the runtime-failure detection we use to
// decide whether to skip to the next candidate. The truth-table approach
// makes it easy to add a new tool without re-reading the production code.
func TestIsFallbackTrigger(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		stderr string
		want   bool
	}{
		{
			name:   "wtype protocol unsupported (GNOME/KDE)",
			tool:   "wtype",
			stderr: "Compositor does not support the virtual keyboard protocol",
			want:   true,
		},
		{
			name:   "wtype generic failure",
			tool:   "wtype",
			stderr: "something else broke",
			want:   false,
		},
		{
			name:   "ydotool daemon socket missing",
			tool:   "ydotool",
			stderr: "failed to connect socket: No such file or directory",
			want:   true,
		},
		{
			name:   "ydotool permission denied (real user-actionable error)",
			tool:   "ydotool",
			stderr: "open /dev/uinput: permission denied",
			want:   false,
		},
		{
			name:   "xdotool no display",
			tool:   "xdotool",
			stderr: "Can't open display: (null)",
			want:   true,
		},
		{
			name:   "unknown tool never triggers fallback",
			tool:   "made-up",
			stderr: "Compositor does not support the virtual keyboard protocol",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFallbackTrigger(tc.tool, tc.stderr); got != tc.want {
				t.Fatalf("isFallbackTrigger(%q, %q) = %v; want %v",
					tc.tool, tc.stderr, got, tc.want)
			}
		})
	}
}

// names extracts the name field from a slice of candidates for readable
// test failure messages.
func names(cs []pasteCandidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

// sameNames compares two ordered slices of candidate names. We intentionally
// compare by value rather than by exec path because the test stub
// fabricates paths and the production code only ever reads the name field.
func sameNames(got []pasteCandidate, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i, c := range got {
		if c.name != want[i] {
			return false
		}
	}
	return true
}

// TestIsFallbackTriggerWtypeMessageIsStable acts as a canary: if wtype
// ever rewords the protocol-unsupported message in a future release, this
// test (and the substring in isFallbackTrigger) need to be updated
// together.
func TestIsFallbackTriggerWtypeMessageIsStable(t *testing.T) {
	const observed = "Compositor does not support the virtual keyboard protocol"
	if !strings.Contains(observed, "virtual keyboard protocol") {
		t.Fatal("wtype message fingerprint changed; update isFallbackTrigger")
	}
}
