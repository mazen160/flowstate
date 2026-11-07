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
