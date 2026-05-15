# Changelog

All notable changes to flowstate are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
  them in place. The `default` and `command` strings are byte-for-byte
  ports of FreeFlow's `PostProcessingService` prompts.
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
- `output_language` config field — when set, appends FreeFlow's
  translation directive to the cleanup system prompt and outputs in the
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
- Hallucination filter ported verbatim from FreeFlow: ten common short
  phrases (`thank you`, `please subscribe`, `subtitles by…`, etc.) are
  dropped when Whisper reports `no_speech_prob >= 0.1`.
- All status messages go to stderr; stdout receives only the cleaned
  transcript. Pipe-safe: `flowstate | jq`, `flowstate > note.md`,
  `flowstate | pbcopy` all work cleanly.
