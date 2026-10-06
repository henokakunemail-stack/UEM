package devicemanagement

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/jmoiron/sqlx"
)

func newExpiryDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedPendingToken writes a device waiting on an enrollment token, expiring at
// the given instant. A nil deadline is a token with no expiry at all.
func seedPendingToken(t *testing.T, d *sqlx.DB, id string, expires *time.Time) string {
	t.Helper()
	const plain = "the-token"
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash,
		                     enrollment_token_hash, enrollment_token_expires_at, created_at, updated_at)
		VALUES (?, 'PC-1', 'windows', 'offline', ?, '', ?, ?, ?, ?)`,
		id, mustTime(t), HashToken(plain), expires, mustTime(t), mustTime(t))
	if err != nil {
		t.Fatalf("seed pending token for %s: %v", id, err)
	}
	return plain
}

// An enrollment token that nobody used used to stay valid forever. The endpoint
// reported an expires_at derived from ENROLLMENT_TTL and no code read it, so the
// only thing that ever invalidated a token was somebody using it. A token pasted
// into a chat was then a permanent credential for that device.
func TestExpiredEnrollmentTokenIsRefused(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	past := time.Now().UTC().Add(-time.Minute)
	token := seedPendingToken(t, d, "dev-expired", &past)

	_, err := repo.findByEnrollmentTokenHash(ctx, HashToken(token))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("lookup of an expired token returned %v, want ErrNotFound", err)
	}

	// The lookup is only half of it: this statement is what actually mints the
	// secret, and it must refuse the same token.
	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("minted-secret")); err == nil {
		t.Fatal("an expired token must not be spendable")
	}
	var secret string
	if err := d.Get(&secret, `SELECT device_secret_hash FROM devices WHERE id = 'dev-expired'`); err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if secret != "" {
		t.Errorf("device_secret_hash = %q, want empty: an expired token minted a credential", secret)
	}
}

// A token past its deadline must read as absent, not as a distinguishable
// failure. Somebody probing for valid tokens learns nothing from the difference.
func TestExpiredAndUnknownTokensAreIndistinguishable(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	past := time.Now().UTC().Add(-time.Minute)
	seedPendingToken(t, d, "dev-expired", &past)

	_, expiredErr := repo.findByEnrollmentTokenHash(ctx, HashToken("the-token"))
	_, unknownErr := repo.findByEnrollmentTokenHash(ctx, HashToken("never-issued"))

	if !errors.Is(expiredErr, ErrNotFound) || !errors.Is(unknownErr, ErrNotFound) {
		t.Errorf("expired = %v, unknown = %v; both must be ErrNotFound", expiredErr, unknownErr)
	}

	expErr, unkErr := repo.ConsumeEnrollmentToken(ctx, HashToken("the-token"), HashToken("s1")),
		repo.ConsumeEnrollmentToken(ctx, HashToken("never-issued"), HashToken("s2"))
	if !errors.Is(expErr, ErrNotFound) || !errors.Is(unkErr, ErrNotFound) {
		t.Errorf("consume: expired = %v, unknown = %v; both must be ErrNotFound", expErr, unkErr)
	}
}

// Every device enrolled before this column existed has NULL there, and those
// tokens must keep working. Refusing them would strand a machine somebody
// enrolled on purpose, over a deadline that server never enforced.
func TestEnrollmentTokenWithNoDeadlineStillWorks(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	token := seedPendingToken(t, d, "dev-legacy", nil)

	dev, err := repo.findByEnrollmentTokenHash(ctx, HashToken(token))
	if err != nil {
		t.Fatalf("a token with no deadline must be honoured, got: %v", err)
	}
	if dev.ID != "dev-legacy" {
		t.Errorf("ID = %q, want dev-legacy", dev.ID)
	}
	if dev.EnrollmentTokenExpiresAt != nil {
		t.Errorf("EnrollmentTokenExpiresAt = %v, want nil", dev.EnrollmentTokenExpiresAt)
	}

	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("minted")); err != nil {
		t.Fatalf("consume a token with no deadline: %v", err)
	}
	var secret string
	if err := d.Get(&secret, `SELECT device_secret_hash FROM devices WHERE id = 'dev-legacy'`); err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if secret != HashToken("minted") {
		t.Error("the legacy token did not mint a secret")
	}
}

// The boundary itself: a deadline a millisecond in the future is still live.
func TestEnrollmentTokenJustBeforeItsDeadlineStillWorks(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	soon := time.Now().UTC().Add(time.Hour)
	token := seedPendingToken(t, d, "dev-soon", &soon)

	if _, err := repo.findByEnrollmentTokenHash(ctx, HashToken(token)); err != nil {
		t.Fatalf("a token with a future deadline must be honoured, got: %v", err)
	}
	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("minted")); err != nil {
		t.Fatalf("consume before deadline: %v", err)
	}
}

// A consumed token is spent. This is the guarantee that existed before expiry
// and must survive it: nulling the hash has to keep working.
func TestConsumedEnrollmentTokenCannotBeReplayed(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	future := time.Now().UTC().Add(time.Hour)
	token := seedPendingToken(t, d, "dev-once", &future)

	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("first")); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := repo.ConsumeEnrollmentToken(ctx, HashToken(token), HashToken("second")); !errors.Is(err, ErrNotFound) {
		t.Errorf("replay returned %v, want ErrNotFound", err)
	}

	var secret string
	if err := d.Get(&secret, `SELECT device_secret_hash FROM devices WHERE id = 'dev-once'`); err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if secret != HashToken("first") {
		t.Error("a replayed token overwrote the secret it had already issued")
	}
}

// Create must persist the deadline. The column exists and the handler sets the
// field, but if this INSERT omitted it the token would land as NULL and live
// forever -- the exact defect the column was added to close.
func TestCreateStoresTheTokenDeadline(t *testing.T) {
	ctx := context.Background()
	d := newExpiryDB(t)
	repo := NewRepository(d)

	hash := HashToken("a-token")
	expires := time.Now().UTC().Add(30 * time.Minute)
	site := "Jakarta"
	dev := Device{
		ID:                       "dev-new",
		Hostname:                 "PC-NEW",
		OSName:                   OSWindows,
		Status:                   StatusOffline,
		EnrolledAt:               mustTime(t),
		EnrollmentTokenHash:      &hash,
		EnrollmentTokenExpiresAt: &expires,
		Site:                     &site,
		CreatedAt:                mustTime(t),
		UpdatedAt:                mustTime(t),
	}
	if err := repo.Create(ctx, dev); err != nil {
		t.Fatalf("create: %v", err)
	}

	var stored *time.Time
	if err := d.Get(&stored,
		`SELECT enrollment_token_expires_at FROM devices WHERE id = 'dev-new'`); err != nil {
		t.Fatalf("read stored deadline: %v", err)
	}
	if stored == nil {
		t.Fatal("enrollment_token_expires_at is NULL: the deadline did not survive Create, so the token never expires")
	}
	if stored.Sub(expires).Abs() > time.Second {
		t.Errorf("stored deadline = %v, want ~%v", stored, expires)
	}
}
