//go:build !linux

package clipboard

import (
	"fmt"
	"sync"

	gocb "golang.design/x/clipboard"
)

// On macOS (NSPasteboard) and Windows (Win32 clipboard) the system clipboard
// is a persistent store that outlives the process that wrote it, so the
// in-process golang.design/x/clipboard backend works correctly for a one-shot
// CLI. We keep using it on these platforms rather than shelling out.

// initOnce guards gocb.Init, which is a cgo init and must run exactly once
// per process. The result is cached so subsequent calls fail fast on, e.g.,
// a headless system instead of re-trying init each time.
var (
	initOnce sync.Once
	initErr  error
)

func ensureInit() error {
	initOnce.Do(func() { initErr = gocb.Init() })
	return initErr
}

// Write copies text to the system clipboard.
func Write(text []byte) error {
	if err := ensureInit(); err != nil {
		return fmt.Errorf("clipboard unavailable: %w", err)
	}
	gocb.Write(gocb.FmtText, text)
	return nil
}

// Read returns the current clipboard text, or nil if empty/unavailable.
func Read() ([]byte, error) {
	if err := ensureInit(); err != nil {
		return nil, fmt.Errorf("clipboard unavailable: %w", err)
	}
	return gocb.Read(gocb.FmtText), nil
}
