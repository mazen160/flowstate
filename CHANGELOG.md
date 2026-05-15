# Changelog

All notable changes to flowstate are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
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
