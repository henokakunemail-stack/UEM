package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIRateLimiter_AllowAndHeaders(t *testing.T) {
	// 60 req/min, burst 3
	limiter := NewAPIRateLimiter(60, 3)
	defer limiter.Close()

	handler := limiter.Middleware(func(r *http.Request) string {
		return "client-1"
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Request 1: allowed, remaining=2
	req1 := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("req1 status=%d, want 200", w1.Code)
	}
	if rem := w1.Header().Get("RateLimit-Remaining"); rem != "2" {
		t.Errorf("req1 remaining=%s, want 2", rem)
	}

	// Request 2: allowed, remaining=1
	req2 := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("req2 status=%d, want 200", w2.Code)
	}
	if rem := w2.Header().Get("RateLimit-Remaining"); rem != "1" {
		t.Errorf("req2 remaining=%s, want 1", rem)
	}

	// Request 3: allowed, remaining=0
	req3 := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("req3 status=%d, want 200", w3.Code)
	}
	if rem := w3.Header().Get("RateLimit-Remaining"); rem != "0" {
		t.Errorf("req3 remaining=%s, want 0", rem)
	}

	// Request 4: rejected with 429 Too Many Requests
	req4 := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)
	if w4.Code != http.StatusTooManyRequests {
		t.Fatalf("req4 status=%d, want 429", w4.Code)
	}
	if ra := w4.Header().Get("Retry-After"); ra == "" {
		t.Errorf("req4 missing Retry-After header")
	}

	// Another client should not be affected
	reqOther := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	handlerOther := limiter.Middleware(func(r *http.Request) string {
		return "client-2"
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	wOther := httptest.NewRecorder()
	handlerOther.ServeHTTP(wOther, reqOther)
	if wOther.Code != http.StatusOK {
		t.Fatalf("client-2 status=%d, want 200", wOther.Code)
	}
}
