package devicemanagement

import (
	"context"
	"testing"
	"time"
)

// A single-device token writes the device row when it is issued, and the
// agent may exchange it days later. enrolled_at stayed at the issue time, so
// the console could not tell a machine that joined an hour ago from one whose
// token had simply been sitting around; it is now the exchange time.
func TestEnrolledAtIsWhenTheAgentEnrolledNotWhenTheTokenWasIssued(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	token := seedPendingToken(t, d, "dev-1", nil) // enrolled_at = mustTime: 2026-09-22

	before := time.Now().UTC().Add(-time.Second)
	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("secret")); err != nil {
		t.Fatal(err)
	}
	dev, err := repo.GetByID(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if dev.EnrolledAt.Before(before) {
		t.Errorf("enrolled_at = %s, want the moment the token was exchanged (after %s)", dev.EnrolledAt, before)
	}
}
