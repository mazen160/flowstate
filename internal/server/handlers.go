package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/mazen160/flowstate/internal/audio"
	"github.com/mazen160/flowstate/internal/recording"
)

// transcribeResponse is the JSON shape /api/transcribe returns on success.
// Both fields can be the empty string: an empty raw means the audio was
// silent (or hit the hallucination filter); an empty cleaned means cleanup
// returned its EMPTY sentinel. The frontend handles both cases by simply
// showing what it got and skipping rendering when both are empty.
type transcribeResponse struct {
	Raw              string `json:"raw"`
	Cleaned          string `json:"cleaned"`
	DurationMS       int64  `json:"duration_ms"`
	RecordingDir     string `json:"recording_dir"`
	RemovedSilenceMS int64  `json:"removed_silence_ms"`
}

// healthResponse is the shape of /api/health.
type healthResponse struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
}

// infoResponse is the shape of /api/info. The frontend reads this once on
// page load to decide whether to surface the "Token" input in the settings
// drawer (auth_required=true => surface; false => hide).
type infoResponse struct {
	Version        string `json:"version"`
	AuthRequired   bool   `json:"auth_required"`
	TrimSilence    bool   `json:"trim_silence"`
	SaveRecordings bool   `json:"save_recordings"`
}

// errorResponse is the shape returned for every non-2xx JSON path.
type errorResponse struct {
	Error string `json:"error"`
}

// writeJSON writes the given status + JSON payload, setting the
// Content-Type appropriately. Any encoding failure is logged via the
// fallback http.Error path because at that point the headers have likely
// been written already.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The response is half-written at this point — best we can do
		// is bail. Silently swallow; the alternative is a panic that
		// kills the whole server.
		_ = err
	}
}

// writeJSONError is the standard error path: serializes {"error": msg}
// at the given status code. Used by the auth middleware and every
// non-success branch of the handlers below.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// handlePing is an authenticated liveness check the login screen uses to
// validate a Bearer token before persisting it in localStorage. Returns
// {"ok": true} when the token is accepted; the auth middleware handles 401.
// When Token is empty (no auth configured), the middleware is a no-op and
// ping returns 200 for any caller — the login gate is never shown in that
// case so this is harmless.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{OK: true})
}

// handleHealth returns 200 + {"ok": true, "version": "..."} — a tiny probe
// for liveness checks and curl smoke tests.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		OK:      true,
		Version: s.opts.Version,
	})
}

// handleInfo exposes a couple of read-only facts the frontend needs to
// render correctly: the build version (for the footer) and whether auth is
// configured (so the settings drawer can show or hide the token input).
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, infoResponse{
		Version:        s.opts.Version,
		AuthRequired:   s.opts.Token != "",
		TrimSilence:    s.opts.TrimSilence,
		SaveRecordings: !s.opts.DisableStorage,
	})
}

// handleTranscribe is the workhorse: it parses an audio multipart upload,
// runs the existing transcribe + cleanup pipeline against the Groq clients
// the server was constructed with, and returns {"raw","cleaned","duration_ms"}.
//
// Status code map:
//   - 200 — success. Both raw and cleaned may be "" (silent / EMPTY).
//   - 400 — request was not multipart/form-data or "audio" part missing.
//   - 405 — non-POST.
//   - 413 — upload exceeded maxUploadBytes.
//   - 415 — "audio" form field present but not a file.
//   - 500 — Groq error. The error message is propagated but stripped of
//     any wrapping that might leak internal context.
//
// Original and processed audio are saved before contacting the provider.
// The raw transcript is saved before cleanup so it survives cleanup errors.
func (s *Server) handleTranscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.opts.Transcribe == nil || s.opts.Cleanup == nil {
		writeJSONError(w, http.StatusInternalServerError, "server not configured")
		return
	}

	t0 := time.Now()

	defer removeMultipart(r)
	upload := s.prepareUpload(w, r)
	if upload == nil {
		return
	}
	saved := upload.saved
	out := transcribeResponse{RecordingDir: saved.Dir, RemovedSilenceMS: upload.removed.Milliseconds()}
	if upload.empty {
		if err := saved.SaveRaw(""); err != nil {
			s.storageError(w, err)
			return
		}
		if err := saved.SaveCleaned(""); err != nil {
			s.storageError(w, err)
			return
		}
		out.DurationMS = time.Since(t0).Milliseconds()
		writeJSON(w, http.StatusOK, out)
		return
	}

	// 1. Transcribe. The Groq client owns its own timeout (20s); we
	// just hand it the request context so a client disconnect bubbles
	// through. The transcribe package returns user-friendly mapped
	// messages for HTTP errors (401/403/413/429/5xx); for everything
	// else (network failures, JSON parse, etc.) the raw err.Error()
	// can contain internal URLs or stack-like details — keep those off
	// the wire and surface them only on the server's stderr log.
	raw, err := s.opts.Transcribe.TranscribeReader(r.Context(), bytes.NewReader(upload.data), upload.filename)
	if err != nil {
		fmt.Fprintf(s.opts.Stderr, "flowstate: transcribe error: %v\n", err)
		writeJSONError(w, http.StatusInternalServerError, sanitizeUpstreamError(err))
		return
	}
	if err := saved.SaveRaw(raw); err != nil {
		s.storageError(w, err)
		return
	}
	out.Raw = raw

	// Empty raw transcript is a documented outcome (silence or
	// hallucination filter) — we return 200 with both fields empty so
	// the frontend can render a "(no speech detected)" notice without
	// special-casing a separate status code.
	if raw == "" {
		if err := saved.SaveCleaned(""); err != nil {
			s.storageError(w, err)
			return
		}
		out.DurationMS = time.Since(t0).Milliseconds()
		writeJSON(w, http.StatusOK, out)
		return
	}

	// 2. Cleanup. Same context plumbing as transcribe. We let the
	// cleanup package's friendly mapHTTPError messages bubble out
	// verbatim.
	cleaned, err := s.opts.Cleanup.Clean(r.Context(),
		s.opts.SystemPrompt, raw, s.opts.ContextSummary)
	if err != nil {
		fmt.Fprintf(s.opts.Stderr, "flowstate: cleanup error: %v\n", err)
		writeJSONError(w, http.StatusInternalServerError, sanitizeUpstreamError(err))
		return
	}

	if err := saved.SaveCleaned(cleaned); err != nil {
		s.storageError(w, err)
		return
	}
	out.Cleaned = cleaned
	out.DurationMS = time.Since(t0).Milliseconds()
	writeJSON(w, http.StatusOK, out)
}

// handleRecord archives a mic clip without sending it to any AI provider.
// []byte is encoded as base64 for the browser's existing audio player.
func (s *Server) handleRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	defer removeMultipart(r)
	upload := s.prepareUpload(w, r)
	if upload == nil {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		RecordingDir     string `json:"recording_dir"`
		Audio            []byte `json:"audio"`
		Ext              string `json:"ext"`
		RemovedSilenceMS int64  `json:"removed_silence_ms"`
	}{upload.saved.Dir, upload.data, strings.TrimPrefix(filepath.Ext(upload.filename), "."), upload.removed.Milliseconds()})
}

func removeMultipart(r *http.Request) {
	if r.MultipartForm != nil {
		_ = r.MultipartForm.RemoveAll()
	}
}

func (s *Server) storageError(w http.ResponseWriter, err error) {
	fmt.Fprintf(s.opts.Stderr, "flowstate: recording storage error: %v\n", err)
	writeJSONError(w, http.StatusInternalServerError, "could not save recording; check the server log for details")
}

type preparedRecording struct {
	saved    *recording.Recording
	data     []byte
	filename string
	removed  time.Duration
	empty    bool
}

// prepareUpload stores the exact original, then prepares the audio used by
// both transcription and Mic Record. Browsers also send their decoded WAV
// as "audio", retaining the MediaRecorder container in "original".
func (s *Server) prepareUpload(w http.ResponseWriter, r *http.Request) *preparedRecording {
	fail := func(status int, msg string) *preparedRecording {
		writeJSONError(w, status, msg)
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxUploadBytes))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		if isMaxBytesError(err) {
			return fail(http.StatusRequestEntityTooLarge, "audio upload exceeded the 25 MiB limit")
		}
		return fail(http.StatusBadRequest, "invalid multipart form")
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		return fail(http.StatusUnsupportedMediaType, "missing 'audio' form field")
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return fail(http.StatusBadRequest, "could not read audio")
	}
	filename := filepath.Base(header.Filename)
	original, originalName := data, filename
	if originalFile, originalHeader, err := r.FormFile("original"); err == nil {
		defer originalFile.Close()
		original, err = io.ReadAll(originalFile)
		if err != nil {
			return fail(http.StatusBadRequest, "could not read original audio")
		}
		originalName = originalHeader.Filename
	}
	trim := s.opts.TrimSilence
	switch r.FormValue("trim_silence") {
	case "":
	case "true":
		trim = true
	case "false":
		trim = false
	default:
		return fail(http.StatusBadRequest, "trim_silence must be true or false")
	}
	store := !s.opts.DisableStorage
	switch r.FormValue("save_recordings") {
	case "":
	case "true":
		store = true
	case "false":
		store = false
	default:
		return fail(http.StatusBadRequest, "save_recordings must be true or false")
	}
	saved := recording.NewInMemory()
	if store {
		saved, err = recording.New(s.opts.StorageDir, originalName, original)
		if err != nil {
			s.storageError(w, err)
			return nil
		}
	}
	processed, err := saved.Prepare(filename, data, trim)
	if err != nil {
		if errors.Is(err, audio.ErrInvalidWAV) {
			msg := "silence removal requires PCM16 WAV (or set trim_silence=false)."
			if store {
				msg += " Original audio was saved."
			}
			return fail(http.StatusBadRequest, msg)
		}
		s.storageError(w, err)
		return nil
	}
	return &preparedRecording{saved: saved, data: processed.Data, filename: processed.Filename, removed: processed.Removed, empty: processed.Empty}
}

// isMaxBytesError sniffs whether err originated from http.MaxBytesReader's
// "request body too large" path. The stdlib's *http.MaxBytesError is the
// canonical type since Go 1.19; older error strings are accepted as a
// belt-and-braces guard so we still surface a sane 413 on slightly older
// toolchains.
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

// sanitizeUpstreamError filters an upstream error into a message safe to
// return to an unauthenticated client. Internal/transcribe and
// internal/cleanup both produce user-friendly mapped messages for
// HTTP-level failures (401/403/404/413/429/5xx) which we want to
// pass through verbatim — but their wrappers can also surface raw URLs,
// transport errors, or JSON-decode complaints that leak the upstream
// host or path. The full error is always logged via the calling
// handler's stderr; this helper just decides what the *client* sees.
//
// Today the policy is: surface the friendly mapped messages verbatim,
// and substitute a generic "transcription failed; please retry" for
// everything else. The detection is heuristic (string substring match
// on the canonical phrases) but accurate against the current
// internal/transcribe and internal/cleanup messages — if those evolve,
// either keep the phrases in sync or graduate to a typed-error API.
func sanitizeUpstreamError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Phrases produced by the internal/{transcribe,cleanup}/errors.go
	// mapHTTPError tables. Any one of these matches makes the message
	// safe to surface — the upstream host name is intentionally part
	// of these strings, e.g. "Invalid API key for api.groq.com".
	safePhrases := []string{
		"Invalid API key for",
		"lacks permission",
		"Endpoint not found",
		"Audio too large",
		"Rate limited",
		"Provider error at",
		"Request failed at",
	}
	for _, p := range safePhrases {
		if strings.Contains(msg, p) {
			return msg
		}
	}
	return "upstream request failed; check the server log for details"
}
