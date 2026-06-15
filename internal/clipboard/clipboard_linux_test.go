//go:build linux

package clipboard

import "testing"

// stubLookPath returns a LookPath stub that reports a tool as installed iff
// it appears in found. The fabricated path is irrelevant — production code
// only ever reads the candidate name.
func stubLookPath(found map[string]bool) func(string) (string, error) {
	return func(name string) (string, error) {
		if found[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errNotFound
	}
}

var errNotFound = &lookErr{}

type lookErr struct{}

func (*lookErr) Error() string { return "not found" }

func names(cs []clipTool) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}

func sameNames(got []clipTool, want []string) bool {
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

// TestWriteCandidates_WaylandOrdering: on Wayland we prefer the native
// wl-copy, then fall back to xclip and xsel via XWayland. Only installed
// tools appear.
func TestWriteCandidates_WaylandOrdering(t *testing.T) {
	cases := []struct {
		name      string
		installed map[string]bool
		want      []string
	}{
		{
			name:      "all installed",
			installed: map[string]bool{"wl-copy": true, "xclip": true, "xsel": true},
			want:      []string{"wl-copy", "xclip", "xsel"},
		},
		{
			name:      "only xsel (common minimal X11-on-Wayland host)",
			installed: map[string]bool{"xsel": true},
			want:      []string{"xsel"},
		},
		{
			name:      "wl-copy missing, xclip present",
			installed: map[string]bool{"xclip": true, "xsel": true},
			want:      []string{"xclip", "xsel"},
		},
		{
			name:      "nothing installed",
			installed: map[string]bool{},
			want:      nil,
		},
	}

	getenv := func(string) string { return "wayland-0" }
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := writeCandidates(getenv, stubLookPath(tc.installed))
			if !sameNames(got, tc.want) {
				t.Fatalf("writeCandidates names = %v; want %v", names(got), tc.want)
			}
		})
	}
}

// TestWriteCandidates_X11Ordering: on X11 (no WAYLAND_DISPLAY) we prefer
// xclip, then xsel, with wl-copy as a last-resort.
func TestWriteCandidates_X11Ordering(t *testing.T) {
	getenv := func(string) string { return "" }
	got := writeCandidates(getenv, stubLookPath(map[string]bool{
		"wl-copy": true, "xclip": true, "xsel": true,
	}))
	want := []string{"xclip", "xsel", "wl-copy"}
	if !sameNames(got, want) {
		t.Fatalf("writeCandidates names = %v; want %v", names(got), want)
	}
}

// TestReadCandidates_WaylandOrdering: reads use wl-paste (not wl-copy) on
// Wayland, then xclip/xsel.
func TestReadCandidates_WaylandOrdering(t *testing.T) {
	getenv := func(string) string { return "wayland-0" }
	got := readCandidates(getenv, stubLookPath(map[string]bool{
		"wl-paste": true, "xclip": true, "xsel": true,
	}))
	want := []string{"wl-paste", "xclip", "xsel"}
	if !sameNames(got, want) {
		t.Fatalf("readCandidates names = %v; want %v", names(got), want)
	}
}

// TestReadCandidates_X11Ordering: xclip first, xsel, then wl-paste.
func TestReadCandidates_X11Ordering(t *testing.T) {
	getenv := func(string) string { return "" }
	got := readCandidates(getenv, stubLookPath(map[string]bool{
		"wl-paste": true, "xclip": true, "xsel": true,
	}))
	want := []string{"xclip", "xsel", "wl-paste"}
	if !sameNames(got, want) {
		t.Fatalf("readCandidates names = %v; want %v", names(got), want)
	}
}

// TestCandidates_ArgsArePopulated guards against a regression where a
// candidate is selected but its argv lookup returns a nil slice. wl-copy is
// the sole legitimately-empty argv (it reads everything from stdin), so it's
// exempt; every other tool must carry a selection flag.
func TestCandidates_ArgsArePopulated(t *testing.T) {
	getenv := func(string) string { return "wayland-0" }
	all := map[string]bool{"wl-copy": true, "wl-paste": true, "xclip": true, "xsel": true}

	for _, c := range writeCandidates(getenv, stubLookPath(all)) {
		if c.name != "wl-copy" && len(c.args) == 0 {
			t.Errorf("write candidate %q has empty argv; writeCommands entry is missing", c.name)
		}
	}
	for _, c := range readCandidates(getenv, stubLookPath(all)) {
		if len(c.args) == 0 {
			t.Errorf("read candidate %q has empty argv; readCommands entry is missing", c.name)
		}
	}
}

// TestWrite_NoToolsInstalled returns an actionable error rather than
// silently succeeding when no clipboard utility is present. We can't easily
// stub exec.LookPath inside Write (it closes over the real one), so we assert
// on the candidate list instead: an empty list is what drives the error path.
func TestWrite_NoToolsInstalled(t *testing.T) {
	getenv := func(string) string { return "wayland-0" }
	if got := writeCandidates(getenv, stubLookPath(map[string]bool{})); len(got) != 0 {
		t.Fatalf("expected no candidates when nothing is installed; got %v", names(got))
	}
}
