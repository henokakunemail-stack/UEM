package devicemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// Repository handles device persistence.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// Create inserts a new device.
func (r *Repository) Create(ctx context.Context, d Device) error {
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
		                     last_seen_at, enrolled_at, enrollment_token_hash,
		                     enrollment_token_expires_at,
		                     device_secret_hash, site, created_at, updated_at)
		VALUES (:id, :hostname, :os_name, :os_version, :agent_version, :status,
		        :last_seen_at, :enrolled_at, :enrollment_token_hash,
		        :enrollment_token_expires_at,
		        :device_secret_hash, :site, :created_at, :updated_at)`, d)
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	return nil
}

// GetByID loads a device by ID.
func (r *Repository) GetByID(ctx context.Context, id string) (Device, error) {
	var d Device
	err := r.db.GetContext(ctx, &d, `SELECT * FROM devices WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("get device %s: %w", id, err)
	}
	return d, nil
}

// List returns devices, optionally filtered by status and/or site.
// Phase 2: retired devices are excluded — they are no longer fleet members, and
// including them would inflate every dashboard count. Use the audit trail to
// review retired devices.
func (r *Repository) List(ctx context.Context, status, site string) ([]Device, error) {
	q := `SELECT * FROM devices WHERE retired_at IS NULL`
	args := []any{}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if site != "" {
		q += ` AND site = ?`
		args = append(args, site)
	}
	q += ` ORDER BY hostname ASC`
	devices := []Device{}
	if err := r.db.SelectContext(ctx, &devices, q, args...); err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	return devices, nil
}

// ListPaged returns a page of devices and the total count matching the filters.
// Pagination is performed in SQL (LIMIT/OFFSET) so only the requested page is
// transferred from the database.
func (r *Repository) ListPaged(ctx context.Context, status, site string, limit, offset int) ([]Device, int, error) {
	// Retirement is a column, not a status value: the dashboard counts retired
	// endpoints as `retired_at IS NOT NULL`. Asking for status='retired' against
	// the default `retired_at IS NULL` predicate ANDs a false pair, so the
	// console's "Retired Only" filter and the dashboard's retired KPI link both
	// returned zero rows no matter how many devices had been decommissioned.
	where := `WHERE retired_at IS NULL`
	args := []any{}
	if status == "retired" {
		where = `WHERE retired_at IS NOT NULL`
	} else if status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}
	if site != "" {
		where += ` AND site = ?`
		args = append(args, site)
	}

	// Total count first (same filters, no LIMIT).
	var total int
	if err := r.db.GetContext(ctx, &total,
		`SELECT COUNT(*) FROM devices `+where, args...); err != nil {
		return nil, 0, fmt.Errorf("count devices: %w", err)
	}

	q := `SELECT * FROM devices ` + where + ` ORDER BY hostname ASC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	devices := []Device{}
	if err := r.db.SelectContext(ctx, &devices, q, args...); err != nil {
		return nil, 0, fmt.Errorf("list devices paged: %w", err)
	}
	return devices, total, nil
}

// UpdateStatus sets status and last_seen_at.
func (r *Repository) UpdateStatus(ctx context.Context, id, status string, lastSeen time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET status = ?, last_seen_at = ?, updated_at = ? WHERE id = ? AND retired_at IS NULL`,
		status, lastSeen, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update status %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateLastPatchScan stamps the device with the time of its most recent patch
// scan. Called when a scan report is accepted, so the stamp moves only when the
// server actually holds the result -- a scan that died on the device, or a
// report that failed to store, leaves the previous timestamp standing rather
// than claiming the machine was just examined.
func (r *Repository) UpdateLastPatchScan(ctx context.Context, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE devices SET last_patch_scan_at = ?, updated_at = ? WHERE id = ?`,
		at.UTC(), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update last patch scan %s: %w", id, err)
	}
	return nil
}

// UpdateOSInfo stores OS/agent details sent in the agent hello message.
func (r *Repository) UpdateOSInfo(ctx context.Context, id, osVersion, agentVersion string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET os_version = ?, agent_version = ?, updated_at = ? WHERE id = ?`,
		osVersion, agentVersion, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update os info %s: %w", id, err)
	}
	return nil
}

// UpdateSystemInfo syncs real machine hostname, OS name, OS version, and agent version
// reported by the agent in hello message. If hostname or osName are empty or unknown,
// the existing values are preserved.
func (r *Repository) UpdateSystemInfo(ctx context.Context, id, hostname, osName, osVersion, agentVersion string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE devices SET
			hostname = CASE WHEN ? != '' THEN ? ELSE hostname END,
			os_name = CASE WHEN ? != '' AND ? != 'unknown' THEN ? ELSE os_name END,
			os_version = ?,
			agent_version = ?,
			updated_at = ?
		WHERE id = ?`,
		hostname, hostname,
		osName, osName, osName,
		osVersion, agentVersion, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update system info %s: %w", id, err)
	}
	return nil
}

// UpdateDevice allows operators to manually rename device hostname or update site location.
func (r *Repository) UpdateDevice(ctx context.Context, id, hostname, site string) error {
	var sitePtr *string
	if site != "" {
		sitePtr = &site
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET
			hostname = CASE WHEN ? != '' THEN ? ELSE hostname END,
			site = CASE WHEN ? != '' THEN ? ELSE site END,
			updated_at = ?
		WHERE id = ? AND retired_at IS NULL`,
		hostname, hostname,
		site, sitePtr,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("update device %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConsumeEnrollmentToken marks the one-time token as used by storing the persistent
// device secret hash. Returns ErrNotFound if the token hash is not registered or
// has expired.
//
// The deadline is repeated here rather than trusted from the caller's earlier
// lookup. This statement is what actually mints the secret, and a token that
// expired between the two must not be spendable.
func (r *Repository) ConsumeEnrollmentToken(ctx context.Context, tokenHash, secretHash string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET device_secret_hash = ?, enrollment_token_hash = NULL,
		                   status = 'offline', updated_at = ?
		WHERE enrollment_token_hash = ? AND retired_at IS NULL
		  AND (enrollment_token_expires_at IS NULL OR enrollment_token_expires_at > ?)`,
		secretHash, time.Now().UTC(), tokenHash, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("consume enrollment token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// findByEnrollmentTokenHash loads the device waiting on a one-time enrollment token.
//
// The deadline is part of the lookup rather than a check afterwards, so an
// expired token and an unknown one are the same absence: the caller cannot tell
// them apart, and neither can somebody probing for valid tokens. NULL means the
// token has no deadline, which is every device enrolled before the column
// existed.
func (r *Repository) findByEnrollmentTokenHash(ctx context.Context, tokenHash string) (Device, error) {
	var d Device
	err := r.db.GetContext(ctx, &d, `
		SELECT * FROM devices
		WHERE enrollment_token_hash = ? AND retired_at IS NULL
		  AND (enrollment_token_expires_at IS NULL OR enrollment_token_expires_at > ?)`,
		tokenHash, time.Now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("find by enrollment token: %w", err)
	}
	return d, nil
}

// ReissueEnrollmentToken rotates credentials for an existing row and mints a new
// one-time token in one conditional write. The old secret cannot reconnect as
// soon as the write commits. A retired row cannot be resurrected by reissuing.
func (r *Repository) ReissueEnrollmentToken(ctx context.Context, id, tokenHash, placeholderSecretHash string, expiresAt time.Time) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET enrollment_token_hash = ?, enrollment_token_expires_at = ?,
		                   device_secret_hash = ?, status = 'offline', updated_at = ?
		WHERE id = ? AND retired_at IS NULL`,
		tokenHash, expiresAt, placeholderSecretHash, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("reissue enrollment token %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// FindBySecretHash looks up a device by its hashed persistent secret.
// Used to authenticate agent WebSocket connections.
func (r *Repository) FindBySecretHash(ctx context.Context, secretHash string) (Device, error) {
	var d Device
	err := r.db.GetContext(ctx, &d, `SELECT * FROM devices WHERE device_secret_hash = ? AND retired_at IS NULL`, secretHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("find by secret: %w", err)
	}
	return d, nil
}

// RotateDeviceSecret replaces the device's secret hash with a new one.
// The old secret immediately stops working for new connections. Returns
// the new plaintext secret so the admin can deliver it (e.g., via re-enroll).
func (r *Repository) RotateDeviceSecret(ctx context.Context, id string) (string, error) {
	// Generate new secret
	newPlain := GenerateToken()
	newHash := HashToken(newPlain)

	res, err := r.db.ExecContext(ctx, `
		UPDATE devices SET device_secret_hash = ?, status = 'offline', updated_at = ?
		WHERE id = ? AND retired_at IS NULL`,
		newHash, time.Now().UTC(), id)
	if err != nil {
		return "", fmt.Errorf("rotate device secret %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return "", ErrNotFound
	}
	return newPlain, nil
}

// UninstallToken represents an issued token for agent uninstallation.
type UninstallToken struct {
	ID        string     `db:"id"`
	DeviceID  string     `db:"device_id"`
	TokenHash string     `db:"token_hash"`
	CreatedBy string     `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
	ExpiresAt time.Time  `db:"expires_at"`
	UsedAt    *time.Time `db:"used_at"`
}

func (r *Repository) CreateUninstallToken(ctx context.Context, deviceID, tokenHash, createdBy string, expiresAt time.Time) error {
	now := time.Now().UTC()
	q := `INSERT INTO device_uninstall_tokens (id, device_id, token_hash, created_by, created_at, expires_at)
	      VALUES (?, ?, ?, ?, ?, ?)`
	_, err := r.db.ExecContext(ctx, r.db.Rebind(q), NewID(), deviceID, tokenHash, createdBy, now, expiresAt)
	if err != nil {
		return fmt.Errorf("create uninstall token for %s: %w", deviceID, err)
	}
	return nil
}

// UninstallTokenGracePeriod defines the window after initial token verification
// during which subsequent uninstaller verification requests (e.g. GUI check,
// section check, and service removal) from the same device remain authorized.
const UninstallTokenGracePeriod = 15 * time.Minute

func (r *Repository) VerifyAndConsumeUninstallToken(ctx context.Context, deviceID, plaintextToken string) (bool, error) {
	tokenHash := HashToken(plaintextToken)
	now := time.Now().UTC()
	graceCutoff := now.Add(-UninstallTokenGracePeriod)

	var token struct {
		ID     string     `db:"id"`
		UsedAt *time.Time `db:"used_at"`
	}
	q := `SELECT id, used_at FROM device_uninstall_tokens
	      WHERE device_id = ? AND token_hash = ? AND expires_at > ?
	        AND (used_at IS NULL OR used_at >= ?)
	      ORDER BY created_at DESC LIMIT 1`
	err := r.db.GetContext(ctx, &token, r.db.Rebind(q), deviceID, tokenHash, now, graceCutoff)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("verify uninstall token: %w", err)
	}

	// Mark consumed on initial verification; do not advance timestamp on subsequent checks within grace window
	if token.UsedAt == nil {
		updateQ := `UPDATE device_uninstall_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL`
		if _, err := r.db.ExecContext(ctx, r.db.Rebind(updateQ), now, token.ID); err != nil {
			return false, fmt.Errorf("mark uninstall token used: %w", err)
		}
	}
	return true, nil
}

func (r *Repository) VerifyUninstallToken(ctx context.Context, deviceID, plaintextToken string) (bool, error) {
	tokenHash := HashToken(plaintextToken)
	now := time.Now().UTC()
	graceCutoff := now.Add(-UninstallTokenGracePeriod)
	var count int
	q := `SELECT COUNT(*) FROM device_uninstall_tokens
	      WHERE device_id = ? AND token_hash = ? AND expires_at > ?
	        AND (used_at IS NULL OR used_at >= ?)`
	err := r.db.GetContext(ctx, &count, r.db.Rebind(q), deviceID, tokenHash, now, graceCutoff)
	if err != nil {
		return false, fmt.Errorf("verify uninstall token check: %w", err)
	}
	return count > 0, nil
}

// EnrollmentTokenRecord represents a shared/reusable enrollment token.
type EnrollmentTokenRecord struct {
	ID        string     `db:"id"`
	TokenHash string     `db:"token_hash"`
	Name      string     `db:"name"`
	Site      *string    `db:"site"`
	CreatedAt time.Time  `db:"created_at"`
	ExpiresAt time.Time  `db:"expires_at"`
	MaxUses   int        `db:"max_uses"` // 0 = unlimited
	UseCount  int        `db:"use_count"`
	CreatedBy string     `db:"created_by"`
	RevokedAt *time.Time `db:"revoked_at"`
}

func (r *Repository) CreateSharedEnrollmentToken(ctx context.Context, id, tokenHash, name string, site *string, expiresAt time.Time, maxUses int, createdBy string) error {
	now := time.Now().UTC()
	q := `INSERT INTO enrollment_tokens (id, token_hash, name, site, created_at, expires_at, max_uses, use_count, created_by)
	      VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)`
	_, err := r.db.ExecContext(ctx, r.db.Rebind(q), id, tokenHash, name, site, now, expiresAt, maxUses, createdBy)
	if err != nil {
		return fmt.Errorf("create shared enrollment token: %w", err)
	}
	return nil
}

func (r *Repository) FindValidSharedEnrollmentToken(ctx context.Context, tokenHash string) (*EnrollmentTokenRecord, error) {
	var t EnrollmentTokenRecord
	now := time.Now().UTC()
	q := `SELECT id, token_hash, name, site, created_at, expires_at, max_uses, use_count, created_by, revoked_at
	      FROM enrollment_tokens
	      WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?
	        AND (max_uses = 0 OR use_count < max_uses)`
	err := r.db.GetContext(ctx, &t, r.db.Rebind(q), tokenHash, now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find shared enrollment token: %w", err)
	}
	return &t, nil
}

func (r *Repository) ConsumeSharedEnrollmentTokenAndCreateDevice(ctx context.Context, tokenHash string, dev Device) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	// Atomically increment use_count if within bounds
	updateQ := `UPDATE enrollment_tokens
	            SET use_count = use_count + 1
	            WHERE token_hash = ? AND revoked_at IS NULL AND expires_at > ?
	              AND (max_uses = 0 OR use_count < max_uses)`
	res, err := tx.ExecContext(ctx, tx.Rebind(updateQ), tokenHash, now)
	if err != nil {
		return fmt.Errorf("update token use count: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}

	// Insert new device
	insertQ := `INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
	                                 last_seen_at, enrolled_at, enrollment_token_hash,
	                                 enrollment_token_expires_at,
	                                 device_secret_hash, site, created_at, updated_at)
	            VALUES (:id, :hostname, :os_name, :os_version, :agent_version, :status,
	                    :last_seen_at, :enrolled_at, :enrollment_token_hash,
	                    :enrollment_token_expires_at,
	                    :device_secret_hash, :site, :created_at, :updated_at)`
	if _, err := tx.NamedExecContext(ctx, insertQ, dev); err != nil {
		return fmt.Errorf("insert enrolled device: %w", err)
	}

	return tx.Commit()
}

