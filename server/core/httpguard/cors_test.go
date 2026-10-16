package httpguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS_PreflightAndHeaders(t *testing.T) {
	middleware := CORS(CORSOptions{
		OriginValidator: func(r *http.Request) bool {
			return r.Header.Get("Origin") == "https://console.example.com"
		},
		AllowCredentials: true,
		MaxAge:           86400,
	})

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Case 1: Preflight OPTIONS from trusted origin
	reqOpt := httptest.NewRequest(http.MethodOptions, "/api/devices", nil)
	reqOpt.Header.Set("Origin", "https://console.example.com")
	wOpt := httptest.NewRecorder()
	handler.ServeHTTP(wOpt, reqOpt)

	if wOpt.Code != http.StatusNoContent {
		t.Fatalf("options status=%d, want 204", wOpt.Code)
	}
	if wOpt.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" {
		t.Errorf("expected allow origin header set")
	}
	if wOpt.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected allow credentials header set")
	}

	// Case 2: Disallowed origin
	reqBad := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	reqBad.Header.Set("Origin", "https://malicious.evil.com")
	wBad := httptest.NewRecorder()
	handler.ServeHTTP(wBad, reqBad)

	if wBad.Code != http.StatusOK {
		t.Fatalf("bad origin request status=%d, want 200", wBad.Code)
	}
	if wBad.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("bad origin must not get Access-Control-Allow-Origin")
	}

	// Case 3: Non-browser request without Origin
	reqDirect := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	wDirect := httptest.NewRecorder()
	handler.ServeHTTP(wDirect, reqDirect)

	if wDirect.Code != http.StatusOK {
		t.Fatalf("direct request status=%d, want 200", wDirect.Code)
	}
	if wDirect.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("direct request must not get CORS header")
	}
}
