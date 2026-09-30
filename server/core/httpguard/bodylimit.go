// Package httpguard holds request-size guards for the server's public surface.
package httpguard

import "net/http"

// JSONBodyLimit is the ceiling for a JSON request body. Every route that
// accepts JSON gets this; a body over it is a mistake or an attack, never a
// legitimate request.
//
// The number is small because these are credential and configuration payloads:
// a login is a username and a password, an enrollment is five short strings.
// There is nothing here that legitimately approaches a megabyte.
const JSONBodyLimit = 1 << 20 // 1 MiB

// LimitJSONBody caps r.Body at JSONBodyLimit for the wrapped handler.
//
// Why this exists at all: the routes this is applied to are the four reachable
// without credentials -- /api/agent/enroll, /api/auth/login, /api/auth/refresh
// and /api/auth/logout. They decode json.NewDecoder(r.Body) with no cap, and
// decode before they authenticate, so an unauthenticated caller can make the
// server buffer and parse an arbitrarily large body. A single 256 MB body was
// measured driving the process to ~2.3 GB of heap, and ten concurrent ones to
// ~19.8 GB. The decode fails at the end, after the memory has already been
// spent, so the failure mode is memory exhaustion rather than a rejected
// request.
//
// This is not a router-level middleware on purpose. The package upload route
// legitimately accepts hundreds of megabytes, and a global cap would break it
// the moment someone reordered a line. Applying the cap at each route keeps
// the large-body routes on their own, larger, explicit limit.
func LimitJSONBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, JSONBodyLimit)
		}
		next.ServeHTTP(w, r)
	})
}

// LimitJSONBodyFunc is LimitJSONBody for a plain HandlerFunc, which is what the
// two modules that mount these routes on a mux directly have.
func LimitJSONBodyFunc(next http.HandlerFunc) http.HandlerFunc {
	return LimitJSONBody(next).ServeHTTP
}
