package devicemanagement

import (
	"context"
	"testing"
	"time"
)

// EnrollmentTokenValid gates the Linux installer's binary download. It must
// say yes exactly when enrollment would accept the token, and never consume it.
func TestEnrollmentTokenValidMatchesWhatEnrollmentAccepts(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)
	now := time.Now().UTC()

	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	deviceToken := seedPendingToken(t, d, "pending", &future)

	mustShared := func(id, plain string, expires time.Time, maxUses int) {
		t.Helper()
		if err := repo.CreateSharedEnrollmentToken(ctx, id, HashToken(plain), id, nil, expires, maxUses, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	mustShared("shared-ok", "shared-ok-token", future, 0)
	mustShared("shared-expired", "shared-expired-token", past, 0)
	mustShared("shared-used", "shared-used-token", future, 1)
	d.MustExec(`UPDATE enrollment_tokens SET use_count = 1 WHERE id = 'shared-used'`)
	mustShared("shared-revoked", "shared-revoked-token", future, 0)
	d.MustExec(`UPDATE enrollment_tokens SET revoked_at = ? WHERE id = 'shared-revoked'`, now)

	cases := map[string]bool{
		deviceToken:            true,
		"shared-ok-token":      true,
		"shared-expired-token": false,
		"shared-used-token":    false,
		"shared-revoked-token": false,
		"never-issued":         false,
		"":                     false,
	}
	for tok, want := range cases {
		got, err := repo.EnrollmentTokenValid(ctx, tok)
		if err != nil {
			t.Fatalf("%q: %v", tok, err)
		}
		if got != want {
			t.Errorf("EnrollmentTokenValid(%q) = %v, want %v", tok, got, want)
		}
	}

	// Checking is not using: the device token still enrolls afterwards.
	if _, err := repo.findByEnrollmentTokenHash(ctx, HashToken(deviceToken)); err != nil {
		t.Fatalf("the check consumed the device token: %v", err)
	}
	var uses int
	if err := d.Get(&uses, `SELECT use_count FROM enrollment_tokens WHERE id = 'shared-ok'`); err != nil || uses != 0 {
		t.Fatalf("the check used the shared token: use_count=%d err=%v", uses, err)
	}

	// An expired device token is refused like an expired shared one.
	d.MustExec(`UPDATE devices SET enrollment_token_expires_at = ? WHERE id = 'pending'`, past)
	if ok, _ := repo.EnrollmentTokenValid(ctx, deviceToken); ok {
		t.Error("an expired device token was accepted")
	}
}
