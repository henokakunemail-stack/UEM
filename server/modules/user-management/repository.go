package usermgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound      = errors.New("user not found")
	ErrDuplicate     = errors.New("username already exists")
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

func (r *Repository) GetByID(ctx context.Context, id string) (*User, error) {
	query := `SELECT id, username, password_hash, role,
		COALESCE(display_name, '') as display_name,
		COALESCE(is_active, 1) as is_active,
		last_login_at, created_at, updated_at
		FROM users WHERE id = ?`
	var u User
	if err := r.db.GetContext(ctx, &u, query, id); err != nil {
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

func (r *Repository) Update(ctx context.Context, id string, req UpdateUserRequest) error {
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

	args = append(args, id)
	query := fmt.Sprintf("UPDATE users SET %s WHERE id = ?", setClauses)
	res, err := r.db.ExecContext(ctx, query, args...)
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

func (r *Repository) Deactivate(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		"UPDATE users SET is_active = 0, updated_at = ? WHERE id = ?", now, id)
	if err != nil {
		return fmt.Errorf("deactivate user: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) UpdateLastLogin(ctx context.Context, id string) error {
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx,
		"UPDATE users SET last_login_at = ? WHERE id = ?", now, id)
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && (errors.Is(err, sql.ErrNoRows) ||
		fmt.Sprintf("%v", err) == "UNIQUE constraint failed: users.username" ||
		contains(err.Error(), "UNIQUE constraint"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
