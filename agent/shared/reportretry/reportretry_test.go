package reportretry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func server(t *testing.T, codes ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if r.Header.Get("X-Device-Id") != "dev-1" || r.Header.Get("X-Device-Secret") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		code := codes[len(codes)-1]
		if n <= len(codes) {
			code = codes[n-1]
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestPost_RetriesTransientFailure(t *testing.T) {
	srv, calls := server(t, 502, 500, 200)
	if err := Post(context.Background(), srv.Client(), srv.URL, "dev-1", "s3cret", map[string]string{"a": "b"}); err != nil {
		t.Fatalf("Post = %v, want success on the third attempt", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestPost_DoesNotRetryRefusal(t *testing.T) {
	srv, calls := server(t, 409)
	err := Post(context.Background(), srv.Client(), srv.URL, "dev-1", "s3cret", 1)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("Post = %v, want a permanent error", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 — a 4xx is refused again on retry", got)
	}
}

func TestPost_GivesUpAfterAttempts(t *testing.T) {
	srv, calls := server(t, 503)
	if err := Post(context.Background(), srv.Client(), srv.URL, "dev-1", "s3cret", 1); err == nil {
		t.Fatal("Post succeeded against a server that always fails")
	}
	if got := calls.Load(); got != Attempts {
		t.Fatalf("attempts = %d, want %d", got, Attempts)
	}
}
