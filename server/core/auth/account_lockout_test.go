package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Failures count per account, wherever they come from, and lock it at the
// limit. This is what a second server node and an attacker rotating source
// addresses can no longer get around.
func TestAnAccountLocksAfterTheFailureLimit(t *testing.T) {
	e := newEnv(t)
	lock := accountLockout{db: e.db}
	ctx := context.Background()

	for i := 0; i < AccountMaxFailures-1; i++ {
		lock.recordFailure(ctx, "Alice")
	}
	if wait := lock.lockedFor(ctx, "alice"); wait != 0 {
		t.Fatalf("locked after %d failures, limit is %d", AccountMaxFailures-1, AccountMaxFailures)
	}
	lock.recordFailure(ctx, "ALICE") // same account, any case
	if wait := lock.lockedFor(ctx, "alice"); wait <= 0 || wait > AccountLockout {
		t.Fatalf("not locked at the limit (wait=%v)", wait)
	}
	if wait := lock.lockedFor(ctx, "bob"); wait != 0 {
		t.Fatal("another account was locked")
	}

	lock.reset(ctx, "alice")
	if wait := lock.lockedFor(ctx, "alice"); wait != 0 {
		t.Fatal("still locked after a successful sign-in reset")
	}
}

// Concurrent failures -- two nodes counting the same account -- are all counted.
func TestConcurrentFailuresAreAllCounted(t *testing.T) {
	e := newEnv(t)
	lock := accountLockout{db: e.db}
	var wg sync.WaitGroup
	for i := 0; i < AccountMaxFailures; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock.recordFailure(context.Background(), "carol")
		}()
	}
	wg.Wait()
	var failures int
	if err := e.db.Get(&failures, `SELECT failures FROM login_failures WHERE username = 'carol'`); err != nil {
		t.Fatal(err)
	}
	if failures != AccountMaxFailures {
		t.Fatalf("%d concurrent failures recorded as %d", AccountMaxFailures, failures)
	}
}

// A locked account is refused even with the right password, and the refusal is
// the same 429 the per-IP limiter gives, so it reveals nothing about the
// password.
func TestALockedAccountIsRefusedWithTheRightPassword(t *testing.T) {
	e := newEnv(t)
	e.seedUser("dave", "correct-horse", "admin")
	if _, err := e.db.Exec(`INSERT INTO login_failures (username, failures, window_started_at, locked_until) VALUES ('dave', ?, ?, ?)`,
		AccountMaxFailures, time.Now().UTC(), time.Now().UTC().Add(AccountLockout)); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"username": "dave", "password": "correct-horse"})
	resp, err := http.Post(e.srv.URL+"/api/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("locked account: status %d, Retry-After %q; want 429 with a wait", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}
