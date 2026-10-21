package assetlicense

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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

// UpdateAssetRequest mirrors the writable columns of HardwareAsset as pointers.
//
// encoding/json cannot tell "field omitted" from "field sent as the zero value",
// so decoding a PUT body straight into the model means a partial body such as
// {"status":"retired"} also writes asset_tag="", model_name="", purchase_cost=0
// and every other field the client never sent. An API client other than the
// console — which today always sends a full body — would silently flatten an
// asset. A nil here means "leave the stored value alone".
type UpdateAssetRequest struct {
	AssetTag          *string    `json:"asset_tag"`
	DeviceID          *string    `json:"device_id"`
	ModelName         *string    `json:"model_name"`
	SerialNumber      *string    `json:"serial_number"`
	Vendor            *string    `json:"vendor"`
	Site              *string    `json:"site"`
	Department        *string    `json:"department"`
	AssignedUser      *string    `json:"assigned_user"`
	PurchaseDate      *time.Time `json:"purchase_date"`
	PurchaseCost      *float64   `json:"purchase_cost"`
	WarrantyExpiresAt *time.Time `json:"warranty_expires_at"`
	Status            *string    `json:"status"`
	Notes             *string    `json:"notes"`
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
	if len(licenses) == 0 {
		return []*LicenseComplianceSummary{}, nil
	}

	// Batch fetch allocation counts in a single group-by query to avoid N+1 query loop
	type allocRow struct {
		LicenseID string `db:"license_id"`
		Count     int    `db:"count"`
	}
	var allocRows []allocRow
	// Allocations to a retired device hold no seat.
	if err := r.db.SelectContext(ctx, &allocRows, `
		SELECT a.license_id, COUNT(*) as count
		FROM license_allocations a
		JOIN devices d ON d.id = a.device_id
		WHERE d.retired_at IS NULL
		GROUP BY a.license_id
	`); err != nil {
		return nil, fmt.Errorf("count license allocations: %w", err)
	}
	allocMap := make(map[string]int, len(allocRows))
	for _, row := range allocRows {
		allocMap[row.LicenseID] = row.Count
	}

	installed, err := r.installedSoftwareNames(ctx)
	if err != nil {
		return nil, err
	}

	summaries := make([]*LicenseComplianceSummary, 0, len(licenses))
	now := time.Now().UTC()

	for _, lic := range licenses {
		allocatedCount := allocMap[lic.ID]
		detectedCount := countInstalls(installed, lic.SoftwareName)

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

	// A failed count is an error, not a zero the page shows as fact.
	if err := r.db.GetContext(ctx, &total, `SELECT COUNT(*) FROM hardware_assets`); err != nil {
		return nil, fmt.Errorf("count assets: %w", err)
	}
	if err := r.db.GetContext(ctx, &active, `SELECT COUNT(*) FROM hardware_assets WHERE status = 'in_use'`); err != nil {
		return nil, fmt.Errorf("count active assets: %w", err)
	}
	if err := r.db.GetContext(ctx, &valuation, `SELECT COALESCE(SUM(purchase_cost), 0.0) FROM hardware_assets WHERE status != 'disposed'`); err != nil {
		return nil, fmt.Errorf("sum asset valuation: %w", err)
	}

	var expiringCount int
	in30Days := time.Now().UTC().AddDate(0, 0, 30)
	now := time.Now().UTC()
	if err := r.db.GetContext(ctx, &expiringCount, `
		SELECT COUNT(*) FROM hardware_assets
		WHERE warranty_expires_at IS NOT NULL
		  AND warranty_expires_at >= ?
		  AND warranty_expires_at <= ?
	`, now, in30Days); err != nil {
		return nil, fmt.Errorf("count expiring warranties: %w", err)
	}

	return &AssetSummary{
		TotalAssets:           total,
		ActiveAssets:          active,
		TotalValuation:        valuation,
		WarrantyExpiringCount: expiringCount,
	}, nil
}

// installedSoftwareNames reads each active device's installed program names,
// lower-cased, once for all licenses.
//
// The count used to be a LIKE over the whole software JSON per license: it
// matched publisher, version and key text as well as names ("Microsoft"
// matched every Microsoft product), treated % and _ in a license name as
// wildcards, counted retired devices, ran one query per license, and turned a
// failed query into 0.
func (r *Repository) installedSoftwareNames(ctx context.Context) ([][]string, error) {
	var rows []string
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT i.software FROM device_inventory i
		JOIN devices d ON d.id = i.device_id
		WHERE d.retired_at IS NULL`); err != nil {
		return nil, fmt.Errorf("read installed software: %w", err)
	}
	out := make([][]string, 0, len(rows))
	for _, raw := range rows {
		var items []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			continue // a row that is not a software list ("null", legacy) has nothing installed
		}
		names := make([]string, 0, len(items))
		for _, it := range items {
			if n := strings.ToLower(strings.TrimSpace(it.Name)); n != "" {
				names = append(names, n)
			}
		}
		out = append(out, names)
	}
	return out, nil
}

// countInstalls counts devices with a program whose name contains the
// license's software name, case-insensitively. "Microsoft Office" matches
// "Microsoft Office Professional Plus 2021"; the publisher does not count.
func countInstalls(devices [][]string, softwareName string) int {
	needle := strings.ToLower(strings.TrimSpace(softwareName))
	if needle == "" {
		return 0
	}
	n := 0
	for _, names := range devices {
		for _, name := range names {
			if strings.Contains(name, needle) {
				n++
				break
			}
		}
	}
	return n
}
