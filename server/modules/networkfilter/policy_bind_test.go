package networkfilter

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

var (
	bindDriverOnce sync.Once
	mockConnInst   = &mockBindConn{}
)

type mockBindDriver struct{}

func (d *mockBindDriver) Open(name string) (driver.Conn, error) {
	return mockConnInst, nil
}

type mockBindConn struct {
	mu       sync.Mutex
	lastArgs []any
}

func (c *mockBindConn) Prepare(query string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not supported")
}

func (c *mockBindConn) Close() error {
	return nil
}

func (c *mockBindConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("tx not supported")
}

func (c *mockBindConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastArgs = make([]any, len(args))
	for i, a := range args {
		c.lastArgs[i] = a.Value
	}
	return driver.RowsAffected(1), nil
}

func setupMockDB(t *testing.T) (*Repository, *mockBindConn) {
	t.Helper()
	bindDriverOnce.Do(func() {
		sql.Register("networkfilter-bind-mock", &mockBindDriver{})
	})

	db, err := sql.Open("networkfilter-bind-mock", "")
	if err != nil {
		t.Fatalf("open mock db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := NewRepository(sqlx.NewDb(db, "postgres"))
	return repo, mockConnInst
}

func TestPolicyRepository_PostgresIntegerBinding(t *testing.T) {
	repo, mock := setupMockDB(t)
	ctx := context.Background()

	t.Run("CreatePolicy binds integer 1 for IsEnabled true", func(t *testing.T) {
		policy := &FilterPolicy{
			ID:          "pol-bind-1",
			Name:        "Test Policy",
			Description: "desc",
			TargetType:  "all",
			TargetID:    "",
			IsEnabled:   true,
			Priority:    100,
			CreatedBy:   "user-1",
		}

		if err := repo.CreatePolicy(ctx, policy); err != nil {
			t.Fatalf("CreatePolicy failed: %v", err)
		}

		mock.mu.Lock()
		args := mock.lastArgs
		mock.mu.Unlock()

		if len(args) < 6 {
			t.Fatalf("expected at least 6 args, got %d", len(args))
		}

		// 6th argument is is_enabled ($6 in INSERT statement)
		enabledArg := args[5]
		switch v := enabledArg.(type) {
		case int:
			if v != 1 {
				t.Fatalf("expected is_enabled to be 1, got %d", v)
			}
		case int64:
			if v != 1 {
				t.Fatalf("expected is_enabled to be 1, got %d", v)
			}
		case bool:
			t.Fatalf("FATAL: is_enabled bound as Go bool (%v); PostgreSQL rejects this with 22P02. Must bind int (1 or 0)", v)
		default:
			t.Fatalf("unexpected type for is_enabled: %T (%v)", enabledArg, enabledArg)
		}
	})

	t.Run("UpdatePolicy binds integer 0 for IsEnabled false", func(t *testing.T) {
		policy := &FilterPolicy{
			ID:          "pol-bind-2",
			Name:        "Test Policy",
			Description: "desc",
			TargetType:  "all",
			TargetID:    "",
			IsEnabled:   false,
			Priority:    100,
		}

		if err := repo.UpdatePolicy(ctx, policy); err != nil {
			t.Fatalf("UpdatePolicy failed: %v", err)
		}

		mock.mu.Lock()
		args := mock.lastArgs
		mock.mu.Unlock()

		if len(args) < 5 {
			t.Fatalf("expected at least 5 args, got %d", len(args))
		}

		// 5th argument is is_enabled ($5 in UPDATE statement)
		enabledArg := args[4]
		switch v := enabledArg.(type) {
		case int:
			if v != 0 {
				t.Fatalf("expected is_enabled to be 0, got %d", v)
			}
		case int64:
			if v != 0 {
				t.Fatalf("expected is_enabled to be 0, got %d", v)
			}
		case bool:
			t.Fatalf("FATAL: is_enabled bound as Go bool (%v); PostgreSQL rejects this with 22P02. Must bind int (1 or 0)", v)
		default:
			t.Fatalf("unexpected type for is_enabled: %T (%v)", enabledArg, enabledArg)
		}
	})
}
