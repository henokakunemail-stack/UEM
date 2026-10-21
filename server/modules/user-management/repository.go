package usermgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

var (
	ErrNotFound  = errors.New("user not found")
	ErrDuplicate = errors.New("username already exists")
	// ErrLastAdmin refuses a change that would leave no active administrator:
	// nobody could then manage users, and the console would be locked for good.
	ErrLastAdmin = errors.New("at least one active administrator must remain")
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Create(ctx context.Context, u *User) error {
	query := `
		INSERT INTO users (id, username, password_hash, role, display_name, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	isActive := 1
	if !u.IsActive {
		isActive = 0
	}
	_, err := r.db.ExecContext(ctx, query,
		u.ID, u.Username, u.PasswordHash, u.Role, u.DisplayName, isActive, u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

const getUserQuery = `SELECT id, username, password_hash, role,
		COALESCE(display_name, '') as display_name,
		COALESCE(is_active, 1) as is_active,
		last_login_at, created_at, updated_at
		FROM users WHERE id = ?`

func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	return getUser(ctx, r.db, id)
}

func getUser(ctx context.Context, q sqlx.QueryerContext, id string) (*User, error) {
	var u User
	if err := sqlx.GetContext(ctx, q, &u, getUserQuery, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

func (r *Repository) GetByUsername(ctx context.Context, username string) (*User, error) {
	query := `SELECT id, username, password_hash, role,
		COALESCE(display_name, '') as display_name,
		COALESCE(is_active, 1) as is_active,
		last_login_at, created_at, updated_at
		FROM users WHERE username = ?`
	var u User
	if err := r.db.GetContext(ctx, &u, query, username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return &u, nil
}

func (r *Repository) List(ctx context.Context, limit int) ([]User, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, username, role,
		COALESCE(display_name, '') as display_name,
		COALESCE(is_active, 1) as is_active,
		last_login_at, created_at, updated_at
		FROM users ORDER BY created_at ASC LIMIT ?`
	users := []User{}
	if err := r.db.SelectContext(ctx, &users, query, limit); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// ErrNoFields is returned when an update names no column to change.
//
// Without it the query below degenerates to "SET updated_at = ?", which matches
// a row, touches nothing, and reports success. The console then shows the edit
// as saved while nothing changed -- a silent no-op that is indistinguishable, to
// the operator, from a working one.
var ErrNoFields = errors.New("no fields to update")

func (r *Repository) Update(ctx context.Context, id string, req UpdateUserRequest) error {
	return updateUser(ctx, r.db, id, req)
}

func updateUser(ctx context.Context, ex sqlx.ExecerContext, id string, req UpdateUserRequest) error {
	now := time.Now().UTC()
	setClauses := "updated_at = ?"
	args := []any{now}

	if req.Role != nil {
		setClauses += ", role = ?"
		args = append(args, *req.Role)
	}
	if req.DisplayName != nil {
		setClauses += ", display_name = ?"
		args = append(args, *req.DisplayName)
	}
	if req.IsActive != nil {
		isActive := 0
		if *req.IsActive {
			isActive = 1
		}
		setClauses += ", is_active = ?"
		args = append(args, isActive)
	}

	if len(args) == 1 {
		return ErrNoFields
	}

	args = append(args, id)
	query := fmt.Sprintf("UPDATE users SET %s WHERE id = ?", setClauses)
	res, err := ex.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) UpdatePassword(ctx context.Context, id, hash string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		hash, now, id)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateGuarded applies req unless it would leave no active administrator.
// check runs against the row as it was, inside the same transaction, for rules
// that depend on it (an operator changing their own role). It returns the row
// before and after the change.
//
// The count and the write are one unit. Checking first and writing after would
// let two admins demote each other at the same moment, each seeing the other
// still active: on SQLite the IMMEDIATE transaction serialises writers, and on
// PostgreSQL -- where READ COMMITTED alone would allow it -- the advisory lock
// serialises every server node.
func (r *Repository) UpdateGuarded(ctx context.Context, id string, req UpdateUserRequest,
	check func(prior *User) error) (prior, updated *User, err error) {
	err = db.WithAdvisoryLock(ctx, r.db, db.LockUserAdmins, func() error {
		tx, err := r.db.BeginTxx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		p, err := getUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if check != nil {
			if err := check(p); err != nil {
				return err
			}
		}
		if removesActiveAdmin(p, req) {
			var others int
			if err := tx.GetContext(ctx, &others, `SELECT COUNT(*) FROM users
				WHERE role = 'admin' AND COALESCE(is_active, 1) = 1 AND id <> ?`, id); err != nil {
				return fmt.Errorf("count admins: %w", err)
			}
			if others == 0 {
				return ErrLastAdmin
			}
		}
		if err := updateUser(ctx, tx, id, req); err != nil {
			return err
		}
		u, err := getUser(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		prior, updated = p, u
		return nil
	})
	return prior, updated, err
}

// removesActiveAdmin reports whether req takes an active admin out of the set
// of active admins, by demotion or deactivation.
func removesActiveAdmin(prior *User, req UpdateUserRequest) bool {
	if prior.Role != "admin" || !prior.IsActive {
		return false
	}
	demoted := req.Role != nil && *req.Role != "admin"
	deactivated := req.IsActive != nil && !*req.IsActive
	return demoted || deactivated
}

// Deactivate turns an account off, with the same last-admin guard as an edit.
func (r *Repository) Deactivate(ctx context.Context, id string) error {
	off := false
	_, _, err := r.UpdateGuarded(ctx, id, UpdateUserRequest{IsActive: &off}, nil)
	return err
}

func (r *Repository) UpdateLastLogin(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET last_login_at = ? WHERE id = ?", now, id)
	return err
}

// isUniqueViolation reports whether err is the database refusing a duplicate
// username.
//
// The sql.ErrNoRows test that used to lead this list was wrong and not merely
// useless: ErrNoRows means "the query matched nothing", which is what every
// successful lookup returns nothing for, and no driver raises it from a failed
// INSERT. A branch that can never be taken was carrying the illusion that this
// function had been thought about. strings.Contains does the job the two
// hand-rolled substring loops were doing, and the exact-message comparison was
// redundant with it.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint")
}
