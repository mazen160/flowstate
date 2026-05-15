# flowstate

Cross-platform CLI for voice dictation via Groq.

## Status

Alpha. v0, not yet released. The binary builds and the unit + smoke tests pass
on macOS, Linux, and Windows, but the audio capture and paste-as-keystrokes
paths have only been hand-exercised on a small number of hosts. Expect rough
edges, especially around microphone selection and paste timing.

## Why flowstate

flowstate records your microphone, sends the audio to Groq's Whisper endpoint
for transcription, runs the raw transcript through a small LLM cleanup pass,
and emits the cleaned text to stdout, your clipboard, or paste-as-keystrokes
into the currently focused app.

It is a one-binary, no-GUI port of the pipeline that powers
[FreeFlow](https://github.com/zachlatta/freeflow) (Swift, macOS-only), with
the same wire-level behavior against Groq. The point of building it as a Go
CLI is so the same dictation workflow runs on Linux servers, Windows
workstations, and a Mac without three different installers.

You probably want flowstate if:

- You already pay for a Groq API key and want sub-second dictation latency.
- You want a tool you can pipe (`flowstate | jq`, `flowstate | pbcopy`, etc.)
  rather than a menu-bar app.
- You want the same dictation tool on every machine you SSH into.

You probably do *not* want flowstate if:

- You want a graphical settings panel — there isn't one. Config is TOML.
- You want a global system-wide hotkey daemon — push-to-talk only works while
  the CLI process owns the terminal.
- You want offline/local transcription — flowstate calls Groq's hosted API.
  Use [whisper.cpp](https://github.com/ggerganov/whisper.cpp) or Wispr Flow
  for that.

## Install

```sh
go install github.com/mazin-ahmed/flowstate/cmd/flowstate@latest
```

This downloads, compiles, and installs the `flowstate` binary to
`$(go env GOPATH)/bin`. Make sure that directory is on your `$PATH`.

flowstate is cgo-enabled (because the audio capture and keyboard-hook
dependencies are). You'll need a working C toolchain:

- **macOS** — Xcode command-line tools (`xcode-select --install`).
- **Linux** — `build-essential` (or your distro's equivalent), plus the dev
  headers for ALSA and X11 / libxkbcommon if you want push-to-talk.
- **Windows** — a recent MSVC or mingw-w64 toolchain.

### Per-platform prerequisites

| Feature                | macOS                              | Linux                                   | Windows                |
| ---------------------- | ---------------------------------- | --------------------------------------- | ---------------------- |
| Audio capture          | built-in                           | built-in (PulseAudio / PipeWire)        | built-in (WASAPI)      |
| `mute_while_recording` | `osascript` (built-in)             | `pactl` (PulseAudio) or `amixer` (ALSA) | built-in (Core Audio)  |
| `output_mode = paste`  | Accessibility permission for the terminal running flowstate | `wtype` (Wayland) or `xdotool` (X11)    | built-in (`SendInput`) |
| Push-to-talk           | Accessibility permission           | (X11/Wayland keyboard hook libs)        | built-in               |

On macOS, the first time you use `paste` or `push-to-talk`, the OS will
prompt you to add Accessibility permission for the terminal application
flowstate is running inside (Terminal, iTerm2, etc.) under **System
Settings → Privacy & Security → Accessibility**.

## Quickstart

1. Initialize the config file:

   ```sh
   flowstate config init
   ```

   This writes a commented default to
   `~/.config/flowstate/config.toml` on macOS/Linux or
   `%APPDATA%\flowstate\config.toml` on Windows. Use `flowstate config path`
   to see the exact location.

2. Get a Groq API key from [https://console.groq.com](https://console.groq.com)
   (the free tier is enough for personal dictation).

3. Export the key as an environment variable. flowstate reads it from
   `GROQ_API_KEY` and never stores it on disk:

   ```sh
   export GROQ_API_KEY=gsk_...
   ```

   `GROQ_API_TOKEN` is accepted as a fallback for compatibility with other
   Groq tooling, but `GROQ_API_KEY` is preferred. Add the `export` line to
   your shell rc (e.g. `~/.zshrc`, `~/.bashrc`) so it survives new shells.

4. Run it:

   ```sh
   flowstate
   ```

   The CLI prints `Recording…`, captures audio, and stops when you press
   **Enter**. After transcription and cleanup, the cleaned text is printed
   to stdout (and copied to your clipboard, by default).

## Config reference

Configuration lives in TOML at the path printed by `flowstate config path`.
Resolution order: `--config <path>` > `$FLOWSTATE_CONFIG` > the platform
default above.

The Groq API key is **not** a config field. It is read from the
`GROQ_API_KEY` environment variable (or `GROQ_API_TOKEN` as a fallback) at
runtime. Any subcommand that calls Groq will fail with a friendly message
naming both env vars if neither is set.

| Field                    | Default                                    | Meaning                                                                              |
| ------------------------ | ------------------------------------------ | ------------------------------------------------------------------------------------ |
| `trigger`                | `"enter"`                                  | How recording stops: `enter` (press Enter) or `push-to-talk` (hold a key).           |
| `ptt_key`                | `"space"`                                  | Key held in push-to-talk mode. Ignored when `trigger = "enter"`.                     |
| `base_url`               | `"https://api.groq.com/openai/v1"`         | Provider root. Override only for a self-hosted OpenAI-compatible proxy.              |
| `transcription_model`    | `"whisper-large-v3"`                       | Whisper model id passed to `/audio/transcriptions`.                                  |
| `cleanup_model`          | `"openai/gpt-oss-20b"`                     | LLM model id passed to `/chat/completions` for cleanup.                              |
| `cleanup_fallback_model` | `"meta-llama/llama-4-scout-17b-16e-instruct"` | Retried on HTTP 429 or empty primary response. Set equal to `cleanup_model` (or empty) to disable. |
| `language`               | `"en"`                                     | ISO-639-1 language hint. Set `""` to auto-detect, or `"fr"`/`"es"`/`"de"`/etc.       |
| `output_language`        | `""`                                       | Translation target for the cleanup pass. Empty = same as spoken. Non-empty adds an "Output ONLY in `<lang>`" directive to the cleanup system prompt. |
| `input_device`           | `""`                                       | Microphone UID or name. Empty = system default. List with `flowstate devices`.       |
| `mute_while_recording`   | `true`                                     | Mutes system audio output during the recording so playback doesn't bleed in.         |
| `output_mode`            | `"stdout,clipboard"`                       | Comma list of `stdout`, `clipboard`, `paste`, or the literal `"all"`.                |
| `preserve_clipboard_after_paste` | `true`                              | When `paste` is enabled, snapshot the current clipboard before pasting and restore it ~500ms later, unless you copied something else in the meantime. |
| `active_prompt`          | `"default"`                                | Which key under `[prompts]` to use as the system prompt for cleanup.                 |
| `custom_vocabulary`      | `""`                                       | Comma/newline/semicolon-separated terms preserved as high-priority spellings during cleanup. Multiline TOML strings are supported. |
| `colors`                 | `"auto"`                                   | Status-line colors: `"auto"` (TTY-detect), `"always"` (force on), `"never"` (force off). `--no-color` and `NO_COLOR=1` also disable. |
| `max_time_seconds`       | `0`                                        | Auto-stop recording after N seconds and process normally (exit 0). `0` disables (manual stop only). Override with `--max-time <seconds>`. |
| `paste_delay_seconds`    | `0`                                        | When `paste` is enabled, wait N seconds between writing the clipboard and firing the paste keystroke. Gives you time to focus the destination window. Override with `--paste-delay <seconds>`. |
| `[prompts]`              | three embedded prompts                     | Table of named cleanup prompts. See **Prompts** below.                               |

This table is a quick reference. The exhaustive spec, including validation
rules and the `--flag` overrides, lives in the project's Config Specification
doc.

## Trigger modes

### `enter` (default)

Capture starts as soon as `flowstate` is invoked and stops when you press
Enter in the terminal. This is the simplest mode and works on every host
without extra permissions.

### `push-to-talk`

Capture starts when you press and hold `ptt_key`, and stops when you
release it. The transcription + cleanup pipeline runs on release.

PTT uses a global keyboard hook (so the key works regardless of which window
has focus), which requires extra permissions:

- **macOS** — Accessibility permission for the terminal application, granted
  in **System Settings → Privacy & Security → Accessibility**. The first
  attempted use will prompt you.
- **Linux** — usually works out of the box on X11; Wayland support depends
  on the compositor.
- **Windows** — works out of the box.

## Status output

While a recording is in flight, flowstate prints staged status lines to
**stderr**:

- A `● Recording — press Enter to stop` prompt with a small live audio-level
  meter while you speak.
- `● Transcribing…` once the upload starts.
- `● Cleaning up…` while the LLM rewrite runs.
- `✓ Done. (N characters, 2.3s)` at the end.

Errors and soft warnings (mute failure, "no speech detected", config typos)
also go to stderr, prefixed with `✗` or `⚠`. **Nothing other than the
cleaned transcript is ever written to stdout**, so `flowstate | jq` and
similar pipelines stay clean.

Colors are emitted when stderr is a TTY. To disable them — for log capture,
CI runs, or terminals that don't render ANSI — use any of:

- The `--no-color` flag (highest priority).
- The `NO_COLOR=1` environment variable (per the [no-color.org](https://no-color.org)
  convention).
- `colors = "never"` in the config file.

To force colors on (e.g. inside a multiplexer that doesn't propagate the
TTY mode bit), set `colors = "always"` in the config. The default,
`colors = "auto"`, picks the right behavior based on whether stderr is a
TTY.

The audio-level meter is only rendered on the interactive (TTY) path. When
stderr is redirected, flowstate falls back to a single static
`Recording — press Enter to stop` line so log files don't fill up with
in-place redraws.

## Output modes

`output_mode` is a comma-separated list of destinations:

- `stdout` — print the cleaned text to standard output, followed by a
  newline. The status line ("Done. (N characters)") goes to stderr, so
  stdout stays pipeable.
- `clipboard` — copy the cleaned text to the system clipboard.
- `paste` — simulate `Cmd+V` (macOS) or `Ctrl+V` (Linux/Windows) into the
  currently focused application. Requires the per-platform paste tool noted
  in the prerequisites table.

The literal value `all` is shorthand for `stdout,clipboard,paste`. Order
within the list doesn't matter; flowstate always writes to all selected
destinations.

## Prompts

`[prompts]` is a table of named system prompts. flowstate ships three by
default:

- **`default`** — full FreeFlow-style cleanup. Removes filler words, fixes
  spelling and punctuation, preserves the speaker's intent, and is aware of
  developer syntax (code blocks, command names) so technical dictation
  doesn't get auto-corrected to gibberish.
- **`command`** — transform a highlighted piece of text per a spoken
  instruction (e.g. "make this shorter"). Reserved for a future edit-mode
  subcommand; currently ignored unless explicitly selected.
- **`literal`** — minimal cleanup, no context-awareness. Matches the simpler
  prompt in FreeFlow's README. Use this if the default prompt is rewriting
  more than you want.

Switch prompts per-run with `--prompt <name>`, or change `active_prompt` in
the config to make it sticky. The body of each prompt is embedded into the
default config at `flowstate config init` time, so you can edit them in
place and re-init won't clobber your changes (use `--force` to overwrite).

## Subcommands

- `flowstate` — record. The default command.
- `flowstate version` — print the build identifier and Go runtime version.
- `flowstate config init [--force] [--config <path>]` — write the default
  config to the resolved path. `--force` overwrites an existing file.
- `flowstate config path [--config <path>]` — print the resolved config path
  to stdout.
- `flowstate devices` — list input devices as `<UID>\t<Name>`. Useful for
  filling in `input_device`. Exits 1 with "no input devices found" on a
  headless host.

Top-level flags during recording:

- `--config <path>` — override the config file path.
- `--prompt <name>` — override `active_prompt`.
- `--output <list>` — override `output_mode`.
- `--no-mute` — disable `mute_while_recording` for this run.
- `--device <uid-or-name>` — override `input_device`.
- `--trigger <mode>` — override `trigger`.
- `--ptt-key <name>` — override `ptt_key`.
- `--model <name>` — override `cleanup_model`.
- `--no-color` — disable ANSI colors in status output (equivalent to
  `colors = "never"` in config or `NO_COLOR=1` in env).

Flags are sticky for the current invocation only; they never rewrite the
config file.

## Troubleshooting

**`Invalid API key for api.groq.com.`** — your Groq key is unset, expired,
or revoked. Generate a new one at
[console.groq.com](https://console.groq.com) and re-export it:
`export GROQ_API_KEY=gsk_...`.

**`Groq API key not found. Set GROQ_API_KEY (preferred) or GROQ_API_TOKEN
in your environment.`** — flowstate looked for both env vars and found
neither set. Export `GROQ_API_KEY` in the shell you launch flowstate from
(add the line to `~/.zshrc` or `~/.bashrc` so it persists).

**`no input devices found` on `flowstate devices`** — the OS has no
microphones registered. On macOS, check **System Settings → Privacy &
Security → Microphone** and grant the terminal app permission. On Linux,
verify `pactl list short sources` shows at least one source.

**Paste doesn't paste anything on macOS** — your terminal hasn't been
granted Accessibility permission. Toggle it off and back on under **System
Settings → Privacy & Security → Accessibility**, then re-run flowstate. The
permission applies to the terminal application, not the flowstate binary
itself.

**`Audio too large (HTTP 413). Try a shorter recording.`** — Groq's
transcription endpoint caps uploads at 25 MB. At PCM16 mono 16 kHz that's
about 13 minutes of audio. Stop and restart for long-form dictation.

**`Rate limited (HTTP 429). Wait a moment and retry.`** — Groq's free tier
has rate limits. flowstate automatically retries the cleanup pass with
`cleanup_fallback_model`, but the transcription pass surfaces 429 directly.

## What's NOT in v1

- No GUI, menu bar, or system tray app.
- No background daemon — flowstate is a one-shot CLI invocation per
  recording.
- No realtime streaming transcription. Record → upload → transcribe → clean
  up. Each clip is a fresh HTTP round-trip.
- No app-context scraping (FreeFlow reads nearby app windows to bias
  cleanup; flowstate sends an empty context summary).
- No "Edit Mode" subcommand against highlighted text. The `command` prompt
  is shipped so a future revision can pipe selected text in via stdin, but
  the wiring is out of scope for v1.
- No OS-keychain integration. The Groq API key is read from the
  `GROQ_API_KEY` environment variable; persisting it across shells is the
  user's responsibility (e.g. via shell rc files or a tool like `direnv`).

## License

MIT, see `LICENSE`.

## Credits

Inspired by [FreeFlow](https://github.com/zachlatta/freeflow) (Swift, macOS)
by Zach Latta, which provides the wire-level reference for the Groq pipeline,
and by [Wispr Flow](https://wisprflow.ai), which set the bar for what
voice-driven dictation should feel like.
