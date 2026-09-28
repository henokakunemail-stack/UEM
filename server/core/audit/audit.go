package audit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jmoiron/sqlx"
)

// Log writes one audit trail entry. details may be nil.
// actorType: user | agent | system. action examples: "auth.login", "device.enroll".
func Log(ctx context.Context, db *sqlx.DB, actorType, actorID, action, targetID string, details any) error {
	var detailJSON any
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil {
			return err
		}
		detailJSON = string(b)
	}
	_, err := db.ExecContext(ctx, `
		INSERT INTO audit_logs (id, actor_type, actor_id, action, target_id, details, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		newID(), actorType, actorID, action, targetID, detailJSON, time.Now().UTC())
	return err
}

// Entry is one audit log row.
type Entry struct {
	ID        string    `db:"id" json:"id"`
	ActorType string    `db:"actor_type" json:"actor_type"`
	ActorID   string    `db:"actor_id" json:"actor_id"`
	Action    string    `db:"action" json:"action"`
	TargetID  string    `db:"target_id" json:"target_id"`
	Details   string    `db:"details" json:"details"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// List returns the most recent entries.
func List(ctx context.Context, db *sqlx.DB, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []Entry
	err := db.SelectContext(ctx, &rows,
		`SELECT id, actor_type, COALESCE(actor_id, '') AS actor_id, action,
		        COALESCE(target_id, '') AS target_id,
		        COALESCE(details, '{}') AS details, created_at
		 FROM audit_logs ORDER BY created_at DESC LIMIT ?`, limit)
	return rows, err
}

func newID() string {
	b := make([]byte, 16)
	// crypto/rand is already validated at startup; a failure here is a fatal bug.
	if _, err := readRand(b); err != nil {
		panic(err)
	}
	return hexEncode(b)
}
