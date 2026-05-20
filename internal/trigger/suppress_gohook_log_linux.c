// Silence libuiohook's stderr noise on Linux by installing a no-op logger
// before gohook's own constructor runs.
//
// Background: gohook (vendored copy of libuiohook) ships a C constructor —
//   __attribute__((constructor)) void on_library_load(void)
// in hook/x11/properties_c.h — that calls load_input_helper() during the
// dynamic-linker init phase, BEFORE Go's runtime, init() functions, or
// main() ever run. On Wayland sessions, load_input_helper() prints
//
//   load_input_helper [PID]: XkbGetKeyboard failed to locate a valid keyboard!
//
// to stderr unconditionally. The XWayland fallback path still works fine,
// so the message is cosmetic, but there is no env var or Go API to disable
// it: libuiohook routes everything through a logger function pointer
// (logger_c.h) whose default writes to stderr.
//
// The fix here uses two facts:
//  1. libuiohook exposes hook_set_logger(logger_t), which swaps the global
//     pointer; calling it with a no-op logger silences ALL library output.
//  2. C constructors execute in ascending priority order; a constructor
//     with priority 101 runs before any constructor with no explicit
//     priority (which defaults to 65535). User-defined priorities must be
//     >= 101 — anything lower is reserved for the implementation.
//
// So we install a silent logger at priority 101. By the time gohook's
// no-priority on_library_load() fires and reaches its first logger(...)
// call, the global function pointer already points to our drop-everything
// stub, and nothing reaches stderr.
//
// Trade-off: this also silences any real gohook warnings (e.g. genuine
// XOpenDisplay failures). That's acceptable because (a) gohook never
// surfaces actionable errors via logger() — operational failures bubble
// up through Go-level return values from hook.Start/hook.Register, and
// (b) silencing the entire C logger is the only mechanism upstream
// provides; line-by-line filtering would require either fd-level pipe
// tricks that can't fire early enough, or patching gohook itself.

#include <stdarg.h>
#include <stdbool.h>

// libuiohook's logger function-pointer type, copied verbatim from
// gohook/hook/logger.h so we don't have to add gohook's include dirs to
// this translation unit's CFLAGS.
typedef bool (*logger_t)(unsigned int level, const char *format, ...);

// Declared in gohook/hook/logger_c.h. Default C visibility is sufficient
// because gohook's C sources and ours are linked into the same Go binary.
extern void hook_set_logger(logger_t logger_proc);

static bool flowstate_silent_logger(unsigned int level, const char *format, ...) {
    (void)level;
    (void)format;
    return true;
}

__attribute__((constructor(101)))
static void flowstate_install_silent_gohook_logger(void) {
    hook_set_logger(flowstate_silent_logger);
}
