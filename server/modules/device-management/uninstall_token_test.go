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

	// 4. Verify and consume (agent uninstaller check)
	consumed, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("verify and consume: %v", err)
	}
	if !consumed {
		t.Errorf("expected token to be successfully consumed")
	}

	// 5. Replay attack: once consumed, token cannot be reused
	replayed, err := repo.VerifyAndConsumeUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("replayed token check: %v", err)
	}
	if replayed {
		t.Errorf("expected consumed token to be rejected on replay")
	}

	// Console check also shows invalid now
	validAfterUse, err := repo.VerifyUninstallToken(ctx, "dev-uninst-1", plain)
	if err != nil {
		t.Fatalf("check after use: %v", err)
	}
	if validAfterUse {
		t.Errorf("expected used token to show as invalid")
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
