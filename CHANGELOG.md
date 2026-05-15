# Changelog

All notable changes to flowstate are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
