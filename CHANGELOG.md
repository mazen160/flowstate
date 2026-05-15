# Changelog

All notable changes to flowstate are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- `--device <uid>` no longer panics with `cgo argument has Go pointer to
  unpinned Go pointer` on Go 1.21+. The DeviceID is now pinned via
  `runtime.Pinner` for the duration of `malgo.InitDevice`.
### Added
- `output_language` config field — when set, the cleanup pass appends
  FreeFlow's translation directive to the system prompt and outputs in the
  requested language regardless of what was spoken. Default `""` (no
  translation).
- `preserve_clipboard_after_paste` config field — when paste output is
  enabled and this is true (default), flowstate snapshots the clipboard
  before paste and restores it ~500ms later, unless you copied something
  else in the meantime. Set to `false` to leave the transcript on the
  clipboard after pasting.
- `custom_vocabulary` config field — comma/newline/semicolon-separated
  list of high-priority terms appended to the cleanup system prompt so
  domain spellings survive the LLM rewrite. Default `""`.

### Breaking
- API key is now read from the `GROQ_API_KEY` environment variable
  (or `GROQ_API_TOKEN` as a fallback). The `api_key` field has been removed
  from the config file. Existing users must export `GROQ_API_KEY=...`
  before running flowstate; an unset key now fails fast with a friendly
  message rather than reporting an empty `api_key`.

### Changed
- `language` now defaults to `"en"` (English) instead of `""` (auto-detect).
  Auto-detect is still available — set `language = ""` explicitly to opt back
  in. Single-language users get faster, more accurate transcription by default.

## [0.1.0] — TBD
### Added
- Initial public release.
- Cross-platform CLI for voice dictation via Groq Whisper + Groq LLM cleanup.
- Two trigger modes: enter (default) and push-to-talk.
- Output destinations: stdout, clipboard, paste (or any combination, or "all").
- Mute-while-recording (best-effort, per-OS).
- Three embedded prompts (default, command, literal) overridable via config.
- TOML config with platform-correct default paths.
- Linux, macOS, and Windows binaries.
