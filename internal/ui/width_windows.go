//go:build windows

package ui

import "golang.org/x/sys/windows"

// terminalWidth returns the column count for the console referred to by fd
// using the Windows console screen-buffer API, or 0 if the query fails.
//
// fd is the value of os.File.Fd(), which on Windows is the underlying
// console HANDLE. The visible width is the window rectangle (Right-Left+1),
// not the full back-buffer width, so a scrolled-back buffer doesn't report
// an inflated column count.
func terminalWidth(fd int) int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(fd), &info); err != nil {
		return 0
	}
	return int(info.Window.Right - info.Window.Left + 1)
}
