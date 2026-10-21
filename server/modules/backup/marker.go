package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rs/zerolog/log"
)

// A restore replaces the database and then the process exits, so nothing that
// happens during it can be recorded in the database: the old one is being
// discarded and the new one is not open yet. The restore marker is a small file
// next to the database that carries the outcome across the restart. The next
// start writes the audit entry from it (into the database that is now live),
// and the console reads it to learn whether the restore it asked for worked.
const markerName = "restore-status.json"

// Marker states and error codes.
const (
	RestorePending   = "pending"
	RestoreSucceeded = "succeeded"
	RestoreFailed    = "failed"

	ErrCodeRestoreFailed  = "restore_failed"  // restore failed, previous database put back
	ErrCodeRollbackFailed = "rollback_failed" // restore failed and putting it back failed too
	ErrCodeInterrupted    = "interrupted"     // the process died while restoring
)

// RestoreMarker is the outcome of the last restore.
type RestoreMarker struct {
	ID           string     `json:"id"`
	State        string     `json:"state"`
	Source       string     `json:"source"`
	SafetyBackup string     `json:"safety_backup"`
	ActorID      string     `json:"actor_id"`
	RequestedAt  time.Time  `json:"requested_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	ErrorCode    string     `json:"error_code,omitempty"`
	Error        string     `json:"error,omitempty"`
	RolledBack   bool       `json:"rolled_back,omitempty"`
	Audited      bool       `json:"audited"`
}

func markerPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), markerName)
}

func newRestoreID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// writeMarker replaces the marker atomically: a crash mid-write leaves the
// previous marker, never half of one.
func writeMarker(dbPath string, m *RestoreMarker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	final := markerPath(dbPath)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write restore marker: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return fmt.Errorf("write restore marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync restore marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close restore marker: %w", err)
	}
	return os.Rename(tmp, final)
}

// ReadRestoreMarker returns the last restore's marker, or nil when there has
// never been one.
func ReadRestoreMarker(dbPath string) (*RestoreMarker, error) {
	b, err := os.ReadFile(markerPath(dbPath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m RestoreMarker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read restore marker: %w", err)
	}
	return &m, nil
}

// FinalizeRestoreMarker runs once at startup, after the database is open and
// migrated. A marker still "pending" means the process died mid-restore; it is
// recorded as failed/interrupted. An outcome not yet in the audit trail is
// written there, under the operator who asked for the restore, and only then
// marked audited -- so a crash between the two can duplicate the entry but
// never lose it.
func FinalizeRestoreMarker(ctx context.Context, dbPath string, a Auditor) (*RestoreMarker, error) {
	m, err := ReadRestoreMarker(dbPath)
	if err != nil || m == nil {
		return m, err
	}
	changed := false
	if m.State == RestorePending {
		now := time.Now().UTC()
		m.State, m.ErrorCode, m.FinishedAt = RestoreFailed, ErrCodeInterrupted, &now
		m.Error = "the server stopped while restoring; the database is whichever copy was in place at that moment"
		changed = true
	}
	if !m.Audited && a != nil {
		action := "system.backup_restore_completed"
		if m.State != RestoreSucceeded {
			action = "system.backup_restore_failed"
		}
		details := map[string]string{
			"restore_id":    m.ID,
			"source":        m.Source,
			"safety_backup": m.SafetyBackup,
			"state":         m.State,
		}
		if m.ErrorCode != "" {
			details["error_code"] = m.ErrorCode
			details["error"] = m.Error
			details["rolled_back"] = fmt.Sprint(m.RolledBack)
		}
		if err := a.Log(ctx, "user", m.ActorID, action, m.Source, details); err != nil {
			log.Warn().Err(err).Msg("restore: could not write the audit entry; will retry at next start")
		} else {
			m.Audited = true
			changed = true
		}
	}
	if changed {
		if err := writeMarker(dbPath, m); err != nil {
			return m, err
		}
	}
	return m, nil
}
