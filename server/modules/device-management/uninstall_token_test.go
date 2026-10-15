package devicemanagement

import (
	"context"
	"testing"
	"time"
)

func TestUninstallToken_Lifecycle(t *testing.T) {
	d := newListTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	seedListDevice(t, d, "dev-uninst-1", "PC-UNINST", "online", false)

	plain := "UNINST-test12345678"
	hash := HashToken(plain)
	expiresAt := time.Now().UTC().Add(1 * time.Hour)

	// 1. Create uninstall token
	if err := repo.CreateUninstallToken(ctx, "dev-uninst-1", hash, "admin-user", expiresAt); err != nil {
		t.Fatalf("create uninstall token: %v", err)
	}

	// 2. Verify without consuming (console check)
	valid, err := repo.VerifyUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("verify uninstall token: %v", err)
	}
	if !valid {
		t.Errorf("expected token to be valid")
	}

	// 3. Verify wrong token fails
	validWrong, err := repo.VerifyUninstallToken(ctx, "dev-uninst-1", "WRONG-TOKEN")
	if err != nil {
		t.Fatalf("verify wrong token: %v", err)
	}
	if validWrong {
		t.Errorf("expected wrong token to be invalid")
	}

	// 4. Verify and consume (agent uninstaller check - step 1: GUI page)
	consumed, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("verify and consume: %v", err)
	}
	if !consumed {
		t.Errorf("expected token to be successfully consumed")
	}

	// 5. Subsequent verify calls within grace period succeed (step 2: Section Uninstall & step 3: service uninstall)
	consumedStep2, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("replayed token check in grace period: %v", err)
	}
	if !consumedStep2 {
		t.Errorf("expected token to remain valid within uninstallation grace period")
	}

	// Read-only check within grace period returns valid
	validInGrace, err := repo.VerifyUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("check in grace period: %v", err)
	}
	if !validInGrace {
		t.Errorf("expected token in grace period to show as valid")
	}

	// 6. Simulate grace window expiration (used_at older than 15 minutes)
	_, err = d.Exec(`UPDATE device_uninstall_tokens SET used_at = ? WHERE device_id = ?`,
		time.Now().UTC().Add(-16*time.Minute), "dev-uninst-1")
	if err != nil {
		t.Fatalf("simulate grace expiry: %v", err)
	}

	// 7. Token must be rejected after grace window has elapsed (replay protection)
	expiredGrace, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("check after grace period: %v", err)
	}
	if expiredGrace {
		t.Errorf("expected token after grace period to be rejected")
	}

	// Console check also shows invalid now
	validAfterGrace, err := repo.VerifyUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("check after grace: %v", err)
	}
	if validAfterGrace {
		t.Errorf("expected used token after grace period to show as invalid")
	}

	// 8. Reject check for a different device ID
	validDiffDev, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-diff", plain)
	if err != nil {
		t.Fatalf("check different device: %v", err)
	}
	if validDiffDev {
		t.Errorf("expected token to be rejected for different device ID")
	}
}

func TestUninstallToken_ExpiredToken(t *testing.T) {
	d := newListTestDB(t)
	repo := NewRepository(d)
	ctx := context.Background()

	seedListDevice(t, d, "dev-uninst-2", "PC-EXPIRED", "online", false)

	plain := "UNINST-expired12345"
	hash := HashToken(plain)
	// Expired 5 minutes ago
	expiredAt := time.Now().UTC().Add(-5 * time.Minute)

	if err := repo.CreateUninstallToken(ctx, "dev-uninst-2", hash, "admin-user", expiredAt); err != nil {
		t.Fatalf("create expired token: %v", err)
	}

	valid, err := repo.VerifyUninstallToken(ctx, "dev-uninst-2", plain)
	if err != nil {
		t.Fatalf("verify expired token: %v", err)
	}
	if valid {
		t.Errorf("expected expired token to be rejected")
	}

	consumed, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-2", plain)
	if err != nil {
		t.Fatalf("consume expired token: %v", err)
	}
	if consumed {
		t.Errorf("expected expired token consumption to be rejected")
	}
}
