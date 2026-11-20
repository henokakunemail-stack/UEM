package devicemanagement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// Posture summary values, as stored in device_posture's summary columns.
const (
	postureOn            = "on"
	posturePartial       = "partial"
	postureOff           = "off"
	postureUnknown       = protocol.PostureUnknown
	postureNotApplicable = protocol.PostureNotApplicable
	postureReported      = protocol.PostureReported
)

// postureSummary is the one-word state per control that lists filter on.
type postureSummary struct {
	DiskEncryption string `json:"disk_encryption" db:"disk_encryption"`
	Antivirus      string `json:"antivirus" db:"antivirus"`
	Firewall       string `json:"firewall" db:"firewall"`
	PasswordPolicy string `json:"password_policy" db:"password_policy"`
	ScreenLock     string `json:"screen_lock" db:"screen_lock"`
}

// controlState passes through the two "could not say" states and maps
// anything the server does not recognise to unknown. The agent's report is
// input from the device: a value outside the contract must not slip through as
// something a report reads as a pass.
func controlState(s string) (string, bool) {
	switch s {
	case protocol.PostureReported:
		return "", true
	case protocol.PostureNotApplicable:
		return postureNotApplicable, false
	default:
		return postureUnknown, false
	}
}

func onPartialOff(on, total int) string {
	switch {
	case total == 0:
		return postureUnknown
	case on == total:
		return postureOn
	case on == 0:
		return postureOff
	default:
		return posturePartial
	}
}

// summarizePosture reduces a posture snapshot to its summary columns.
func summarizePosture(p protocol.Posture) postureSummary {
	var s postureSummary

	if st, ok := controlState(p.DiskEncryption.State); !ok {
		s.DiskEncryption = st
	} else {
		on := 0
		for _, v := range p.DiskEncryption.Volumes {
			if v.Protected {
				on++
			}
		}
		s.DiskEncryption = onPartialOff(on, len(p.DiskEncryption.Volumes))
	}

	if st, ok := controlState(p.Antivirus.State); !ok {
		s.Antivirus = st
	} else {
		// One active product is protection; none at all is off.
		s.Antivirus = postureOff
		for _, a := range p.Antivirus.Products {
			if a.Enabled != nil && *a.Enabled {
				s.Antivirus = postureOn
				break
			}
		}
	}

	if st, ok := controlState(p.Firewall.State); !ok {
		s.Firewall = st
	} else {
		on := 0
		for _, f := range p.Firewall.Profiles {
			if f.Enabled {
				on++
			}
		}
		s.Firewall = onPartialOff(on, len(p.Firewall.Profiles))
	}

	if st, ok := controlState(p.PasswordPolicy.State); !ok {
		s.PasswordPolicy = st
	} else {
		s.PasswordPolicy = postureReported
	}
	if st, ok := controlState(p.ScreenLock.State); !ok {
		s.ScreenLock = st
	} else {
		s.ScreenLock = postureReported
	}
	return s
}

// storePosture saves the latest posture and, when its summary changed, a
// history row. A first report counts as a change.
func (r *inventoryRepository) storePosture(ctx context.Context, deviceID string, p protocol.Posture, collectedAt time.Time) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	sum := summarizePosture(p)
	now := time.Now().UTC()
	if collectedAt.IsZero() {
		collectedAt = now
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var prev postureSummary
	err = tx.GetContext(ctx, &prev, `SELECT disk_encryption, antivirus, firewall, password_policy, screen_lock
		FROM device_posture WHERE device_id = ?`, deviceID)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return fmt.Errorf("read previous posture: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO device_posture
			(device_id, posture_json, disk_encryption, antivirus, firewall, password_policy, screen_lock, collected_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			posture_json = excluded.posture_json,
			disk_encryption = excluded.disk_encryption,
			antivirus = excluded.antivirus,
			firewall = excluded.firewall,
			password_policy = excluded.password_policy,
			screen_lock = excluded.screen_lock,
			collected_at = excluded.collected_at,
			updated_at = excluded.updated_at`,
		deviceID, string(raw), sum.DiskEncryption, sum.Antivirus, sum.Firewall, sum.PasswordPolicy, sum.ScreenLock,
		collectedAt, now); err != nil {
		return fmt.Errorf("upsert posture: %w", err)
	}

	if first || prev != sum {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO device_posture_history
				(id, device_id, disk_encryption, antivirus, firewall, password_policy, screen_lock, changed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			NewID(), deviceID, sum.DiskEncryption, sum.Antivirus, sum.Firewall, sum.PasswordPolicy, sum.ScreenLock,
			collectedAt); err != nil {
			return fmt.Errorf("insert posture history: %w", err)
		}
	}
	return tx.Commit()
}

type storedPosture struct {
	PostureJSON string    `db:"posture_json"`
	CollectedAt time.Time `db:"collected_at"`
	postureSummary
}

type postureHistoryRow struct {
	postureSummary
	ChangedAt time.Time `json:"changed_at" db:"changed_at"`
}

const postureHistoryLimit = 50

func (r *inventoryRepository) getPosture(ctx context.Context, deviceID string) (storedPosture, []postureHistoryRow, error) {
	var sp storedPosture
	err := r.db.GetContext(ctx, &sp, `SELECT posture_json, collected_at, disk_encryption, antivirus, firewall,
		password_policy, screen_lock FROM device_posture WHERE device_id = ?`, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return sp, nil, ErrNotFound
	}
	if err != nil {
		return sp, nil, fmt.Errorf("get posture %s: %w", deviceID, err)
	}
	var hist []postureHistoryRow
	if err := r.db.SelectContext(ctx, &hist, `SELECT disk_encryption, antivirus, firewall, password_policy,
		screen_lock, changed_at FROM device_posture_history WHERE device_id = ?
		ORDER BY changed_at DESC LIMIT ?`, deviceID, postureHistoryLimit); err != nil {
		return sp, nil, fmt.Errorf("get posture history %s: %w", deviceID, err)
	}
	return sp, hist, nil
}

// postureResponse is GET /api/devices/{id}/posture.
type postureResponse struct {
	// Reported is false when the agent has never sent posture: it predates the
	// feature or runs on a platform whose collector does not read it yet.
	Reported    bool                `json:"reported"`
	Reason      string              `json:"reason,omitempty"`
	Summary     *postureSummary     `json:"summary,omitempty"`
	Posture     json.RawMessage     `json:"posture,omitempty"`
	CollectedAt *time.Time          `json:"collected_at,omitempty"`
	History     []postureHistoryRow `json:"history,omitempty"`
}

func (h *inventoryHandler) getPosture(w http.ResponseWriter, r *http.Request) {
	deviceID := chi.URLParam(r, "id")
	sp, hist, err := h.repo.getPosture(r.Context(), deviceID)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusOK, postureResponse{Reported: false,
			Reason: "this device's agent has not reported security posture; it needs an agent version that collects it"})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	sum := sp.postureSummary
	at := sp.CollectedAt
	writeJSON(w, http.StatusOK, postureResponse{
		Reported: true, Summary: &sum, Posture: json.RawMessage(sp.PostureJSON),
		CollectedAt: &at, History: hist,
	})
}
