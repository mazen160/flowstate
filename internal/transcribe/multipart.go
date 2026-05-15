// Package transcribe — multipart request body builder.
//
// Groq's /audio/transcriptions endpoint is OpenAI-compatible and expects
// multipart/form-data with a specific field order: model, response_format,
// (optional) language, file. We use a freshly-generated UUID-style boundary
// per request and write the parts hand-crafted (rather than via
// mime/multipart.Writer's default field order, which is just whatever order
// the caller invokes CreateFormField in but using our own writer keeps the
// header layout explicit and easy to audit against the FreeFlow port).
//
// The body is materialized in memory because the recordings are short (a
// few hundred KB at most for normal dictation) and net/http requires either
// a known length or a known body type; an in-memory []byte gives us both.
package transcribe

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// transcriptionContentType is the value we send for the file part's
// Content-Type header. The spec pins this to audio/wav because Flowstate
// only ever uploads PCM16 mono 16 kHz WAVs.
const transcriptionContentType = "audio/wav"

// buildMultipartBody assembles the request body for a transcription request.
// It returns the body bytes, the boundary string (for the Content-Type
// header on the outer request), and any error encountered reading wavPath.
//
// Field order is: model, response_format=verbose_json, [language,] file.
// The language part is included only when language is non-empty. Each part
// uses CRLF line endings as required by RFC 2046.
func buildMultipartBody(wavPath, model, language string) (body []byte, boundary string, err error) {
	boundary, err = newBoundary()
	if err != nil {
		return nil, "", fmt.Errorf("generate boundary: %w", err)
	}

	audioBytes, err := os.ReadFile(wavPath)
	if err != nil {
		return nil, "", fmt.Errorf("read wav file: %w", err)
	}

	fileName := filepath.Base(wavPath)

	// Build in a bytes-friendly way using a sequence of writes. We size
	// the slice generously then let append grow it as needed; the cost of
	// a few reallocations is dwarfed by the network round-trip.
	buf := make([]byte, 0, len(audioBytes)+512)

	writePart := func(name, value string) {
		buf = append(buf, "--"...)
		buf = append(buf, boundary...)
		buf = append(buf, "\r\n"...)
		buf = append(buf, fmt.Sprintf("Content-Disposition: form-data; name=%q\r\n\r\n", name)...)
		buf = append(buf, value...)
		buf = append(buf, "\r\n"...)
	}

	writePart("model", model)
	writePart("response_format", "verbose_json")
	if language != "" {
		writePart("language", language)
	}

	// File part has additional headers (filename + Content-Type).
	buf = append(buf, "--"...)
	buf = append(buf, boundary...)
	buf = append(buf, "\r\n"...)
	buf = append(buf, fmt.Sprintf("Content-Disposition: form-data; name=%q; filename=%q\r\n", "file", fileName)...)
	buf = append(buf, fmt.Sprintf("Content-Type: %s\r\n\r\n", transcriptionContentType)...)
	buf = append(buf, audioBytes...)
	buf = append(buf, "\r\n"...)

	// Closing boundary.
	buf = append(buf, "--"...)
	buf = append(buf, boundary...)
	buf = append(buf, "--\r\n"...)

	return buf, boundary, nil
}

// newBoundary returns a freshly-generated UUIDv4-style string suitable for
// use as a multipart boundary. We don't depend on a UUID library — 16 random
// bytes formatted as 8-4-4-4-12 hex give the same uniqueness guarantees we
// need (the boundary just has to not appear in the body, which 122 bits of
// entropy makes statistically impossible).
func newBoundary() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	// Set the version (4) and variant bits to match RFC 4122 §4.4. This is
	// cosmetic — any 16 random bytes would do — but it makes the boundary
	// indistinguishable from a real UUIDv4 if anyone logs it.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
