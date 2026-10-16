package httpguard

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

type rateRecord struct {
	tokens     float64
	lastRefill time.Time
}

// APIRateLimiter provides token-bucket rate limiting for API endpoints.
type APIRateLimiter struct {
	ratePerSec float64 // refill rate in tokens per second
	capacity   float64 // maximum bucket capacity
	records    map[string]*rateRecord
	mu         sync.Mutex
	stop       chan struct{}
}

// NewAPIRateLimiter creates an API rate limiter.
// reqPerMinute: allowed requests per minute (e.g. 120).
// burstCapacity: maximum burst tokens (e.g. 60).
func NewAPIRateLimiter(reqPerMinute int, burstCapacity int) *APIRateLimiter {
	if reqPerMinute <= 0 {
		reqPerMinute = 120
	}
	if burstCapacity <= 0 {
		burstCapacity = reqPerMinute / 2
		if burstCapacity < 10 {
			burstCapacity = 10
		}
	}

	limiter := &APIRateLimiter{
		ratePerSec: float64(reqPerMinute) / 60.0,
		capacity:   float64(burstCapacity),
		records:    make(map[string]*rateRecord),
		stop:       make(chan struct{}),
	}
	go limiter.cleanupLoop()
	return limiter
}

// Allow evaluates if a request from key (typically client IP) is permitted.
// Returns (allowed, remainingTokens, resetSeconds).
func (l *APIRateLimiter) Allow(key string) (bool, int, int) {
	if l == nil || key == "" {
		return true, int(l.capacity), 0
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()
	rec, exists := l.records[key]
	if !exists {
		rec = &rateRecord{
			tokens:     l.capacity,
			lastRefill: now,
		}
		l.records[key] = rec
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(rec.lastRefill).Seconds()
	if elapsed > 0 {
		rec.tokens += elapsed * l.ratePerSec
		if rec.tokens > l.capacity {
			rec.tokens = l.capacity
		}
		rec.lastRefill = now
	}

	// Calculate reset seconds until full bucket
	missing := l.capacity - rec.tokens
	resetSec := int(missing / l.ratePerSec)
	if resetSec < 1 {
		resetSec = 1
	}

	if rec.tokens >= 1.0 {
		rec.tokens -= 1.0
		return true, int(rec.tokens), resetSec
	}

	// Throttled
	retryAfter := int((1.0 - rec.tokens) / l.ratePerSec)
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, 0, retryAfter
}

// Close terminates the background cleanup routine.
func (l *APIRateLimiter) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	select {
	case <-l.stop:
		l.mu.Unlock()
		return
	default:
		close(l.stop)
	}
	l.mu.Unlock()
}

func (l *APIRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now().UTC()
			for k, rec := range l.records {
				if now.Sub(rec.lastRefill) > 5*time.Minute {
					delete(l.records, k)
				}
			}
			l.mu.Unlock()
		case <-l.stop:
			return
		}
	}
}

// Middleware returns an HTTP middleware enforcing rate limits with standard headers.
func (l *APIRateLimiter) Middleware(keyExtractor func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := ""
			if keyExtractor != nil {
				key = keyExtractor(r)
			}
			if key == "" {
				host, _, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					key = r.RemoteAddr
				} else {
					key = host
				}
			}

			allowed, remaining, resetSec := l.Allow(key)

			w.Header().Set("RateLimit-Limit", strconv.Itoa(int(l.capacity)))
			w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("RateLimit-Reset", strconv.Itoa(resetSec))

			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(resetSec))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":       "API rate limit exceeded; please slow down",
					"retry_after": resetSec,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
