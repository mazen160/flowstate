//go:build !windows

package ui

import "golang.org/x/sys/unix"

// terminalWidth returns the column count for the terminal referred to by fd
// using the POSIX TIOCGWINSZ ioctl, or 0 if the query fails.
func terminalWidth(fd int) int {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}
