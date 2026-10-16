package devicemanagement

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestSharedMultiDeviceEnrollment_Lifecycle(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	tokenPlain := "shared-token-xyz"
	tokenHash := HashToken(tokenPlain)
	tokenID := "tok-123"
	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	site := "Jakarta-HQ"
	maxUses := 2 // allow at most 2 devices

	if err := repo.CreateSharedEnrollmentToken(ctx, tokenID, tokenHash, "Lab PCs", &site, expiresAt, maxUses, "admin-1"); err != nil {
		t.Fatalf("create shared enrollment token: %v", err)
	}

	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).Register(r)

	// Enroll Device 1
	body1, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-LAB-01",
		OSName:          "windows",
		OSVersion:       "10.0.19045",
		AgentVersion:    "1.2.0",
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("device 1 enroll status = %d, want %d: %s", w1.Code, http.StatusOK, w1.Body.String())
	}
	var resp1 enrollResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	if resp1.DeviceID == "" || resp1.DeviceSecret == "" {
		t.Fatalf("device 1 got empty credentials: %+v", resp1)
	}

	// Enroll Device 2 using the SAME token!
	body2, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-LAB-02",
		OSName:          "linux",
		OSVersion:       "Ubuntu 22.04",
		AgentVersion:    "1.2.0",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("device 2 enroll status = %d, want %d: %s", w2.Code, http.StatusOK, w2.Body.String())
	}
	var resp2 enrollResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp2.DeviceID == "" || resp2.DeviceSecret == "" {
		t.Fatalf("device 2 got empty credentials: %+v", resp2)
	}

	// Verify both devices have distinct IDs and secrets
	if resp1.DeviceID == resp2.DeviceID {
		t.Fatalf("device 1 and 2 shared the same device ID %s", resp1.DeviceID)
	}
	if resp1.DeviceSecret == resp2.DeviceSecret {
		t.Fatalf("device 1 and 2 shared the same secret")
	}

	// Verify both exist in devices table with their respective hostnames and shared site
	dev1, err := repo.GetByID(ctx, resp1.DeviceID)
	if err != nil || dev1.Hostname != "PC-LAB-01" || dev1.SiteValue() != "Jakarta-HQ" {
		t.Fatalf("dev1 mismatch: %+v, err: %v", dev1, err)
	}
	dev2, err := repo.GetByID(ctx, resp2.DeviceID)
	if err != nil || dev2.Hostname != "PC-LAB-02" || dev2.SiteValue() != "Jakarta-HQ" {
		t.Fatalf("dev2 mismatch: %+v, err: %v", dev2, err)
	}

	// Enroll Device 3 should FAIL because maxUses = 2
	body3, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-LAB-03",
	})
	req3 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)

	if w3.Code != http.StatusUnauthorized {
		t.Fatalf("device 3 enroll status = %d, want 401 when max_uses exceeded: %s", w3.Code, w3.Body.String())
	}
}

func TestSharedMultiDeviceEnrollment_ExpiredToken(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	tokenPlain := "expired-shared-token"
	tokenHash := HashToken(tokenPlain)
	tokenID := "tok-expired"
	// Already expired 10 minutes ago
	expiresAt := time.Now().UTC().Add(-10 * time.Minute)

	if err := repo.CreateSharedEnrollmentToken(ctx, tokenID, tokenHash, "Old Batch", nil, expiresAt, 0, "admin-1"); err != nil {
		t.Fatalf("create shared enrollment token: %v", err)
	}

	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).Register(r)

	body, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-EXPIRED",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("enroll expired token status = %d, want 401: %s", w.Code, w.Body.String())
	}
}

func TestSharedMultiDeviceEnrollment_PerDeviceSiteOverride(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	tokenPlain := "shared-site-override-token"
	tokenHash := HashToken(tokenPlain)
	tokenID := "tok-sites"
	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	defaultSite := "Jakarta-HQ"

	if err := repo.CreateSharedEnrollmentToken(ctx, tokenID, tokenHash, "Multi-Site Batch", &defaultSite, expiresAt, 0, "admin-1"); err != nil {
		t.Fatalf("create shared enrollment token: %v", err)
	}

	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).Register(r)

	// Device 1 specifies custom branch site: Medan-Branch
	body1, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-MEDAN-01",
		Site:            "Medan-Branch",
	})
	req1 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("dev1 enroll status = %d: %s", w1.Code, w1.Body.String())
	}
	var resp1 enrollResponse
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)

	// Device 2 omits site -> should fall back to token default site: Jakarta-HQ
	body2, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-HQ-01",
		Site:            "",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("dev2 enroll status = %d: %s", w2.Code, w2.Body.String())
	}
	var resp2 enrollResponse
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)

	// Device 3 specifies custom branch site: Surabaya-Branch
	body3, _ := json.Marshal(enrollRequest{
		EnrollmentToken: tokenPlain,
		Hostname:        "PC-SBY-01",
		Site:            "Surabaya-Branch",
	})
	req3 := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	r.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("dev3 enroll status = %d: %s", w3.Code, w3.Body.String())
	}
	var resp3 enrollResponse
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)

	// Verify in database
	dev1, err := repo.GetByID(ctx, resp1.DeviceID)
	if err != nil || dev1.SiteValue() != "Medan-Branch" {
		t.Fatalf("dev1 expected site Medan-Branch, got: %s (err: %v)", dev1.SiteValue(), err)
	}

	dev2, err := repo.GetByID(ctx, resp2.DeviceID)
	if err != nil || dev2.SiteValue() != "Jakarta-HQ" {
		t.Fatalf("dev2 expected fallback site Jakarta-HQ, got: %s (err: %v)", dev2.SiteValue(), err)
	}

	dev3, err := repo.GetByID(ctx, resp3.DeviceID)
	if err != nil || dev3.SiteValue() != "Surabaya-Branch" {
		t.Fatalf("dev3 expected site Surabaya-Branch, got: %s (err: %v)", dev3.SiteValue(), err)
	}
}
