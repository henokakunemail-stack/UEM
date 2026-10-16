package httpguard

import (
	"net/http"
)

// CORSOptions configures cross-origin resource sharing.
type CORSOptions struct {
	AllowedOrigins   []string
	OriginValidator  func(r *http.Request) bool
	AllowCredentials bool
	MaxAge           int
}

// CORS returns an HTTP middleware enforcing CORS headers and handling preflight OPTIONS.
func CORS(opts CORSOptions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			allowed := false
			if opts.OriginValidator != nil {
				allowed = opts.OriginValidator(r)
			} else if len(opts.AllowedOrigins) == 0 {
				allowed = true
			} else {
				for _, o := range opts.AllowedOrigins {
					if o == "*" || o == origin {
						allowed = true
						break
					}
				}
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, Accept, X-Requested-With")
				w.Header().Set("Access-Control-Expose-Headers", "RateLimit-Limit, RateLimit-Remaining, RateLimit-Reset, Retry-After, X-Request-ID")
				if opts.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				if opts.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", "86400")
				}
				w.Header().Add("Vary", "Origin")
			}

			// Preflight request handling
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
