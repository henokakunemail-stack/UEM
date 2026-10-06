package assetlicense

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type HardwareAsset struct {
	ID                string     `db:"id" json:"id"`
	AssetTag          string     `db:"asset_tag" json:"asset_tag"`
	DeviceID          *string    `db:"device_id" json:"device_id,omitempty"`
	ModelName         string     `db:"model_name" json:"model_name"`
	SerialNumber      string     `db:"serial_number" json:"serial_number"`
	Vendor            string     `db:"vendor" json:"vendor"`
	Site              string     `db:"site" json:"site"`
	Department        string     `db:"department" json:"department"`
	AssignedUser      string     `db:"assigned_user" json:"assigned_user"`
	PurchaseDate      *time.Time `db:"purchase_date" json:"purchase_date,omitempty"`
	PurchaseCost      float64    `db:"purchase_cost" json:"purchase_cost"`
	WarrantyExpiresAt *time.Time `db:"warranty_expires_at" json:"warranty_expires_at,omitempty"`
	Status            string     `db:"status" json:"status"`
	Notes             string     `db:"notes" json:"notes"`
	CreatedAt         time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at" json:"updated_at"`

	// DeviceHostname comes from the LEFT JOIN in ListAssets, not from the
	// hardware_assets table. It is the only way the console can show which
	// machine an asset belongs to without one extra request per row.
	DeviceHostname string `db:"device_hostname" json:"device_hostname"`
}

type SoftwareLicense struct {
	ID           string     `db:"id" json:"id"`
	SoftwareName string     `db:"software_name" json:"software_name"`
	Publisher    string     `db:"publisher" json:"publisher"`
	LicenseKey   string     `db:"license_key" json:"license_key"`
	LicenseType  string     `db:"license_type" json:"license_type"`
	TotalSeats   int        `db:"total_seats" json:"total_seats"`
	Cost         float64    `db:"cost" json:"cost"`
	PurchasedAt  *time.Time `db:"purchased_at" json:"purchased_at,omitempty"`
	ExpiresAt    *time.Time `db:"expires_at" json:"expires_at,omitempty"`
	Notes        string     `db:"notes" json:"notes"`
	CreatedAt    time.Time  `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at" json:"updated_at"`
}

type LicenseAllocation struct {
	ID          string    `db:"id" json:"id"`
	LicenseID   string    `db:"license_id" json:"license_id"`
	DeviceID    string    `db:"device_id" json:"device_id"`
	AllocatedAt time.Time `db:"allocated_at" json:"allocated_at"`
	AllocatedBy string    `db:"allocated_by" json:"allocated_by"`
}

type LicenseComplianceSummary struct {
	LicenseID         string     `json:"license_id"`
	SoftwareName      string     `json:"software_name"`
	Publisher         string     `json:"publisher"`
	LicenseType       string     `json:"license_type"`
	TotalSeats        int        `json:"total_seats"`
	AllocatedSeats    int        `json:"allocated_seats"`
	InstalledDetected int        `json:"installed_detected"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	Status            string     `json:"status"` // 'compliant', 'over_allocated', 'expiring_soon', 'expired'
}

type AssetSummary struct {
	TotalAssets           int     `json:"total_assets"`
	ActiveAssets          int     `json:"active_assets"`
	TotalValuation        float64 `json:"total_valuation"`
	WarrantyExpiringCount int     `json:"warranty_expiring_count"`
}

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Repository) CreateAsset(ctx context.Context, a *HardwareAsset) error {
	if a.ID == "" {
		a.ID = newID()
	}
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now
	if a.Status == "" {
		a.Status = "in_use"
	}

	query := `
		INSERT INTO hardware_assets (
			id, asset_tag, device_id, model_name, serial_number, vendor,
			site, department, assigned_user, purchase_date, purchase_cost,
			warranty_expires_at, status, notes, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		a.ID, a.AssetTag, a.DeviceID, a.ModelName, a.SerialNumber, a.Vendor,
		a.Site, a.Department, a.AssignedUser, a.PurchaseDate, a.PurchaseCost,
		a.WarrantyExpiresAt, a.Status, a.Notes, a.CreatedAt, a.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create asset: %w", err)
	}
	return nil
}

// AssetTagTaken reports whether another asset already holds this tag. Excludes
// excludeID when non-empty, so an edit that keeps its own tag is not refused
// as a conflict with itself.
func (r *Repository) AssetTagTaken(ctx context.Context, tag, excludeID string) (bool, error) {
	query := `SELECT COUNT(*) FROM hardware_assets WHERE asset_tag = ?`
	args := []any{tag}
	if excludeID != "" {
		query += ` AND id != ?`
		args = append(args, excludeID)
	}
	var n int
	if err := r.db.GetContext(ctx, &n, query, args...); err != nil {
		return false, fmt.Errorf("check asset tag %s: %w", tag, err)
	}
	return n > 0, nil
}

func (r *Repository) GetAsset(ctx context.Context, id string) (*HardwareAsset, error) {
	// Same LEFT JOIN as ListAssets, so the edit form shows the linked machine's
	// hostname without the console needing a second request per open.
	var a HardwareAsset
	err := r.db.GetContext(ctx, &a, `
		SELECT a.id, a.asset_tag, a.device_id, a.model_name, a.serial_number,
		       a.vendor, a.site, a.department, a.assigned_user, a.purchase_date,
		       a.purchase_cost, a.warranty_expires_at, a.status, a.notes,
		       a.created_at, a.updated_at,
		       COALESCE(d.hostname, '') AS device_hostname
		FROM hardware_assets a
		LEFT JOIN devices d ON d.id = a.device_id
		WHERE a.id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get asset %s: %w", id, err)
	}
	return &a, nil
}

// DeviceExists reports whether an id names a real device. It exists so a bad
// device_id is refused with a message an operator can act on, instead of
// surfacing as a raw SQLite foreign-key string on a 500.
func (r *Repository) DeviceExists(ctx context.Context, deviceID string) (bool, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM devices WHERE id = ?`, deviceID)
	if err != nil {
		return false, fmt.Errorf("check device %s: %w", deviceID, err)
	}
	return n > 0, nil
}

func (r *Repository) GetAssetByTag(ctx context.Context, tag string) (*HardwareAsset, error) {
	var a HardwareAsset
	err := r.db.GetContext(ctx, &a, `SELECT * FROM hardware_assets WHERE asset_tag = ?`, tag)
	if err != nil {
		return nil, fmt.Errorf("get asset by tag %s: %w", tag, err)
	}
	return &a, nil
}

func (r *Repository) ListAssets(ctx context.Context, site, status string) ([]*HardwareAsset, error) {
	// LEFT JOIN, not INNER: device_id is nullable and ON DELETE SET NULL, so a
	// retired device leaves the asset behind with no device row to match. An
	// inner join would silently drop exactly the assets an operator most wants
	// to find — the ones whose machine is gone.
	//
	// Columns are listed explicitly rather than SELECT * because the joined
	// hostname has no hardware_assets column to keep the two in step, and a
	// duplicate name across the two tables would be a silent scan error.
	query := `
		SELECT a.id, a.asset_tag, a.device_id, a.model_name, a.serial_number,
		       a.vendor, a.site, a.department, a.assigned_user, a.purchase_date,
		       a.purchase_cost, a.warranty_expires_at, a.status, a.notes,
		       a.created_at, a.updated_at,
		       COALESCE(d.hostname, '') AS device_hostname
		FROM hardware_assets a
		LEFT JOIN devices d ON d.id = a.device_id
		WHERE 1=1
	`
	var args []any

	if site != "" {
		query += ` AND a.site = ?`
		args = append(args, site)
	}
	if status != "" {
		query += ` AND a.status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY a.created_at DESC`

	list := []*HardwareAsset{}
	err := r.db.SelectContext(ctx, &list, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	return list, nil
}

func (r *Repository) UpdateAsset(ctx context.Context, a *HardwareAsset) error {
	now := time.Now().UTC()
	a.UpdatedAt = now

	query := `
		UPDATE hardware_assets SET
			asset_tag = ?, device_id = ?, model_name = ?, serial_number = ?,
			vendor = ?, site = ?, department = ?, assigned_user = ?,
			purchase_date = ?, purchase_cost = ?, warranty_expires_at = ?,
			status = ?, notes = ?, updated_at = ?
		WHERE id = ?
	`
	_, err := r.db.ExecContext(ctx, query,
		a.AssetTag, a.DeviceID, a.ModelName, a.SerialNumber, a.Vendor,
		a.Site, a.Department, a.AssignedUser, a.PurchaseDate, a.PurchaseCost,
		a.WarrantyExpiresAt, a.Status, a.Notes, a.UpdatedAt, a.ID,
	)
	return err
}

func (r *Repository) DeleteAsset(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM hardware_assets WHERE id = ?`, id)
	return err
}

func (r *Repository) CreateLicense(ctx context.Context, l *SoftwareLicense) error {
	if l.ID == "" {
		l.ID = newID()
	}
	now := time.Now().UTC()
	l.CreatedAt = now
	l.UpdatedAt = now
	if l.TotalSeats <= 0 {
		l.TotalSeats = 1
	}

	query := `
		INSERT INTO software_licenses (
			id, software_name, publisher, license_key, license_type,
			total_seats, cost, purchased_at, expires_at, notes, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		l.ID, l.SoftwareName, l.Publisher, l.LicenseKey, l.LicenseType,
		l.TotalSeats, l.Cost, l.PurchasedAt, l.ExpiresAt, l.Notes, l.CreatedAt, l.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create license: %w", err)
	}
	return nil
}

func (r *Repository) GetLicense(ctx context.Context, id string) (*SoftwareLicense, error) {
	var l SoftwareLicense
	err := r.db.GetContext(ctx, &l, `SELECT * FROM software_licenses WHERE id = ?`, id)
	if err != nil {
		return nil, fmt.Errorf("get license %s: %w", id, err)
	}
	return &l, nil
}

func (r *Repository) ListLicenses(ctx context.Context) ([]*SoftwareLicense, error) {
	list := []*SoftwareLicense{}
	err := r.db.SelectContext(ctx, &list, `SELECT * FROM software_licenses ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list licenses: %w", err)
	}
	return list, nil
}

func (r *Repository) AllocateLicense(ctx context.Context, licenseID, deviceID, actorID string) error {
	allocID := newID()
	now := time.Now().UTC()
	query := `
		INSERT INTO license_allocations (id, license_id, device_id, allocated_at, allocated_by)
		VALUES (?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query, allocID, licenseID, deviceID, now, actorID)
	return err
}

func (r *Repository) DeallocateLicense(ctx context.Context, licenseID, deviceID string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM license_allocations WHERE license_id = ? AND device_id = ?`, licenseID, deviceID)
	return err
}

func (r *Repository) GetAllocationsForLicense(ctx context.Context, licenseID string) ([]string, error) {
	var deviceIDs []string
	err := r.db.SelectContext(ctx, &deviceIDs, `SELECT device_id FROM license_allocations WHERE license_id = ?`, licenseID)
	return deviceIDs, err
}

func (r *Repository) ComputeCompliance(ctx context.Context) ([]*LicenseComplianceSummary, error) {
	licenses, err := r.ListLicenses(ctx)
	if err != nil {
		return nil, err
	}

	summaries := make([]*LicenseComplianceSummary, 0, len(licenses))
	now := time.Now().UTC()

	for _, lic := range licenses {
		var allocatedCount int
		_ = r.db.GetContext(ctx, &allocatedCount, `SELECT COUNT(*) FROM license_allocations WHERE license_id = ?`, lic.ID)

		// Check detected installations from device inventory
		var detectedCount int
		pattern := "%" + lic.SoftwareName + "%"
		_ = r.db.GetContext(ctx, &detectedCount, `
			SELECT COUNT(DISTINCT device_id)
			FROM device_inventory
			WHERE LOWER(software) LIKE LOWER(?)
		`, pattern)

		effectiveUsed := allocatedCount
		if detectedCount > effectiveUsed {
			effectiveUsed = detectedCount
		}

		status := "compliant"
		if effectiveUsed > lic.TotalSeats {
			status = "over_allocated"
		} else if lic.ExpiresAt != nil {
			if now.After(*lic.ExpiresAt) {
				status = "expired"
			} else if now.AddDate(0, 0, 30).After(*lic.ExpiresAt) {
				status = "expiring_soon"
			}
		}

		summaries = append(summaries, &LicenseComplianceSummary{
			LicenseID:         lic.ID,
			SoftwareName:      lic.SoftwareName,
			Publisher:         lic.Publisher,
			LicenseType:       lic.LicenseType,
			TotalSeats:        lic.TotalSeats,
			AllocatedSeats:    allocatedCount,
			InstalledDetected: detectedCount,
			ExpiresAt:         lic.ExpiresAt,
			Status:            status,
		})
	}

	return summaries, nil
}

func (r *Repository) GetAssetSummary(ctx context.Context) (*AssetSummary, error) {
	var total, active int
	var valuation float64

	_ = r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM hardware_assets`)
	_ = r.db.GetContext(ctx, &active, `SELECT COUNT(*) FROM hardware_assets WHERE status = 'in_use'`)
	_ = r.db.GetContext(ctx, &valuation, `SELECT COALESCE(SUM(purchase_cost), 0.0) FROM hardware_assets WHERE status != 'disposed'`)

	var expiringCount int
	in30Days := time.Now().UTC().AddDate(0, 0, 30)
	now := time.Now().UTC()
	_ = r.db.GetContext(ctx, &expiringCount, `
		SELECT COUNT(*) FROM hardware_assets
		WHERE warranty_expires_at IS NOT NULL
		  AND warranty_expires_at >= ?
		  AND warranty_expires_at <= ?
	`, now, in30Days)

	return &AssetSummary{
		TotalAssets:           total,
		ActiveAssets:          active,
		TotalValuation:        valuation,
		WarrantyExpiringCount: expiringCount,
	}, nil
}
