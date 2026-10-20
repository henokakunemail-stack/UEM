package metrics

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// requestInfo is filled in by handlers deeper in the chain and read back by the
// middleware after the request. A context only flows inward, so the outer
// middleware cannot see a user id the auth middleware puts into its own derived
// context; a shared pointer is how the answer gets back out.
type requestInfo struct {
	user string
}

type infoKey struct{}

// SetUser records the authenticated user for the access log. Called by the auth
// middleware; a no-op outside a request the HTTP middleware wrapped.
func SetUser(ctx context.Context, userID string) {
	if info, ok := ctx.Value(infoKey{}).(*requestInfo); ok {
		info.user = userID
	}
}

// HTTP records every request: a counter and a latency histogram per route
// pattern, and one access-log line.
//
// The route is chi's pattern (/api/devices/{id}), not the raw path, so the
// metric has one series per endpoint rather than one per device -- a label per
// device id would grow without bound and take the scraper down with it.
// Requests that match no route are labelled "unmatched" for the same reason.
type HTTP struct {
	requests *CounterVec
	duration *HistogramVec
	// Quiet paths are counted but not logged: a load balancer probing
	// /readyz every second would otherwise be most of the log.
	quiet map[string]bool
}

func NewHTTP(r *Registry) *HTTP {
	return &HTTP{
		requests: r.Counter("em_http_requests_total", "HTTP requests served, by method, route and status.", "method", "route", "status"),
		duration: r.Histogram("em_http_request_duration_seconds", "HTTP request latency by route.", DefaultLatencyBuckets, "route"),
		quiet:    map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true},
	}
}

func (m *HTTP) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		r = r.WithContext(context.WithValue(r.Context(), infoKey{}, info))
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		status := ww.Status()
		if status == 0 {
			// A hijacked WebSocket, or a handler that wrote nothing.
			status = http.StatusOK
		}
		route := "unmatched"
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if p := rc.RoutePattern(); p != "" {
				route = p
			}
		}
		elapsed := time.Since(start)
		m.requests.Inc(r.Method, route, strconv.Itoa(status))
		m.duration.Observe(elapsed.Seconds(), route)

		if m.quiet[r.URL.Path] {
			return
		}
		ev := log.Info()
		if status >= 500 {
			ev = log.Error()
		} else if status >= 400 {
			ev = log.Warn()
		}
		ev.Str("request_id", middleware.GetReqID(r.Context())).
			Str("method", r.Method).
			Str("route", route).
			Str("path", r.URL.Path).
			Int("status", status).
			Int("bytes", ww.BytesWritten()).
			Dur("duration", elapsed).
			Str("user", info.user).
			Msg("http request")
	})
}
