package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func TestSessionStore_CacheHitAndInvalidation(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "cache_test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()

	store := NewSessionStore(d, time.Hour)
	ctx := context.Background()

	// Seed session
	jti := "test-jti-cache-1"
	if err := store.Create(ctx, "user-1", "alice", "admin", jti, "test-agent", "127.0.0.1"); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// First read - cache miss, loads from DB into cache
	live, err := store.IsLive(ctx, jti)
	if err != nil || !live {
		t.Fatalf("expected live=true, err=nil; got live=%v, err=%v", live, err)
	}

	// Verify cached
	store.cache.mu.RLock()
	entry, ok := store.cache.items[jti]
	store.cache.mu.RUnlock()
	if !ok || !entry.live {
		t.Fatalf("expected jti to be cached with live=true")
	}

	// Revoke - must invalidate cache
	if err := store.Revoke(ctx, jti); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	store.cache.mu.RLock()
	_, okAfterRevoke := store.cache.items[jti]
	store.cache.mu.RUnlock()
	if okAfterRevoke {
		t.Fatalf("expected cache entry to be purged after revoke")
	}

	// Next read - queries DB, sees revoked, caches false
	liveAfter, err := store.IsLive(ctx, jti)
	if err != nil || liveAfter {
		t.Fatalf("expected live=false, err=nil; got live=%v, err=%v", liveAfter, err)
	}

	// Verify negative cache
	store.cache.mu.RLock()
	negEntry, okNeg := store.cache.items[jti]
	store.cache.mu.RUnlock()
	if !okNeg || negEntry.live {
		t.Fatalf("expected negative cache entry with live=false")
	}
}
