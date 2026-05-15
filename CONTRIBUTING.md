# Contributing to flowstate

Thanks for your interest in improving flowstate. This document explains how to
set up a development environment, run the test suite, and submit changes.

## Development environment

flowstate requires **Go 1.24 or newer**.

It links three cgo libraries (`malgo`, `gohook`, `golang.design/x/clipboard`),
so a C toolchain is required on every platform. CGO is enabled by default.

### Linux prerequisites

On Debian/Ubuntu-based distributions install the system headers before
building:

```
sudo apt-get update
sudo apt-get install -y \
    libasound2-dev \
    libx11-dev libxkbcommon-dev libxtst-dev libxinerama-dev libxrandr-dev \
    pulseaudio-utils alsa-utils
```

- `libasound2-dev` provides the ALSA headers used by `malgo`.
- The five `libx*` packages are needed by `gohook` (libuiohook). All five are
  required — missing one produces an opaque cgo linker error.
- `pulseaudio-utils` installs `pactl`, used by the mute package at runtime.
- `alsa-utils` installs `amixer` (a fallback mute backend).

For Wayland-only environments add `wayland-protocols` and `wtype`.

### macOS prerequisites

None beyond Xcode command-line tools (`xcode-select --install`). Everything
malgo, gohook, paste, and mute need is preinstalled.

### Windows prerequisites

None beyond a recent Go toolchain. The MSVC or MinGW C toolchain that ships
with the Go installer is sufficient.

## Building and testing

Common workflows are wrapped in the `Makefile`:

```
make build         # build ./flowstate
make test          # run the unit suite
make test-race     # unit suite with -race
make test-e2e      # end-to-end suite (slower, hits the audio + paste stack)
make vet           # go vet ./...
make lint          # golangci-lint run (if installed)
make cover         # generate coverage.out and an HTML report
```

`make test` is the gate every pull request must pass.

## Pull-request flow

1. Fork the repository and create a topic branch from `main`.
2. Make focused changes — one logical change per PR. Keep diffs small.
3. Use clear commit messages. A loose `<area>: <imperative summary>` style
   (for example `mute: handle pactl missing on minimal images`) is preferred.
4. Run `make test` and `make vet` locally before pushing.
5. Open a PR against `main`. Fill out the PR template (summary, changes,
   testing, checklist).
6. Add a `## [Unreleased]` entry to `CHANGELOG.md` for any user-visible change.

## Adding a new prompt

Embedded prompts live under `internal/prompts/`:

1. Add a new `<name>.txt` file with the prompt body.
2. Register the name in `internal/prompts/prompts.go` (`Names()` and the
   embed FS).
3. Add a unit test under `internal/prompts/` that asserts the prompt loads
   and is non-empty.

## Where the design lives

The user-facing surface is documented in `README.md`. The internal design,
milestone plans, and per-feature specs live in the project's local
[`.backlog/`](./.backlog) workspace — open it with the `backlog` CLI or read
the docs directly. Each milestone (M0..M16) is captured as a backlog task with
linked design docs that explain the rationale behind the current code.

Release process is documented in `docs/RELEASING.md`.

## Reporting bugs and requesting features

Use the GitHub issue templates under `.github/ISSUE_TEMPLATE/` so we have the
information needed to reproduce.
