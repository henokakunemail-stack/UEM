package devicemanagement

import (
	"time"

	"github.com/jmoiron/sqlx"
)

// DeviceInventory is the last collected snapshot for a device. The JSON sections
// keep the schema stable when a new fact is added; the cached columns keep the
// dashboard filters and sorts fast without parsing JSON for every row.
type DeviceInventory struct {
	ID       string `db:"id"`
	DeviceID string `db:"device_id"`
	HW       string `db:"hw"`       // JSON: Hardware
	Software string `db:"software"` // JSON: []Software
	OSDetail string `db:"os_detail"`
	// Cached columns, populated from the JSON by the handler. NULL-able so a
	// collection that failed partway through does not write a fake zero.
	HWRAMBytes    *int64    `db:"hw_ram_bytes"`
	HWDiskFreePct *float64  `db:"hw_disk_free_pct"`
	HWCPUModel    *string   `db:"hw_cpu_model"`
	CollectedAt   time.Time `db:"collected_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

// StatusRetired marks a device that left the fleet. Its row is kept so audit
// trail references stay resolvable, but its device secret is cleared so the
// agent cannot reconnect with the old credential.
const StatusRetired = "retired"

// DeviceGroup is a static deployment target.
type DeviceGroup struct {
	ID          string    `db:"id" json:"id"`
	Name        string    `db:"name" json:"name"`
	Description string    `db:"description" json:"description"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
	UpdatedAt   time.Time `db:"updated_at" json:"updated_at"`
}

// GroupMemberCount is used by the group list view so the console can show how
// many devices each group holds without a second query per row.
type GroupMemberCount struct {
	DeviceGroup
	MemberCount int `db:"member_count" json:"member_count"`
}

// InventoryRepository is the exported handle to the inventory/group store.
// The internal type is unexported so its query surface cannot grow into an
// accidental public API; this wrapper exposes only construction.
type InventoryRepository struct {
	inventoryRepository
}

// NewInventoryRepository builds the Phase 2 repository over an existing pool.
func NewInventoryRepository(db *sqlx.DB) *InventoryRepository {
	return &InventoryRepository{inventoryRepository{db: db}}
}
