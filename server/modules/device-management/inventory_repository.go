package devicemanagement

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// inventoryRepository handles inventory, group and lifecycle persistence.
// It is a separate type from Repository so the Phase 1 enrollment/status code
// stays unaffected as inventory queries grow.
type inventoryRepository struct {
	db *sqlx.DB
}

func newInventoryRepository(db *sqlx.DB) *inventoryRepository {
	return &inventoryRepository{db: db}
}

// getDevice returns the device row, so handlers can reject requests for unknown
// or retired devices before touching inventory.
func (r *inventoryRepository) getDevice(ctx context.Context, id string) (Device, error) {
	var d Device
	err := r.db.GetContext(ctx, &d,
		`SELECT * FROM devices WHERE id = ? AND retired_at IS NULL`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("get device %s: %w", id, err)
	}
	return d, nil
}

// upsertInventory stores a collected snapshot, replacing the previous one.
// A device only ever has the latest inventory on record; history is an audit
// concern, handled by inventory diffing in the handler.
func (r *inventoryRepository) upsertInventory(ctx context.Context, inv DeviceInventory) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO device_inventory
			(id, device_id, hw, software, os_detail,
			 hw_ram_bytes, hw_disk_free_pct, hw_cpu_model, collected_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			hw = excluded.hw,
			software = excluded.software,
			os_detail = excluded.os_detail,
			hw_ram_bytes = excluded.hw_ram_bytes,
			hw_disk_free_pct = excluded.hw_disk_free_pct,
			hw_cpu_model = excluded.hw_cpu_model,
			collected_at = excluded.collected_at,
			updated_at = excluded.updated_at`,
		inv.ID, inv.DeviceID, inv.HW, inv.Software, inv.OSDetail,
		inv.HWRAMBytes, inv.HWDiskFreePct, inv.HWCPUModel,
		inv.CollectedAt, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("upsert inventory: %w", err)
	}
	return nil
}

// getInventory loads the latest snapshot for a device.
func (r *inventoryRepository) getInventory(ctx context.Context, deviceID string) (DeviceInventory, error) {
	var inv DeviceInventory
	err := r.db.GetContext(ctx, &inv,
		`SELECT * FROM device_inventory WHERE device_id = ?`, deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceInventory{}, ErrNotFound
	}
	if err != nil {
		return DeviceInventory{}, fmt.Errorf("get inventory %s: %w", deviceID, err)
	}
	return inv, nil
}

// retire removes a device from the fleet without deleting its row: the secret
// and any unspent enrollment token are cleared, while audit references stay
// resolvable. Returns ErrNotFound if the device does not exist.
func (r *inventoryRepository) retire(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices
		SET status = ?, retired_at = ?, device_secret_hash = '',
		    enrollment_token_hash = NULL, enrollment_token_expires_at = NULL,
		    updated_at = ?
		WHERE id = ? AND retired_at IS NULL`,
		StatusRetired, time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("retire device %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// restore returns a retired device to the fleet. The device secret stays empty,
// so the agent must enroll again — this is intentional: a restore is a
// re-onboarding, not a resurrection of the old credential.
func (r *inventoryRepository) restore(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE devices
		SET status = ?, retired_at = NULL, updated_at = ?
		WHERE id = ? AND retired_at IS NOT NULL`,
		StatusOffline, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("restore device %s: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// setCapabilities records which command types the agent supports, so the server
// never sends a command an older agent would silently ignore.
func (r *inventoryRepository) setCapabilities(ctx context.Context, deviceID, capabilitiesJSON string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE devices SET capabilities = ?, updated_at = ? WHERE id = ?`,
		capabilitiesJSON, time.Now().UTC(), deviceID)
	if err != nil {
		return fmt.Errorf("set capabilities %s: %w", deviceID, err)
	}
	return nil
}

// --- groups ---

func (r *inventoryRepository) createGroup(ctx context.Context, g DeviceGroup) error {
	_, err := r.db.NamedExecContext(ctx, `
		INSERT INTO device_groups (id, name, description, created_at, updated_at)
		VALUES (:id, :name, :description, :created_at, :updated_at)`, g)
	if err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	return nil
}

func (r *inventoryRepository) getGroup(ctx context.Context, id string) (DeviceGroup, error) {
	var g DeviceGroup
	err := r.db.GetContext(ctx, &g, `SELECT * FROM device_groups WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceGroup{}, ErrNotFound
	}
	if err != nil {
		return DeviceGroup{}, fmt.Errorf("get group %s: %w", id, err)
	}
	return g, nil
}

// getGroupByName exists so a duplicate name is reported as a conflict rather
// than surfacing as a UNIQUE constraint violation from the INSERT.
func (r *inventoryRepository) getGroupByName(ctx context.Context, name string) (DeviceGroup, error) {
	var g DeviceGroup
	err := r.db.GetContext(ctx, &g, `SELECT * FROM device_groups WHERE name = ?`, name)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceGroup{}, ErrNotFound
	}
	if err != nil {
		return DeviceGroup{}, fmt.Errorf("get group by name %q: %w", name, err)
	}
	return g, nil
}

// listGroups returns all groups with their member counts in one query.
func (r *inventoryRepository) listGroups(ctx context.Context) ([]GroupMemberCount, error) {
	groups := []GroupMemberCount{}
	err := r.db.SelectContext(ctx, &groups, `
		SELECT g.*, COUNT(m.device_id) AS member_count
		FROM device_groups g
		LEFT JOIN device_group_members m ON m.group_id = g.id
		GROUP BY g.id, g.name, g.description, g.created_at, g.updated_at
		ORDER BY g.name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// groupUsers are the tables that can name a group as their target. Deleting a
// group that any of these still points at would leave the record describing a
// run against a target that no longer resolves, so the run would reach nobody
// and still look successful in the history.
var groupUsers = []struct{ table, label string }{
	{"software_deployments", "a software deployment"},
	{"task_schedules", "a scheduled task"},
	{"filter_policies", "a filter policy"},
	{"update_campaigns", "an update campaign"},
	{"maintenance_jobs", "a maintenance job"},
}

// groupInUse returns the first feature still targeting this group, or an empty
// string when nothing references it. The five tables are checked in a fixed
// order so the message names the same feature every time for the same group.
func (r *inventoryRepository) groupInUse(ctx context.Context, id string) (string, error) {
	for _, u := range groupUsers {
		var n int
		if err := r.db.GetContext(ctx, &n,
			`SELECT COUNT(*) FROM `+u.table+
				` WHERE target_type = 'group' AND target_id = ?`, id); err != nil {
			return "", fmt.Errorf("check %s for group %s: %w", u.table, id, err)
		}
		if n > 0 {
			return u.label, nil
		}
	}
	return "", nil
}

// deleteGroup removes the group and its memberships. Devices themselves are
// untouched — membership is a property of the group, not the device. A group a
// deployment or schedule still points at is refused: removing it would turn
// that run into a no-op that reports success.
func (r *inventoryRepository) deleteGroup(ctx context.Context, id string) error {
	// Before the transaction, so the check is not held under the write lock and
	// a refusal costs nothing.
	label, err := r.groupInUse(ctx, id)
	if err != nil {
		return err
	}
	if label != "" {
		return fmt.Errorf("%w by %s", ErrGroupInUse, label)
	}

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	defer tx.Rollback() // safe: a committed tx has nothing to roll back

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM device_group_members WHERE group_id = ?`, id); err != nil {
		return fmt.Errorf("delete memberships: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM device_groups WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit group delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// addMembers adds devices to a group. Existing memberships are ignored by the
// PRIMARY KEY constraint, so re-adding is idempotent.
func (r *inventoryRepository) addMembers(ctx context.Context, groupID, addedBy string, deviceIDs []string) (int, error) {
	now := time.Now().UTC()
	var inserted int
	for _, deviceID := range deviceIDs {
		res, err := r.db.ExecContext(ctx, `
			INSERT OR IGNORE INTO device_group_members (group_id, device_id, added_at, added_by)
			VALUES (?, ?, ?, ?)`, groupID, deviceID, now, addedBy)
		if err != nil {
			return inserted, fmt.Errorf("add member %s: %w", deviceID, err)
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}
	return inserted, nil
}

// removeMember removes one device from a group.
func (r *inventoryRepository) removeMember(ctx context.Context, groupID, deviceID string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM device_group_members WHERE group_id = ? AND device_id = ?`,
		groupID, deviceID)
	if err != nil {
		return fmt.Errorf("remove member: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// listGroupDevices returns the devices in a group with pagination.
func (r *inventoryRepository) listGroupDevices(ctx context.Context, groupID string, limit, offset int) ([]Device, error) {
	devices := []Device{}
	err := r.db.SelectContext(ctx, &devices, `
		SELECT d.* FROM devices d
		JOIN device_group_members m ON m.device_id = d.id
		WHERE m.group_id = ?
		ORDER BY d.hostname ASC
		LIMIT ? OFFSET ?`, groupID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list group devices: %w", err)
	}
	return devices, nil
}

// countGroupDevices returns the total member count of a group.
func (r *inventoryRepository) countGroupDevices(ctx context.Context, groupID string) (int, error) {
	var count int
	err := r.db.GetContext(ctx, &count,
		`SELECT COUNT(*) FROM device_group_members WHERE group_id = ?`, groupID)
	if err != nil {
		return 0, fmt.Errorf("count group devices: %w", err)
	}
	return count, nil
}
