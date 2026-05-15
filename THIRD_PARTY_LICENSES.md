# Third-party licenses

flowstate is released under the MIT license (see [`LICENSE`](./LICENSE)). The
distributed binaries also link against the third-party Go modules listed
below, each under its own permissive license. Every listed license permits
redistribution as part of an MIT-licensed binary; this file exists so users
who need a license inventory for their own distribution have one in one place.

Run `go mod download -json` or `go list -m -json all` against the repo for
the exact version pins; the table below uses the versions in `go.mod` at
the time of writing.

## Direct dependencies

| Module | License | Purpose |
|---|---|---|
| `github.com/BurntSushi/toml v1.4.0` | MIT | TOML parser used by `internal/config`. |
| `github.com/gen2brain/malgo v0.11.25` | Unlicense (public domain dedication) | cgo bindings around miniaudio for cross-platform microphone capture. |
| `github.com/robotn/gohook v0.42.3` | MIT | cgo bindings around libuiohook for the push-to-talk global keyboard hook. |
| `golang.design/x/clipboard v0.7.1` | MIT | Cross-platform clipboard read/write. |

## Indirect dependencies (transitive)

These are pulled in by the direct deps above; full version pins are in
`go.sum`. Listing the license headers here so a redistribution audit
doesn't need to walk the module cache.

| Module | License | Pulled in by |
|---|---|---|
| `github.com/vcaesar/keycode` | MIT | `robotn/gohook` |
| `github.com/vcaesar/tt` | MIT | `robotn/gohook` |
| `github.com/jezek/xgb` | BSD-3-Clause | `golang.design/x/clipboard` (Linux/X11) |
| `github.com/go-gl/glfw/v3.3/glfw` | BSD-3-Clause | `golang.design/x/clipboard` (X11/Wayland integration) |
| `dmitri.shuralyov.com/gpu/mtl` | BSD-3-Clause | `golang.design/x/clipboard` (Metal bindings on darwin) |
| `golang.org/x/exp/shiny` | BSD-3-Clause | `golang.design/x/clipboard` |
| `golang.org/x/image` | BSD-3-Clause | `golang.design/x/clipboard` |
| `golang.org/x/mobile` | BSD-3-Clause | `golang.design/x/clipboard` |
| `golang.org/x/mod` | BSD-3-Clause | toolchain transitive |
| `golang.org/x/sync` | BSD-3-Clause | toolchain transitive |
| `golang.org/x/sys` | BSD-3-Clause | toolchain transitive |
| `golang.org/x/text` | BSD-3-Clause | toolchain transitive |
| `golang.org/x/tools` | BSD-3-Clause | toolchain transitive |

## Embedded prompts

`internal/prompts/{default,command,literal}.txt` are byte-for-byte ports of
prompt strings from [FreeFlow](https://github.com/zachlatta/freeflow), which
is also MIT-licensed. The port is attributed in the README's "Credits"
section and in code comments above each prompt.

## Reproducing this list

```sh
go list -m -json all | jq -r '"\(.Path) @ \(.Version) — \(.GoVersion // "n/a")"'
```

For per-module license texts, point a tool like
[`google/go-licenses`](https://github.com/google/go-licenses) at the binary:

```sh
go-licenses report ./cmd/flowstate > licenses.csv
go-licenses save   ./cmd/flowstate --save_path=./licenses
```

This table is updated whenever `go.mod` adds a new direct dependency. The
indirect column is best-effort and may lag a `go mod tidy` by one release.
