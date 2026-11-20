package assetlicense

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// unitRow is the shared shape of both lifecycle queries. The asset columns are
// NULL for a device that no asset points at; the device columns are NULL for
// an asset with no active linked device.
type unitRow struct {
	AssetID           sql.NullString  `db:"asset_id"`
	AssetTag          sql.NullString  `db:"asset_tag"`
	Model             sql.NullString  `db:"model_name"`
	AssetSite         sql.NullString  `db:"asset_site"`
	Category          sql.NullString  `db:"category"`
	PurchaseDate      sql.NullTime    `db:"purchase_date"`
	PurchaseCost      sql.NullFloat64 `db:"purchase_cost"`
	WarrantyExpiresAt sql.NullTime    `db:"warranty_expires_at"`
	DeviceID          sql.NullString  `db:"device_id"`
	Hostname          sql.NullString  `db:"hostname"`
	DeviceSite        sql.NullString  `db:"device_site"`
	HW                sql.NullString  `db:"hw"`
	OSDetail          sql.NullString  `db:"os_detail"`
	RAMBytes          sql.NullInt64   `db:"hw_ram_bytes"`
	DiskFreePct       sql.NullFloat64 `db:"hw_disk_free_pct"`
	Posture           sql.NullString  `db:"posture_json"`
}

// Assets that are still owned: retired and disposed ones are out of the fleet.
const lifecycleAssets = `
	SELECT a.id AS asset_id, a.asset_tag, a.model_name, a.site AS asset_site, a.category,
	       a.purchase_date, a.purchase_cost, a.warranty_expires_at,
	       d.id AS device_id, d.hostname, d.site AS device_site,
	       i.hw, i.os_detail, i.hw_ram_bytes, i.hw_disk_free_pct, p.posture_json
	FROM hardware_assets a
	LEFT JOIN devices d ON d.id = a.device_id AND d.retired_at IS NULL
	LEFT JOIN device_inventory i ON i.device_id = d.id
	LEFT JOIN device_posture p ON p.device_id = d.id
	WHERE a.status NOT IN ('retired', 'disposed')
	ORDER BY a.asset_tag ASC, a.id ASC`

// Active devices that no owned asset points at. They are part of the fleet
// even before anyone registers them as assets; their age can only come from
// the agent.
const lifecycleBareDevices = `
	SELECT d.id AS device_id, d.hostname, d.site AS device_site,
	       i.hw, i.os_detail, i.hw_ram_bytes, i.hw_disk_free_pct, p.posture_json
	FROM devices d
	LEFT JOIN device_inventory i ON i.device_id = d.id
	LEFT JOIN device_posture p ON p.device_id = d.id
	WHERE d.retired_at IS NULL
	  AND NOT EXISTS (SELECT 1 FROM hardware_assets a
	                  WHERE a.device_id = d.id AND a.status NOT IN ('retired', 'disposed'))
	ORDER BY d.hostname ASC, d.id ASC`

// LoadUnitFacts reads every owned asset and every active device without one.
func (r *Repository) LoadUnitFacts(ctx context.Context) ([]UnitFacts, error) {
	var assets, bare []unitRow
	if err := r.db.SelectContext(ctx, &assets, lifecycleAssets); err != nil {
		return nil, fmt.Errorf("read assets for lifecycle: %w", err)
	}
	if err := r.db.SelectContext(ctx, &bare, lifecycleBareDevices); err != nil {
		return nil, fmt.Errorf("read devices for lifecycle: %w", err)
	}
	out := make([]UnitFacts, 0, len(assets)+len(bare))
	for _, row := range append(assets, bare...) {
		out = append(out, row.facts())
	}
	return out, nil
}

func (row unitRow) facts() UnitFacts {
	f := UnitFacts{
		AssetID: row.AssetID.String, AssetTag: row.AssetTag.String, Model: row.Model.String,
		Site: row.AssetSite.String, Category: row.Category.String, PurchaseCost: row.PurchaseCost.Float64,
		DeviceID: row.DeviceID.String, Hostname: row.Hostname.String,
	}
	if f.Site == "" {
		f.Site = row.DeviceSite.String
	}
	if row.PurchaseDate.Valid {
		t := row.PurchaseDate.Time
		f.PurchaseDate = &t
	}
	if row.WarrantyExpiresAt.Valid {
		t := row.WarrantyExpiresAt.Time
		f.WarrantyExpiresAt = &t
	}
	if row.HW.Valid {
		f.HasInventory = true
		var hw struct {
			Model *struct {
				Product     string `json:"product"`
				ChassisType string `json:"chassis_type"`
				BIOSDate    string `json:"bios_date"`
			} `json:"model"`
		}
		if json.Unmarshal([]byte(row.HW.String), &hw) == nil && hw.Model != nil {
			f.ChassisType = hw.Model.ChassisType
			f.BIOSDate = parseBIOSDate(hw.Model.BIOSDate)
			if f.Model == "" {
				f.Model = hw.Model.Product
			}
		}
		if row.RAMBytes.Valid && row.RAMBytes.Int64 > 0 {
			v := row.RAMBytes.Int64
			f.RAMBytes = &v
		}
		if row.DiskFreePct.Valid {
			v := row.DiskFreePct.Float64
			f.DiskFreePct = &v
		}
	}
	// Windows reports the BIOS date in its posture; it wins over the model's.
	if row.Posture.Valid {
		var p struct {
			BIOSReleaseDate string `json:"bios_release_date"`
		}
		if json.Unmarshal([]byte(row.Posture.String), &p) == nil {
			if t := parseBIOSDate(p.BIOSReleaseDate); t != nil {
				f.BIOSDate = t
			}
		}
	}
	if row.OSDetail.Valid {
		var od struct {
			InstallTimestamp int64 `json:"install_timestamp"`
		}
		if json.Unmarshal([]byte(row.OSDetail.String), &od) == nil && od.InstallTimestamp > 0 {
			t := time.Unix(od.InstallTimestamp, 0).UTC()
			if plausibleDate(t) {
				f.OSInstall = &t
			}
		}
	}
	return f
}

// earliestPlausible rejects firmware placeholder dates (01/01/1970 and the
// like) as an age.
var earliestPlausible = time.Date(1995, 1, 1, 0, 0, 0, 0, time.UTC)

func plausibleDate(t time.Time) bool {
	return !t.Before(earliestPlausible) && !t.After(time.Now().AddDate(0, 0, 2))
}

// parseBIOSDate reads YYYY-MM-DD. Anything else, or an implausible date, is
// no date.
func parseBIOSDate(s string) *time.Time {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil || !plausibleDate(t) {
		return nil
	}
	return &t
}

// LoadLicenseUsage reads every license with its allocations to active
// devices, and each active device's installed program names (lower-cased),
// keyed by device id. A device with no inventory has no key: whether it has
// the software installed is unknown, not "no".
func (r *Repository) LoadLicenseUsage(ctx context.Context) ([]LicenseUsage, map[string][]string, error) {
	licenses, err := r.ListLicenses(ctx)
	if err != nil {
		return nil, nil, err
	}
	var allocs []struct {
		LicenseID string `db:"license_id"`
		DeviceID  string `db:"device_id"`
	}
	if err := r.db.SelectContext(ctx, &allocs, `
		SELECT a.license_id, a.device_id FROM license_allocations a
		JOIN devices d ON d.id = a.device_id
		WHERE d.retired_at IS NULL
		ORDER BY a.device_id`); err != nil {
		return nil, nil, fmt.Errorf("read license allocations: %w", err)
	}
	byLicense := map[string][]string{}
	for _, a := range allocs {
		byLicense[a.LicenseID] = append(byLicense[a.LicenseID], a.DeviceID)
	}
	usage := make([]LicenseUsage, 0, len(licenses))
	for _, l := range licenses {
		usage = append(usage, LicenseUsage{License: *l, Allocated: byLicense[l.ID]})
	}

	var rows []struct {
		DeviceID string `db:"device_id"`
		Software string `db:"software"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		SELECT i.device_id, i.software FROM device_inventory i
		JOIN devices d ON d.id = i.device_id
		WHERE d.retired_at IS NULL`); err != nil {
		return nil, nil, fmt.Errorf("read installed software: %w", err)
	}
	installed := make(map[string][]string, len(rows))
	for _, row := range rows {
		var items []struct {
			Name string `json:"name"`
		}
		names := []string{}
		if json.Unmarshal([]byte(row.Software), &items) == nil {
			for _, it := range items {
				if n := strings.ToLower(strings.TrimSpace(it.Name)); n != "" {
					names = append(names, n)
				}
			}
		}
		installed[row.DeviceID] = names
	}
	return usage, installed, nil
}

// lifecycleZone cuts quarters and years in WIB.
var lifecycleZone = time.FixedZone("WIB", 7*3600)

// Lifecycle computes the dashboard as of now.
func (r *Repository) Lifecycle(ctx context.Context, now time.Time) (LifecycleReport, error) {
	s, err := r.GetLifecycleSettings(ctx)
	if err != nil {
		return LifecycleReport{}, err
	}
	facts, err := r.LoadUnitFacts(ctx)
	if err != nil {
		return LifecycleReport{}, err
	}
	usage, installed, err := r.LoadLicenseUsage(ctx)
	if err != nil {
		return LifecycleReport{}, err
	}
	return ComputeLifecycle(s, facts, usage, installed, now.In(lifecycleZone)), nil
}
