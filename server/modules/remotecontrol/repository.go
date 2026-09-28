package remotecontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type RemoteControlSession struct {
	ID               string     `json:"id" db:"id"`
	DeviceID         string     `json:"device_id" db:"device_id"`
	OperatorID       string     `json:"operator_id" db:"operator_id"`
	SessionMode      string     `json:"session_mode" db:"session_mode"` // 'full_control', 'view_only'
	Status           string     `json:"status" db:"status"`             // 'active', 'ended', 'rejected'
	FramesTransmitted int       `json:"frames_transmitted" db:"frames_transmitted"`
	BytesTransmitted  int64     `json:"bytes_transmitted" db:"bytes_transmitted"`
	InputEventsCount int        `json:"input_events_count" db:"input_events_count"`
	StartedAt        time.Time  `json:"started_at" db:"started_at"`
	EndedAt          *time.Time `json:"ended_at" db:"ended_at"`
	CreatedAt        time.Time  `json:"created_at" db:"created_at"`

	// Joined
	Hostname     string `json:"hostname,omitempty" db:"hostname"`
	Site         string `json:"site,omitempty" db:"site"`
	OperatorName string `json:"operator_name,omitempty" db:"operator_name"`
}

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Repository) CreateSession(ctx context.Context, s *RemoteControlSession) error {
	now := time.Now().UTC()
	if s.ID == "" {
		s.ID = NewID()
	}
	s.Status = "active"
	s.StartedAt = now
	s.CreatedAt = now

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO remote_control_sessions (
			id, device_id, operator_id, session_mode, status,
			frames_transmitted, bytes_transmitted, input_events_count,
			started_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, s.ID, s.DeviceID, s.OperatorID, s.SessionMode, s.Status,
		s.FramesTransmitted, s.BytesTransmitted, s.InputEventsCount,
		s.StartedAt, s.CreatedAt)
	if err != nil {
		return fmt.Errorf("create rc session: %w", err)
	}
	return nil
}

func (r *Repository) GetSessionByID(ctx context.Context, id string) (*RemoteControlSession, error) {
	var s RemoteControlSession
	query := `
		SELECT
			s.id, s.device_id, s.operator_id, s.session_mode, s.status,
			s.frames_transmitted, s.bytes_transmitted, s.input_events_count,
			s.started_at, s.ended_at, s.created_at,
			COALESCE(d.hostname, '') as hostname,
			COALESCE(d.site, '') as site,
			COALESCE(u.username, '') as operator_name
		FROM remote_control_sessions s
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN users u ON u.id = s.operator_id
		WHERE s.id = ?
	`
	if err := r.db.GetContext(ctx, &s, query, id); err != nil {
		return nil, fmt.Errorf("get rc session: %w", err)
	}
	return &s, nil
}

func (r *Repository) EndSession(ctx context.Context, id string, frames int, bytes int64, inputEvents int) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE remote_control_sessions SET
			status = 'ended',
			frames_transmitted = ?,
			bytes_transmitted = ?,
			input_events_count = ?,
			ended_at = ?
		WHERE id = ? AND status = 'active'
	`, frames, bytes, inputEvents, now, id)
	if err != nil {
		return fmt.Errorf("end rc session: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("session not found or already ended")
	}
	return nil
}

func (r *Repository) ListSessionsByDevice(ctx context.Context, deviceID string, limit int) ([]RemoteControlSession, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	sessions := []RemoteControlSession{}
	query := `
		SELECT
			s.id, s.device_id, s.operator_id, s.session_mode, s.status,
			s.frames_transmitted, s.bytes_transmitted, s.input_events_count,
			s.started_at, s.ended_at, s.created_at,
			COALESCE(d.hostname, '') as hostname,
			COALESCE(d.site, '') as site,
			COALESCE(u.username, '') as operator_name
		FROM remote_control_sessions s
		LEFT JOIN devices d ON d.id = s.device_id
		LEFT JOIN users u ON u.id = s.operator_id
		WHERE s.device_id = ?
		ORDER BY s.started_at DESC LIMIT ?
	`
	if err := r.db.SelectContext(ctx, &sessions, query, deviceID, limit); err != nil {
		return nil, fmt.Errorf("list device rc sessions: %w", err)
	}
	return sessions, nil
}
