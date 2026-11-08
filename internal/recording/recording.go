// Package recording preserves each stage of a recording in a private,
// unique directory. Artifacts survive transcription and cleanup failures.
package recording

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mazen160/flowstate/internal/audio"
)

const DefaultDir = "~/.flowstate"

type Recording struct {
	Dir string
}

// NewInMemory processes audio without keeping any recording artifacts on disk.
func NewInMemory() *Recording { return &Recording{} }

type Audio struct {
	Data     []byte
	Filename string
	Removed  time.Duration
	Empty    bool
}

// New saves the original bytes before any processing takes place. An empty
// root defaults to ~/.flowstate. A leading ~/ expands to the user's home;
// other relative paths resolve from the working directory.
func New(root, filename string, original []byte) (*Recording, error) {
	if root == "" {
		root = DefaultDir
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve recording storage home: %w", err)
		}
		if root == "~" {
			root = homeDir
		} else {
			root = filepath.Join(homeDir, root[2:])
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create recording storage: %w", err)
	}
	dir, err := os.MkdirTemp(root, time.Now().UTC().Format("20060102T150405.000000000Z")+"-")
	if err != nil {
		return nil, fmt.Errorf("create recording directory: %w", err)
	}
	r := &Recording{Dir: dir}
	if err := r.write("original"+extension(filename), original); err != nil {
		return nil, err
	}
	return r, nil
}

// Prepare processes audio and saves it when this recording has a directory.
// When trimming is disabled, compressed audio is preserved as-is too.
func (r *Recording) Prepare(filename string, data []byte, trim bool) (Audio, error) {
	var removed time.Duration
	var empty bool
	ext := extension(filename)
	if trim {
		result, trimErr := audio.TrimSilence(data)
		if trimErr != nil {
			return Audio{}, trimErr
		}
		data, removed, empty = result.WAV, result.Removed, result.Empty
		ext = ".wav"
	}
	name := "modified" + ext
	if err := r.write(name, data); err != nil {
		return Audio{}, err
	}
	return Audio{Data: data, Filename: name, Removed: removed, Empty: empty}, nil
}

func (r *Recording) SaveRaw(text string) error     { return r.write("raw.txt", []byte(text)) }
func (r *Recording) SaveCleaned(text string) error { return r.write("cleaned.txt", []byte(text)) }

func (r *Recording) write(name string, data []byte) error {
	if r.Dir == "" {
		return nil
	}
	if err := os.WriteFile(filepath.Join(r.Dir, name), data, 0o600); err != nil {
		return fmt.Errorf("save recording %s: %w", name, err)
	}
	return nil
}

// Never use a supplied path or extension as an artifact filename.
func extension(filename string) string {
	switch ext := strings.ToLower(filepath.Ext(filename)); ext {
	case ".wav", ".webm", ".ogg", ".opus", ".mp4", ".m4a", ".mp3", ".flac":
		return ext
	default:
		return ".audio"
	}
}
