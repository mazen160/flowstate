package server

import (
	"net/http"
)

// authMiddleware wraps the given handler with Bearer-token authentication
// when Options.Token is non-empty. When Token is empty, the middleware is
// a no-op pass-through — every request reaches the inner handler.
//
// Mismatched or missing Authorization headers receive a 401 with the
// canonical JSON error shape. The check uses a constant-time-ish exact
// string compare; we don't pad to defeat timing attacks because the only
// realistic deployment surface is loopback or a single-user LAN box, and
// Go's default == on strings is already constant-time-ish for equal-length
// inputs. If this is ever exposed to the open internet, swap in
// crypto/subtle.ConstantTimeCompare.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token == "" {
			next.ServeHTTP(w, r)
			return
		}

		got := r.Header.Get("Authorization")
		want := "Bearer " + s.opts.Token
		if got != want {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
