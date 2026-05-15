# Releasing flowstate

This is the release runbook for flowstate. It describes how to cut a new
versioned release end-to-end, the rationale behind the build strategy, and
known future directions if the release surface grows.

flowstate uses **Strategy A** for its release pipeline: per-OS native CI
runners that each build their own binary with `CGO_ENABLED=1`, plus a final
aggregator job that uploads every platform's archive to a single GitHub
release via `gh release` / `softprops/action-gh-release`. The
`.goreleaser.yaml` at the repo root is a host-only placeholder kept for
parity and to keep a future Strategy B upgrade one step away — it is **not**
invoked by CI.

## Why per-OS runners (the cgo rationale)

flowstate links three cgo libraries that each wrap platform-native
audio / input / clipboard APIs:

- `github.com/gen2brain/malgo` — wraps miniaudio (CoreAudio on macOS,
  ALSA on Linux, WASAPI on Windows).
- `github.com/robotn/gohook` — wraps libuiohook (X11/Wayland on Linux,
  native APIs on macOS and Windows).
- `golang.design/x/clipboard` — uses platform-native clipboard APIs on
  each OS.

Because every target needs its own native toolchain and platform SDK, the
"single-host cross-compile from one Linux runner" model that works for pure
`CGO_ENABLED=0` Go projects does **not** apply here. The simplest honest
answer is to give each OS its own runner and build natively. That is
Strategy A.

Concretely, this means:

- `CGO_ENABLED=1` everywhere — flowstate cannot ship as a static Go binary.
- No cross-compile in CI. Linux artifacts are built on `ubuntu-latest`,
  macOS artifacts on `macos-latest`, Windows artifacts on `windows-latest`.
- `goreleaser`'s cross-build feature is unused. The `.goreleaser.yaml` is
  configured with `goos: "{{ .Runtime.Goos }}"` and
  `goarch: "{{ .Runtime.Goarch }}"` so that, if anyone runs it locally, it
  only builds for the host OS / host architecture. Running
  `goreleaser release --snapshot --clean` on, e.g., an Intel macOS host
  produces a single `dist/flowstate_<version>_darwin_amd64.tar.gz` plus
  `dist/checksums.txt`.

## Cutting a release

The release flow is tag-driven. All you do locally is update the changelog,
commit, and push a tag. CI does the rest.

### 1. Update `CHANGELOG.md`

Move the `## [Unreleased]` items into a new versioned section. Keep the
keep-a-changelog format. Example:

```markdown
## [Unreleased]

## [0.2.0] — 2026-06-01
### Added
- New `--foo` flag.

### Fixed
- Bug in mute on Wayland.
```

The top of the file (heading + intro paragraph + `## [Unreleased]`) stays
in place; new items get inserted under a new `## [vX.Y.Z]` heading dated
with the release day.

### 2. Commit and tag

```bash
git add CHANGELOG.md
git commit -m "release: vX.Y.Z"
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin main
git push origin vX.Y.Z
```

The tag push is the event that fires the release pipeline.

### 3. CI takes over

Pushing a `v*` tag triggers `.github/workflows/release.yml` (added in M15).
That workflow runs a 3-OS matrix:

- `ubuntu-latest` — installs the Linux apt prerequisites (ALSA + X11
  headers + `pulseaudio-utils`), runs `make build`, archives as
  `flowstate_<tag>_linux_amd64.tar.gz`.
- `macos-latest` — runs `make build`, archives as
  `flowstate_<tag>_darwin_<arch>.tar.gz`.
- `windows-latest` — runs `make build`, archives as
  `flowstate_<tag>_windows_amd64.zip`.

Each runner also produces a per-archive SHA-256 checksum (`shasum -a 256`
on Linux/macOS, `Get-FileHash` on Windows). Every matrix job uploads its
archive + checksum as a GitHub Actions artifact.

A final aggregator job (`needs: [build]`) downloads all artifacts and
creates the GitHub release in one shot via
`softprops/action-gh-release@v2`, using `CHANGELOG.md` for release notes
and marking the release as `draft: true`. The draft is what gives you a
chance to manually smoke-test before publishing.

### 4. Manual smoke per platform

Before flipping the draft release to "published", download each archive
from the draft release page and verify on the matching platform:

```bash
# Linux / macOS
tar -xzf flowstate_vX.Y.Z_<os>_<arch>.tar.gz
./flowstate version          # should print vX.Y.Z
./flowstate config init      # should write the default config file
```

```powershell
# Windows
Expand-Archive flowstate_vX.Y.Z_windows_amd64.zip
.\flowstate.exe version
.\flowstate.exe config init
```

On macOS the first run will trip Gatekeeper. The published workaround is:

```bash
xattr -d com.apple.quarantine ./flowstate
```

That note also lives in `README.md` under Troubleshooting.

### 5. Publish

Once all three platforms smoke-test cleanly, go to the GitHub release page
for the draft and click **Publish release**. Done.

If a smoke test fails:

1. Delete the draft release on GitHub.
2. Delete the tag locally and on the remote:
   ```bash
   git tag -d vX.Y.Z
   git push origin :refs/tags/vX.Y.Z
   ```
3. Fix the issue, bump the patch version in the changelog, and re-tag.

## The `.goreleaser.yaml` placeholder

The committed `.goreleaser.yaml` is **not** part of the CI release flow.
It exists because:

1. It documents intent — anyone reading the repo can see how flowstate
   *would* be released under Strategy B.
2. `goreleaser check` validates it, so the YAML is guaranteed to be
   structurally correct.
3. The upgrade to Strategy B (using `goreleaser release` under CI instead
   of raw `tar` + `gh release`) is then a one-step migration: swap the
   workflow's archive logic for a `goreleaser release --skip=publish`
   call per OS, then download + `goreleaser continue` on the aggregator
   job. No config rewrite needed.

### Host-only invocation

The original M11 spec showed `goos: "{{ .Runtime.Goos }}"` / `goarch:
"{{ .Runtime.Goarch }}"` to express "host-only". GoReleaser does **not**
accept templates in the `goos` / `goarch` enum fields
(see [`goreleaser#796`](https://github.com/goreleaser/goreleaser/issues/796),
closed `wontfix`). The committed YAML therefore lists the full supported
matrix (`linux`/`darwin`/`windows` × `amd64`/`arm64`, with
`windows/arm64` ignored).

To get the host-only behavior locally, invoke goreleaser with
`--single-target`:

```bash
# Build host-only binary (no archive, no checksum file).
goreleaser build --single-target --snapshot --clean
ls dist/
# flowstate_<host-os>_<host-arch>/flowstate
```

`goreleaser release --snapshot --clean` is **not** host-only — it tries
to cross-compile every matrix entry, which fails for flowstate under
`CGO_ENABLED=1` (linux and windows cgo cross-compile from macOS would
need a Linux/Windows C toolchain). Reserve `release --snapshot` for
testing a single matrix entry on its own native runner.

`dist/` is gitignored, so leftover snapshots are safe to leave on disk.

## Known future direction

If the release surface ever needs to grow — for example, shipping `arm64`
Linux binaries from `x86_64` runners — the path forward is one of:

1. **Switch to Strategy B with `goreleaser-cross`**: the
   `ghcr.io/goreleaser/goreleaser-cross` Docker image ships
   cross-compilers (including `zig-cc`) for cgo on Linux and Windows. The
   `.goreleaser.yaml` would drop the `{{ .Runtime.* }}` templates and list
   real `goos` / `goarch` matrices. macOS targets would still need a real
   macOS runner because they require the macOS SDK regardless.
2. **Stay on Strategy A and just add runners**: GitHub-hosted runners
   already include `ubuntu-24.04-arm` and `macos-14` (Apple Silicon).
   Extending the matrix is cheaper conceptually than introducing
   `goreleaser-cross`, and stays honest about the cgo constraint.

For v0.1.0 neither is necessary: Linux `amd64` + macOS (host arch) +
Windows `amd64` is the full advertised surface.

## Related files

- `.goreleaser.yaml` — the host-only placeholder described above.
- `.github/workflows/release.yml` — the actual tag-triggered release
  pipeline (added in M15).
- `Makefile` — the `build` target invoked by the release workflow
  (added in M13).
- `CHANGELOG.md` — the source of release notes.
