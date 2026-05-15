package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webFS embeds the static frontend (index.html, app.js, style.css) so the
// `flowstate` binary remains a single file with no external runtime
// dependencies. The web/ directory is bundled at compile time.
//
//go:embed web
var webFS embed.FS

// handleIndex serves the SPA shell (index.html) on "/" and 404s anything
// else that fell through to the catch-all root. Static assets live under
// /static/* and are dispatched separately so this handler is purely the
// landing-page logic.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		// Any non-root path that wasn't matched by another route is a
		// 404. Keeps a misspelled /api typo from surfacing the HTML
		// shell as a "successful" response.
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	data, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(data)
}

// staticHandler returns an http.Handler that serves files from the
// embedded "web" subtree, picking Content-Types based on extension. We
// don't fall back to http.FileServer's default sniff for two reasons:
//
//  1. The embed FS has only three files; an explicit map is simpler and
//     surfaces an unrecognized extension as a defensive failure rather
//     than a misleading "text/plain" content-type.
//  2. We want short, predictable cache headers — http.FileServer doesn't
//     emit Cache-Control on its own.
func (s *Server) staticHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject path traversal attempts before touching the FS. embed
		// already disallows "..", but rejecting early gives a cleaner
		// 404 than the EBADF-style error otherwise.
		clean := path.Clean("/" + r.URL.Path)[1:]
		if strings.Contains(clean, "..") || strings.HasPrefix(clean, "/") {
			http.NotFound(w, r)
			return
		}
		full := "web/" + clean
		data, err := fs.ReadFile(webFS, full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentTypeForAsset(clean))
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
}

// contentTypeForAsset maps a known asset suffix to its MIME type. Only the
// three suffixes the embedded SPA ships with are explicit; the fallback is
// application/octet-stream so an unrecognized embed surfaces as "binary"
// rather than being silently rendered as HTML.
func contentTypeForAsset(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".json"):
		return "application/json"
	case strings.HasSuffix(name, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(name, ".png"):
		return "image/png"
	default:
		return "application/octet-stream"
	}
}
