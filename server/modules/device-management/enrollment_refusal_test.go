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

func enrollWith(t *testing.T, r http.Handler, token, host string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(enrollRequest{EnrollmentToken: token, Hostname: host, OSName: "windows"})
	req := httptest.NewRequest(http.MethodPost, "/api/agent/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func refusalCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body.Code
}

// Every refusal used to read "invalid or expired enrollment token", so an
// operator could not tell a batch token that ran out of uses from a typo.
func TestEnrollRefusalsSayWhy(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).Register(r)
	future := time.Now().UTC().Add(time.Hour)

	if err := repo.CreateSharedEnrollmentToken(ctx, "t-one", HashToken("one-use"), "one", nil, future, 1, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := enrollWith(t, r, "one-use", "A"); rec.Code != http.StatusOK {
		t.Fatalf("first use = %d %s", rec.Code, rec.Body.String())
	}
	if err := repo.CreateSharedEnrollmentToken(ctx, "t-old", HashToken("old"), "old", nil, time.Now().UTC().Add(-time.Minute), 0, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateSharedEnrollmentToken(ctx, "t-rev", HashToken("revoked"), "rev", nil, future, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	d.MustExec(`UPDATE enrollment_tokens SET revoked_at = ? WHERE id = 't-rev'`, time.Now().UTC())

	for token, want := range map[string]string{
		"one-use":   EnrollTokenUsedUp,
		"old":       EnrollTokenExpired,
		"revoked":   EnrollTokenRevoked,
		"never-was": EnrollTokenInvalid,
	} {
		rec := enrollWith(t, r, token, "B")
		if rec.Code != http.StatusUnauthorized || refusalCode(t, rec) != want {
			t.Errorf("%s: %d %s, want 401 %s", token, rec.Code, rec.Body.String(), want)
		}
	}

	var audited int
	if err := d.Get(&audited, `SELECT COUNT(*) FROM audit_logs WHERE action = 'device.enroll_failed' AND target_id = 't-one'`); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("refusals of the used-up shared token audited %d times, want 1", audited)
	}
}

// A token pasted with a trailing newline is the same token.
func TestEnrollTrimsTheToken(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).Register(r)
	if err := repo.CreateSharedEnrollmentToken(ctx, "t", HashToken("batch"), "b", nil, time.Now().UTC().Add(time.Hour), 0, "admin"); err != nil {
		t.Fatal(err)
	}
	if rec := enrollWith(t, r, "  batch\r\n", "A"); rec.Code != http.StatusOK {
		t.Fatalf("token with whitespace = %d %s", rec.Code, rec.Body.String())
	}
}

// A failing database is a server fault. The shared lookup's error used to
// fall through to the single-device path and read as a bad token.
func TestEnrollDatabaseFaultIsNotABadToken(t *testing.T) {
	d := newExpiryDB(t)
	r := chi.NewRouter()
	NewEnrollmentHandler(NewRepository(d), d).Register(r)
	d.MustExec(`ALTER TABLE enrollment_tokens RENAME TO enrollment_tokens_gone`)
	if rec := enrollWith(t, r, "anything", "A"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("enroll with a broken token table = %d %s, want 500", rec.Code, rec.Body.String())
	}
}

// A branch behind one NAT address enrolls many machines. Failures from
// earlier tries kept counting after successful enrollments, so the whole
// site could be locked out for five minutes.
func TestASuccessfulEnrollClearsTheFailureCount(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	limiter := auth.NewIPRateLimiter(3, time.Minute, 5*time.Minute)
	r := chi.NewRouter()
	NewEnrollmentHandler(repo, d).WithRateLimiter(limiter).Register(r)
	if err := repo.CreateSharedEnrollmentToken(ctx, "t", HashToken("batch"), "b", nil, time.Now().UTC().Add(time.Hour), 0, "admin"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		enrollWith(t, r, "typo", "X")
	}
	if rec := enrollWith(t, r, "batch", "A"); rec.Code != http.StatusOK {
		t.Fatalf("enroll = %d", rec.Code)
	}
	for i := 0; i < 2; i++ {
		enrollWith(t, r, "typo", "X")
	}
	if rec := enrollWith(t, r, "batch", "B"); rec.Code != http.StatusOK {
		t.Fatalf("enroll after earlier failures = %d %s, want 200: a success must clear the count", rec.Code, rec.Body.String())
	}
}
