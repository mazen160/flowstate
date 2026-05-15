package server

import (
	"crypto/subtle"
	"net/http"
)

// authMiddleware wraps the given handler with Bearer-token authentication
// when Options.Token is non-empty. When Token is empty, the middleware is
// a no-op pass-through — every request reaches the inner handler.
//
// Mismatched or missing Authorization headers receive a 401 with the
// canonical JSON error shape. The comparison uses
// crypto/subtle.ConstantTimeCompare so an attacker cannot infer the
// token byte-by-byte by timing 401 responses. Strings of unequal
// length compare as not-equal before reaching the constant-time path
// (cheap mismatch shortcut); after that point, the comparison cost is
// proportional to len(want) regardless of how close `got` is to a
// match.
//
// Public/non-gated routes (currently /api/health and /api/info) wire
// straight to the mux without this middleware so the frontend can
// pre-detect `auth_required` before it has the token.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Token == "" {
			next.ServeHTTP(w, r)
			return
		}

		got := r.Header.Get("Authorization")
		want := "Bearer " + s.opts.Token
		if !constantTimeEqualString(got, want) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// constantTimeEqualString is a string-typed wrapper around
// crypto/subtle.ConstantTimeCompare. Unequal lengths short-circuit to
// false BEFORE comparing bytes — that exposes length, which is fine
// since len(want) is fixed by the server's configured token and not a
// secret. The byte comparison itself takes time proportional to len(a)
// regardless of where the first mismatch occurs.
func constantTimeEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
