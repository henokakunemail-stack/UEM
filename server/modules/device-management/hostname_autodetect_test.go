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
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
)

func TestCreateEnrollToken_OptionalHostnameAndOS(t *testing.T) {
	d := newExpiryDB(t)
	repo := NewRepository(d)
	jwt := auth.NewJWTService("secret", time.Hour, 24*time.Hour)
	handler := NewHandler(repo, d, jwt, time.Hour)

	// 1. Request with empty hostname and empty os_name
	payload := []byte(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/devices/enroll-token", bytes.NewReader(payload))
	rec := httptest.NewRecorder()

	handler.createEnrollToken(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201 Created, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp createEnrollTokenResp
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.DeviceID == "" || resp.EnrollmentToken == "" {
		t.Fatalf("expected non-empty device id and token: %+v", resp)
	}

	// Verify device created in DB with placeholder
	dev, err := repo.GetByID(context.Background(), resp.DeviceID)
	if err != nil {
		t.Fatalf("get created device: %v", err)
	}
	if dev.Hostname != "Pending Enrollment" {
		t.Fatalf("expected default hostname 'Pending Enrollment', got '%s'", dev.Hostname)
	}
	if dev.OSName != "windows" {
		t.Fatalf("expected default OS 'windows', got '%s'", dev.OSName)
	}

	// 2. Test UpdateSystemInfo (simulating agent hello)
	err = repo.UpdateSystemInfo(context.Background(), dev.ID, "DESKTOP-FIN-88", "windows", "11.0.22631", "1.0.0")
	if err != nil {
		t.Fatalf("update system info: %v", err)
	}

	updated, err := repo.GetByID(context.Background(), dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Hostname != "DESKTOP-FIN-88" {
		t.Fatalf("expected updated hostname 'DESKTOP-FIN-88', got '%s'", updated.Hostname)
	}
	if updated.OSVersionString() != "11.0.22631" {
		t.Fatalf("expected OS version '11.0.22631', got '%s'", updated.OSVersionString())
	}

	// 3. Test UpdateDevice (manual rename)
	err = repo.UpdateDevice(context.Background(), dev.ID, "PC-VIP-01", "Branch-Bali")
	if err != nil {
		t.Fatalf("update device: %v", err)
	}
	renamed, err := repo.GetByID(context.Background(), dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Hostname != "PC-VIP-01" {
		t.Fatalf("expected renamed hostname 'PC-VIP-01', got '%s'", renamed.Hostname)
	}
	if renamed.SiteValue() != "Branch-Bali" {
		t.Fatalf("expected site 'Branch-Bali', got '%s'", renamed.SiteValue())
	}
}

func TestHandler_UpdateDeviceEndpoint(t *testing.T) {
	d := newExpiryDB(t)
	repo := NewRepository(d)
	jwt := auth.NewJWTService("secret", time.Hour, 24*time.Hour)
	handler := NewHandler(repo, d, jwt, time.Hour)

	// Seed device
	now := time.Now().UTC()
	dev := Device{
		ID:         "dev-rename-1",
		Hostname:   "OLD-NAME",
		OSName:     "linux",
		Status:     StatusOffline,
		EnrolledAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := repo.Create(context.Background(), dev); err != nil {
		t.Fatal(err)
	}

	// Patch device
	body := []byte(`{"hostname": "NEW-SRV-NAME", "site": "DataCenter-01"}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/devices/dev-rename-1", bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "dev-rename-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxUserID, "admin"))

	rec := httptest.NewRecorder()
	handler.updateDevice(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var updatedDTO deviceDTO
	if err := json.NewDecoder(rec.Body).Decode(&updatedDTO); err != nil {
		t.Fatal(err)
	}
	if updatedDTO.Hostname != "NEW-SRV-NAME" {
		t.Fatalf("expected hostname 'NEW-SRV-NAME', got '%s'", updatedDTO.Hostname)
	}
	if updatedDTO.Site != "DataCenter-01" {
		t.Fatalf("expected site 'DataCenter-01', got '%s'", updatedDTO.Site)
	}
}
