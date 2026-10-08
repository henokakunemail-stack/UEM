package remoteexec

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmoiron/sqlx"
)

var (
	ErrNotFound = errors.New("not found")
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateExecution(ctx context.Context, e *RemoteExecution) error {
	query := `
		INSERT INTO remote_executions (
			id, device_id, operator_id, shell_type, command_text,
			status, exit_code, output, error_message, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, query,
		e.ID, e.DeviceID, e.OperatorID, e.ShellType, e.CommandText,
		e.Status, e.ExitCode, e.Output, e.ErrorMessage, e.StartedAt.UTC(),
	)
	return err
}

func (r *Repository) GetExecution(ctx context.Context, id string) (*RemoteExecution, error) {
	query := `
		SELECT
			e.id, e.device_id, e.operator_id, e.shell_type, e.command_text,
			e.status, e.exit_code, e.output, e.error_message, e.started_at, e.completed_at,
			COALESCE(u.username, 'unknown') AS operator_name,
			COALESCE(d.hostname, 'unknown') AS hostname
		FROM remote_executions e
		LEFT JOIN users u ON u.id = e.operator_id
		LEFT JOIN devices d ON d.id = e.device_id
		WHERE e.id = ?`

	var e RemoteExecution
	if err := r.db.GetContext(ctx, &e, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

func (r *Repository) ListExecutions(ctx context.Context, deviceID string, limit int) ([]RemoteExecution, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `
		SELECT
			e.id, e.device_id, e.operator_id, e.shell_type, e.command_text,
			e.status, e.exit_code, e.output, e.error_message, e.started_at, e.completed_at,
			COALESCE(u.username, 'unknown') AS operator_name,
			COALESCE(d.hostname, 'unknown') AS hostname
		FROM remote_executions e
		LEFT JOIN users u ON u.id = e.operator_id
		LEFT JOIN devices d ON d.id = e.device_id
		WHERE e.device_id = ?
		ORDER BY e.started_at DESC
		LIMIT ?`

	rows := []RemoteExecution{}
	if err := r.db.SelectContext(ctx, &rows, query, deviceID, limit); err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *Repository) UpdateExecutionResult(ctx context.Context, deviceID string, report ExecResultReport) error {
	now := time.Now().UTC()
	query := `
		UPDATE remote_executions
		SET status = ?, exit_code = ?, output = ?, error_message = ?, completed_at = ?
		WHERE id = ? AND device_id = ?
		  AND status NOT IN ('completed', 'failed', 'timeout', 'cancelled')`
	res, err := r.db.ExecContext(ctx, query,
		report.Status, report.ExitCode, report.Output, report.ErrorMessage, now,
		report.ExecutionID, deviceID,
	)
	if err != nil {
		return err
	}
	// No rows means either that no such execution exists, that it belongs to
	// another device, or that it is already in a terminal state. All three
	// answer the same way, so this cannot be used to enumerate which execution
	// ids the fleet has.
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) CreateTerminalSession(ctx context.Context, s *TerminalSession) error {
	query := `
		INSERT INTO terminal_sessions (
			id, device_id, operator_id, shell_type, status, created_at
		) VALUES (?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, query,
		s.ID, s.DeviceID, s.OperatorID, s.ShellType, s.Status, s.CreatedAt.UTC(),
	)
	return err
}

func (r *Repository) CloseTerminalSession(ctx context.Context, id string) error {
	now := time.Now().UTC()
	query := `
		UPDATE terminal_sessions
		SET status = 'closed', closed_at = ?
		WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, now, id)
	return err
}

func (r *Repository) GetTerminalSession(ctx context.Context, id string) (*TerminalSession, error) {
	query := `
		SELECT
			t.id, t.device_id, t.operator_id, t.shell_type, t.status, t.created_at, t.closed_at,
			COALESCE(u.username, 'unknown') AS operator_name,
			COALESCE(d.hostname, 'unknown') AS hostname
		FROM terminal_sessions t
		LEFT JOIN users u ON u.id = t.operator_id
		LEFT JOIN devices d ON d.id = t.device_id
		WHERE t.id = ?`

	var s TerminalSession
	if err := r.db.GetContext(ctx, &s, query, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *Repository) ListTerminalSessions(ctx context.Context, deviceID string, limit int) ([]TerminalSession, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	query := `
		SELECT
			t.id, t.device_id, t.operator_id, t.shell_type, t.status, t.created_at, t.closed_at,
			COALESCE(u.username, 'unknown') AS operator_name,
			COALESCE(d.hostname, 'unknown') AS hostname
		FROM terminal_sessions t
		LEFT JOIN users u ON u.id = t.operator_id
		LEFT JOIN devices d ON d.id = t.device_id
		WHERE t.device_id = ?
		ORDER BY t.created_at DESC
		LIMIT ?`

	rows := []TerminalSession{}
	if err := r.db.SelectContext(ctx, &rows, query, deviceID, limit); err != nil {
		return nil, err
	}
	return rows, nil
}
