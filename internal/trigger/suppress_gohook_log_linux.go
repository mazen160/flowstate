//go:build linux

package trigger

// This file exists only to activate cgo for the trigger package on Linux,
// which causes the Go toolchain to compile and link the accompanying
// suppress_gohook_log_linux.c. That C file installs a no-op libuiohook
// logger via a priority-101 constructor — see the long comment in the .c
// file for the full rationale.
//
// On macOS and Windows we skip this entirely: gohook on those platforms
// doesn't emit the Wayland-only XkbGetKeyboard warning, so there is
// nothing to silence and no reason to pay the extra cgo build cost.

// #include <stdbool.h>
import "C"
