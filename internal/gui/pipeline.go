// Package gui implements `flowstate ui`: a native Linux desktop UI (Fyne,
// no Electron, no browser) inspired by Whisperflow's floating pill. It
// reuses the exact record-pipeline stages from the CLI — capture in
// gui.go, transcribe + cleanup in pipeline.go — so wire behavior (models,
// prompts, silence trimming, artifact layout) matches `flowstate` and
// `flowstate web` byte-for-byte.
package gui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mazen160/flowstate/internal/cleanup"
	"github.com/mazen160/flowstate/internal/recording"
	"github.com/mazen160/flowstate/internal/transcribe"
)

// PipelineOpts carries the knobs Process needs. Callers build it from the
// live *config.Config (see gui.go) so UI edits apply without a restart.
type PipelineOpts struct {
	APIKey             string
	BaseURL            string
	TranscriptionModel string
	Language           string
	CleanupModel       string
	FallbackModel      string
	// SystemPrompt is the active prompt body, already augmented with the
	// output-language directive and custom-vocabulary block.
	SystemPrompt string
	TrimSilence  bool
	// SaveRecordings + StorageDir mirror the CLI flags: when saving is
	// on, original/modified audio plus raw/cleaned text land in a fresh
	// timestamped directory (see recording.New).
	SaveRecordings bool
	StorageDir     string
}

// PipelineResult is what a finished recording produced. Raw and Cleaned
// are both "" when the audio held no speech (or the hallucination filter
// dropped it) — the caller renders "(no speech detected)" and exits the
// processing state exactly like the CLI's exit-0 path.
type PipelineResult struct {
	Raw            string
	Cleaned        string
	RecordingDir   string
	RemovedSilence time.Duration
	ProcessTime    time.Duration
}

// Process runs the transcribe → cleanup stages over an already-recorded
// WAV file. It mirrors runRecord's steps 8–12 (read → archive → trim →
// transcribe → cleanup → output files) so the GUI and CLI stay identical
// on the wire.
func Process(ctx context.Context, wavPath string, o PipelineOpts) (PipelineResult, error) {
	t0 := time.Now()
	var res PipelineResult

	original, err := os.ReadFile(wavPath)
	if err != nil {
		return res, fmt.Errorf("read recording: %w", err)
	}
	saved := recording.NewInMemory()
	if o.SaveRecordings {
		saved, err = recording.New(o.StorageDir, wavPath, original)
		if err != nil {
			return res, err
		}
	}
	processed, err := saved.Prepare(wavPath, original, o.TrimSilence)
	if err != nil {
		return res, fmt.Errorf("process audio: %w", err)
	}
	res.RecordingDir = saved.Dir
	res.RemovedSilence = processed.Removed
	if processed.Empty {
		_ = saved.SaveRaw("")
		_ = saved.SaveCleaned("")
		res.ProcessTime = time.Since(t0)
		return res, nil
	}

	tx := transcribe.NewClient(transcribe.Options{
		APIKey:   o.APIKey,
		BaseURL:  o.BaseURL,
		Model:    o.TranscriptionModel,
		Language: o.Language,
	})
	raw, err := tx.TranscribeReader(ctx, bytes.NewReader(processed.Data), processed.Filename)
	if err != nil {
		return res, fmt.Errorf("transcribe: %w", err)
	}
	if err := saved.SaveRaw(raw); err != nil {
		return res, err
	}
	if raw == "" {
		_ = saved.SaveCleaned("")
		res.ProcessTime = time.Since(t0)
		return res, nil
	}
	// Record the raw transcript now: if cleanup fails, the caller still gets
	// the words the user actually said instead of losing the whole take.
	res.Raw = raw

	cl := cleanup.NewClient(cleanup.Options{
		APIKey:        o.APIKey,
		BaseURL:       o.BaseURL,
		Model:         o.CleanupModel,
		FallbackModel: o.FallbackModel,
	})
	cleaned, err := cl.Clean(ctx, o.SystemPrompt, raw, "")
	if err != nil {
		res.ProcessTime = time.Since(t0)
		return res, fmt.Errorf("cleanup: %w", err)
	}
	if err := saved.SaveCleaned(cleaned); err != nil {
		return res, err
	}
	res.Cleaned = cleaned
	res.ProcessTime = time.Since(t0)
	return res, nil
}

// RecentItem is one row in the History tab: a past artifact directory with
// a short preview. Time falls back to the directory mtime when the name
// doesn't parse (foreign directories dropped into the storage root).
type RecentItem struct {
	Dir     string
	Time    time.Time
	Preview string
	HasText bool
}

// RecentRecordings lists up to n artifact directories under storageDir,
// newest first. The storage root may not exist (fresh install) — that
// yields an empty slice, not an error. Unreadable preview files degrade
// to an empty preview rather than dropping the row.
func RecentRecordings(storageDir string, n int) []RecentItem {
	dir := expandHome(storageDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// Artifact directories are timestamp-prefixed, so reverse-lexical
	// order is newest-first without a stat per entry.
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	if n > 0 && len(dirs) > n {
		dirs = dirs[:n]
	}
	var out []RecentItem
	for _, name := range dirs {
		full := filepath.Join(dir, name)
		item := RecentItem{Dir: full}
		if st, err := os.Stat(full); err == nil {
			item.Time = st.ModTime()
		}
		// Prefer the cleaned text; fall back to raw when cleanup never
		// ran (transcribe error, empty result).
		for _, f := range []string{"cleaned.txt", "raw.txt"} {
			b, err := os.ReadFile(filepath.Join(full, f))
			if err != nil {
				continue
			}
			if p := previewText(string(b)); p != "" {
				item.Preview = p
				item.HasText = true
				break
			}
		}
		out = append(out, item)
	}
	return out
}

// previewText collapses whitespace and clips to a list-row-sized snippet.
// Clipping is rune-based so a multi-byte character is never cut in half.
func previewText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const max = 120
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

// expandHome resolves a leading "~/" against the user's home directory,
// mirroring recording.New's convention for the storage root.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}
