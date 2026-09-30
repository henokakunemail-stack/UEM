package devicemanagement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSecretLookup is a stand-in for Repository that resolves only one known
// secret, so the tests can distinguish authenticated from anonymous callers
// without needing a database.
type fakeSecretLookup struct {
	deviceID string
	secret   string
}

func (f fakeSecretLookup) FindBySecretHash(_ context.Context, hash string) (Device, error) {
	if hash == HashToken(f.secret) {
		return Device{ID: f.deviceID}, nil
	}
	return Device{}, ErrNotFound
}

func TestAuthenticateAgent_RejectsMissingCredentials(t *testing.T) {
	lookup := fakeSecretLookup{deviceID: "dev-1", secret: "s3cret"}

	cases := map[string]http.Header{
		"no headers at all": {},
		"id without secret": {"X-Device-Id": {"dev-1"}},
		"secret without id": {"X-Device-Secret": {"s3cret"}},
	}

	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/agent/x", nil)
			for k, v := range hdr {
				req.Header.Set(k, v[0])
			}
			rec := httptest.NewRecorder()

			if _, ok := AuthenticateAgent(rec, req, lookup); ok {
				t.Fatal("anonymous request was authenticated")
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
			}
		})
	}
}

func TestAuthenticateAgent_RejectsWrongSecret(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/agent/x", nil)
	req.Header.Set("X-Device-Id", "dev-1")
	req.Header.Set("X-Device-Secret", "not-the-real-secret")
	rec := httptest.NewRecorder()

	if _, ok := AuthenticateAgent(rec, req, fakeSecretLookup{deviceID: "dev-1", secret: "s3cret"}); ok {
		t.Fatal("request with wrong secret was authenticated")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestAuthenticateAgent_RejectsSecretBoundToAnotherDevice is the important one:
// a device holding a valid secret must not be able to act on behalf of a
// different device ID.
func TestAuthenticateAgent_RejectsSecretBoundToAnotherDevice(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/agent/x", nil)
	req.Header.Set("X-Device-Id", "dev-2") // not the owner
	req.Header.Set("X-Device-Secret", "s3cret")
	rec := httptest.NewRecorder()

	if _, ok := AuthenticateAgent(rec, req, fakeSecretLookup{deviceID: "dev-1", secret: "s3cret"}); ok {
		t.Fatal("valid secret was accepted for a different device ID")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAuthenticateAgent_AcceptsMatchingPair(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/agent/x", nil)
	req.Header.Set("X-Device-Id", "dev-1")
	req.Header.Set("X-Device-Secret", "s3cret")
	rec := httptest.NewRecorder()

	gotID, ok := AuthenticateAgent(rec, req, fakeSecretLookup{deviceID: "dev-1", secret: "s3cret"})
	if !ok {
		t.Fatalf("valid credentials rejected (status %d)", rec.Code)
	}
	if gotID != "dev-1" {
		t.Errorf("device ID = %q, want %q", gotID, "dev-1")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (no error written)", rec.Code, http.StatusOK)
	}
}
