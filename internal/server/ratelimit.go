package server

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate-limit parameters for /api/transcribe. A token bucket would be
// nicer but a sliding-window counter is enough for the realistic threat
// model: a single user (or a small handful on the same LAN) hitting the
// API faster than Groq can keep up. Numbers tuned so that interactive
// use (~one record per ~5–10 s) is unimpeded but a runaway loop on the
// same source IP gets 429-ed within seconds.
const (
	rateLimitWindow   = 60 * time.Second
	rateLimitMaxPerIP = 30
)

// ipRateLimiter is a per-source-IP sliding-window counter. Each IP has
// a small slice of recent request timestamps; on each call we drop the
// ones older than the window and check whether the survivor count is
// under the limit.
//
// Memory: bounded by len(distinct_IPs) × rateLimitMaxPerIP. For a single
// user dev tool that's trivial. If this ever becomes a public-facing
// service, swap in golang.org/x/time/rate or evict entries on a janitor
// timer.
type ipRateLimiter struct {
	mu     sync.Mutex
	events map[string][]time.Time
}

func newIPRateLimiter() *ipRateLimiter {
	return &ipRateLimiter{events: make(map[string][]time.Time)}
}

// allow records a request from ip at time now and returns true if the
// request fits inside the current window's allowance. now is injected
// so tests can drive the clock; production callers pass time.Now().
func (l *ipRateLimiter) allow(ip string, now time.Time, window time.Duration, maxN int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-window)
	prior := l.events[ip]

	// In-place compact: keep only timestamps newer than cutoff. Reusing
	// the same backing array avoids a hot-path allocation.
	kept := prior[:0]
	for _, t := range prior {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= maxN {
		l.events[ip] = kept
		return false
	}
	l.events[ip] = append(kept, now)
	return true
}

// clientIP extracts a stable identifier for rate-limiting from a request.
// We use net.SplitHostPort to strip the port from RemoteAddr — IPv6
// addresses include colons, so a naive strings.Split would mangle them.
// If RemoteAddr is malformed (which the net/http server should never
// emit, but we don't trust the input), we fall back to the full string
// so we still rate-limit something.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimitMiddleware wraps next with the ipRateLimiter. On the deny
// path it returns 429 with a friendly JSON body and a Retry-After
// header that hints at the window length so a polite client can pause
// before retrying.
//
// Configured to allow [rateLimitMaxPerIP] requests per
// [rateLimitWindow] per source IP. Tuned for interactive dictation;
// a future "API mode" flag could relax this.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !s.rateLimiter.allow(ip, time.Now(), rateLimitWindow, rateLimitMaxPerIP) {
			w.Header().Set("Retry-After", strconv.Itoa(int(rateLimitWindow.Seconds())))
			writeJSONError(w, http.StatusTooManyRequests,
				"too many requests; slow down and retry")
			return
		}
		next.ServeHTTP(w, r)
	})
}
