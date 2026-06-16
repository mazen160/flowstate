# Changelog

All notable changes to flowstate are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.3] — 2026-06-15

### Added
- The `✓ Done` summary now confirms which destinations received the text on
  a trailing line, e.g. `→ printed to stdout · copied to clipboard · pasted
  into focused app`. Only destinations that actually succeeded are listed
  (a clipboard seeded solely to back a paste keystroke is not, since that
  text is transient). The line is omitted when no destination succeeded.

### Fixed
- **Windows build was broken** by the narrow-terminal line-wrap change, which
  called the Unix-only `TIOCGWINSZ` ioctl from a non-build-tagged file
  (`undefined: unix.IoctlGetWinsize` on `windows-latest`). Terminal-width
  detection is now split per-OS — POSIX keeps the ioctl, Windows uses the
  console screen-buffer API — so all three platforms compile.
- **Clipboard output was silently lost on Linux.** `output_mode = clipboard`
  (half of the default `stdout,clipboard`) relied on an in-process X11/Wayland
  clipboard owner, but flowstate is a one-shot CLI — the moment it exited, the
  selection ownership died and the clipboard went empty, so nothing was there
  to paste. The Linux clipboard now shells out to `wl-copy` / `xclip` / `xsel`
  (chosen by session, mirroring the paste-tool fallback), which keep the text
  resident after flowstate exits. macOS and Windows were unaffected (their
  system pasteboards persist) and keep the existing in-process backend.
- `flowstate version` no longer prints a doubled `go` prefix. The Go
  runtime string already carries its own `go` prefix (e.g. `go1.24.5`),
  so the output is now `flowstate v1.0.2 (go1.24.5)` instead of
  `flowstate v1.0.2 (go go1.24.5)`.
- Synced the in-tree default version constant and the documented
  `flowstate version` / `GET /api/info` examples to `1.0.2`.

### Changed
- Linux clipboard output now requires one of `wl-copy` (wl-clipboard),
  `xclip`, or `xsel` to be installed. A run with none installed reports
  `clipboard unavailable: ... none of wl-copy, xclip, or xsel are installed`
  instead of failing silently.

## [1.0.2] — 2026-06-09

### Added
- Web UI "Mic Record" tab: a second view alongside Transcribe for recording
  raw mic clips directly in the browser. Includes tab navigation, a mic
  device picker with a refresh button, per-recording Copy and Download
  buttons, and session export. Mic clips and transcripts share the same
  localStorage-backed session history.

### Fixed
- Linux paste: `ydotool` is now tried as a fallback when the preferred tool
  fails at runtime. The selection logic uses an ordered candidate list —
  Wayland: `wtype → ydotool → xdotool`; X11: `xdotool → ydotool → wtype`
  — and detects runtime rejections (e.g. `wtype`'s "Compositor does not
  support the virtual keyboard protocol" on GNOME/KDE) to advance to the
  next candidate instead of surfacing an error the user cannot act on.
  Genuinely actionable errors (permission-denied on `/dev/uinput`, etc.)
  are still surfaced verbatim.
- Linux trigger: suppressed libuiohook's harmless `XkbGetKeyboard` warning
  before it reaches the terminal, removing noise on push-to-talk sessions.

### Documentation
- Linux build prerequisites: added `libxt-dev` to the apt package list
  required for `gohook` header resolution.

## [1.0.1] — 2026-05-16

### Added
- `--silent` flag on the record command. Suppresses every stderr message
  (status, warnings, errors) so a `flowstate --silent | pbcopy` pipeline
  yields exactly the cleaned transcript on stdout and nothing else. Exit
  code still signals success vs failure (0 / 1 / 2).
- `flowstate web` now opens the UI in your default browser automatically
  once the listener is bound. `--no-browser` opts out and reverts to the
  previous "print the URL and wait" behavior. The open command is per-OS
  (`open` on macOS, `rundll32 url.dll,FileProtocolHandler` on Windows,
  `xdg-open` with a `sensible-browser` fallback on Linux).
- Web UI auth gate for servers started with `--web-token` or
  `FLOWSTATE_WEB_TOKEN`. When auth is required and no token is saved, the
  page now opens with a focused token prompt before recording is available.
- Explicit Save buttons for the first-run auth prompt and Settings token
  field. Token edits are no longer persisted on blur.
- `GET /api/ping` authenticated token-check endpoint. The web UI validates
  a token before storing it in `localStorage`.

### Improved
- Replace the blocking browser token prompt after a failed transcription
  with in-page auth recovery, status text, and toast feedback.
- Verify a previously saved web token on page load and reopen the auth
  prompt if the server rejects it.
- Fix the Settings drawer toggle so opening Settings focuses the intended
  control instead of only focusing while closing.
- Wrap Linux CI tests in `xvfb-run` so `gohook` can initialize
  `XOpenDisplay` on headless GitHub Actions runners.

## [1.0.0] — 2026-05-15

### Added
- Initial public release. Cross-platform single-binary CLI for voice
  dictation via Groq Whisper + Groq LLM cleanup. Linux, macOS, and
  Windows (cgo-enabled).
- Two trigger modes: `enter` (default, no permissions) and
  `push-to-talk` (hold a key, via `robotn/gohook` global keyboard hook).
- Output destinations selected via `output_mode`: any subset of
  `stdout` / `clipboard` / `paste`, or the literal `"all"`.
- Three embedded prompts (`default`, `command`, `literal`) inlined into
  the TOML config on first `flowstate config init` so users can edit
  them in place.
- TOML config at platform-default paths (`$XDG_CONFIG_HOME/flowstate/`
  on macOS/Linux, `%APPDATA%\flowstate\` on Windows). `--config <path>`
  and `$FLOWSTATE_CONFIG` overrides.
- Subcommands: `flowstate version`, `flowstate config init [--force]`,
  `flowstate config path`, `flowstate devices`, `flowstate web`.
- `flowstate web` subcommand: local HTTP server (default
  `127.0.0.1:8585`) with a vanilla-JS browser UI. The page captures
  audio via `MediaRecorder`, POSTs it to `/api/transcribe`, and runs the
  same Groq pipeline as the CLI. Per-session transcript history lives
  in `localStorage` — the server never persists audio or transcripts.
  Three flags: `--web-interface-listen`, `--web-port`, `--web-token`
  (optional Bearer auth; falls back to `FLOWSTATE_WEB_TOKEN` env var).
  Non-loopback binds without a token print a stderr warning at startup.
- `--max-time <seconds>` flag + `max_time_seconds` config field.
  Recording auto-stops after the given duration and processes
  normally (exit 0). Users can still stop early; first signal wins.
- `--paste-delay <seconds>` flag + `paste_delay_seconds` config field.
  When `paste` is enabled, flowstate writes the transcript to the
  clipboard, prints a `● Pasting in Ns…` status, waits the delay,
  then fires the paste keystroke. Lets the user switch to the
  destination window before the keystroke lands.
- `output_language` config field — when set, appends a translation
  directive to the cleanup system prompt so the output lands in the
  requested language regardless of what was spoken.
- `preserve_clipboard_after_paste` config field — when paste output is
  enabled and this is true (default), flowstate snapshots the clipboard
  before paste and restores it ~500 ms later, unless you copied
  something else in the meantime.
- `custom_vocabulary` config field — comma/newline/semicolon-separated
  list of high-priority terms appended to the cleanup system prompt so
  domain spellings survive the LLM rewrite.
- `colors` config field and `--no-color` flag — control ANSI colors in
  the staged status output. Values: `"auto"` (default; TTY-detect),
  `"always"`, `"never"`. The `NO_COLOR` environment variable is also
  honored per the no-color.org convention.
- Live audio-level meter on the recording prompt when stderr is a TTY.
- Staged status lines (`● Transcribing…`, `● Cleaning up…`,
  `✓ Done — N chars · N words · ~N tokens / recording Ns ·
  processing Ns · total Ns`) replacing the previous plain `Recording…`
  / `Done.` prints.
- Web UI auto-copy toggle in Settings — when on, the cleaned transcript
  is written to the clipboard automatically after each transcription.
  Click-to-toggle recording (no longer push-to-talk), per-row Copy
  buttons on every transcript, JSON / Markdown session export, "Clear
  local data" button in Settings.

### Behavior
- API key is read from the `GROQ_API_KEY` environment variable (or
  `GROQ_API_TOKEN` as a fallback) — never stored in the config file.
  Missing key fails fast with a friendly message naming both env vars.
- `language` defaults to `"en"` (English). Set to `""` for Whisper
  auto-detect, or `"fr"`/`"es"`/etc. for other languages.
- Hallucination filter: ten common short phrases (`thank you`,
  `please subscribe`, `subtitles by…`, etc.) are dropped when Whisper
  reports `no_speech_prob >= 0.1`.
- All status messages go to stderr; stdout receives only the cleaned
  transcript. Pipe-safe: `flowstate | wc -w`, `flowstate > note.md`,
  `flowstate | pbcopy` all work cleanly.
