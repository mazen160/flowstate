// Package e2e contains the end-to-end smoke test for the flowstate binary.
//
// The test in flowstate_e2e_test.go has two halves:
//
//  1. A "CLI surface" half that builds the binary with `go build` and exercises
//     the user-visible subcommands (`version`, `--help`, `config init`,
//     `config path`, `devices`). This verifies the process boots, parses
//     flags, resolves the config path, and writes a default config without
//     hitting the network.
//
//  2. A "wire-level" half that imports internal/transcribe and internal/cleanup
//     directly and runs them against an httptest.NewServer that fakes Groq's
//     /audio/transcriptions and /chat/completions endpoints. This verifies the
//     real HTTP-handling code paths against canned responses without needing a
//     microphone or a live API key.
//
// The full audio-capture → transcribe → cleanup → output pipeline is NOT
// exercised end-to-end by this package, because doing so would require either
// driving real audio capture from a test (impractical on CI) or adding a
// hidden CLI flag to the production binary (out of scope for M10). Manual
// smoke against a real microphone is documented in the README.
package e2e
