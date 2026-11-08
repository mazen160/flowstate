package recording

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArtifactsArePrivateAndIsolated(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".flowstate")
	original := []byte("original compressed bytes")
	first, err := New(root, "../../recording.webm", original)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root, "recording.webm", original)
	if err != nil {
		t.Fatal(err)
	}
	if first.Dir == second.Dir {
		t.Fatal("recordings share a directory")
	}
	processed, err := first.Prepare("recording.webm", original, false)
	if err != nil || processed.Removed != 0 || processed.Empty || !bytes.Equal(processed.Data, original) {
		t.Fatalf("prepare: %+v, %v", processed, err)
	}
	if err := first.SaveRaw("um hello"); err != nil {
		t.Fatal(err)
	}
	if err := first.SaveCleaned("Hello."); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{"original.webm": original, "modified.webm": original, "raw.txt": []byte("um hello"), "cleaned.txt": []byte("Hello.")} {
		path := filepath.Join(first.Dir, name)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %q, %v", name, got, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions: %v", name, info.Mode())
		}
	}
	for _, dir := range []string{root, first.Dir, second.Dir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%s permissions: %v", dir, info.Mode())
		}
	}
}

func TestOriginalSurvivesProcessingFailure(t *testing.T) {
	original := []byte("unrecognized container")
	r, err := New(t.TempDir(), "clip.webm", original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare("clip.webm", original, true); err == nil {
		t.Fatal("expected unsupported format error")
	}
	got, err := os.ReadFile(filepath.Join(r.Dir, "original.webm"))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("original lost: %v", err)
	}
}

func TestStorageDirectoryResolution(t *testing.T) {
	homeDir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", homeDir)
	} else {
		t.Setenv("HOME", homeDir)
	}
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	// Resolve symlinks in the temporary directory (e.g. /var on macOS)
	// so the expected relative path matches the OS working directory.
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, root, want string
	}{
		{"default", "", filepath.Join(homeDir, ".flowstate")},
		{"configured default", "~/.flowstate", filepath.Join(homeDir, ".flowstate")},
		{"home override", "~/recordings", filepath.Join(homeDir, "recordings")},
		{"relative override", ".flowstate", filepath.Join(workingDir, ".flowstate")},
		{"absolute override", filepath.Join(homeDir, "custom"), filepath.Join(homeDir, "custom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New(tc.root, "clip.wav", []byte("original"))
			if err != nil {
				t.Fatal(err)
			}
			dir, err := filepath.Abs(r.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(dir) != tc.want {
				t.Fatalf("recording stored in %q, want a directory under %q", dir, tc.want)
			}
			data, err := os.ReadFile(filepath.Join(dir, "original.wav"))
			if err != nil || string(data) != "original" {
				t.Fatalf("original missing from resolved directory: %q, %v", data, err)
			}
		})
	}
}

func TestDefaultStorageFailsWithoutHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", "")
	} else {
		t.Setenv("HOME", "")
	}
	t.Chdir(t.TempDir())
	if _, err := New("", "clip.wav", []byte("original")); err == nil {
		t.Fatal("default storage must fail when the home directory is unavailable")
	}
	if _, err := os.Stat(".flowstate"); !os.IsNotExist(err) {
		t.Fatalf("default storage touched the working directory: %v", err)
	}
	if _, err := New("custom", "clip.wav", []byte("original")); err != nil {
		t.Fatalf("explicit storage should work without a home directory: %v", err)
	}
}
