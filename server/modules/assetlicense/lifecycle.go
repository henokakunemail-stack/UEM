package assetlicense

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Asset lifecycle: how old each machine is, when it is due for replacement,
// what that will cost, and which license seats are paid for but not used.
//
// Every age carries its source. A purchase date from the asset register is a
// fact; a BIOS release date or an OS install date reported by the agent is
// only an estimate (a BIOS update moves the date forward, a reinstall moves
// the OS date forward), and the dashboard and the PDF say so. A machine with
// no age at all is never guessed into the budget forecast: it is counted as
// "data kurang" instead.

// Category values. An empty asset category means "follow the linked device".
const (
	CategoryLaptop  = "laptop"
	CategoryDesktop = "desktop"
	CategoryServer  = "server"
	CategoryOther   = "other"
)

// Categories is the fixed list, in display order.
var Categories = []string{CategoryLaptop, CategoryDesktop, CategoryServer, CategoryOther}

// Age sources.
const (
	AgeFromPurchase  = "purchase"
	AgeFromBIOS      = "bios"
	AgeFromOSInstall = "os_install"
	AgeUnknown       = "unknown"
)

func isCategory(c string) bool {
	for _, k := range Categories {
		if c == k {
			return true
		}
	}
	return false
}

// isComputer reports whether an asset of this category may borrow its linked
// device's facts. A monitor or a dock (category "other") linked to a PC must
// not take the PC's BIOS date as its own age.
func isComputer(category string) bool { return category != CategoryOther }

func validCategory(w http.ResponseWriter, c string) bool {
	if c == "" || isCategory(c) {
		return true
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "category must be one of laptop, desktop, server, other, or empty"})
	return false
}

// CategoryLife is the admin's lifecycle setting for one category.
type CategoryLife struct {
	UsefulLifeYears int `json:"useful_life_years"`
	// ReplacementCost is what one replacement unit costs, in rupiah. 0 means
	// not set: the forecast then falls back to the asset's purchase cost.
	ReplacementCost float64 `json:"replacement_cost"`
}

// LifecycleSettings is what an admin sets for the lifecycle dashboard.
type LifecycleSettings struct {
	Categories map[string]CategoryLife `json:"categories"`
	// MinRAMGB flags machines with less memory. 0 turns the check off.
	MinRAMGB float64 `json:"min_ram_gb"`
	// MinDiskFreePercent flags machines whose disks are fuller. 0 turns it off.
	MinDiskFreePercent float64 `json:"min_disk_free_percent"`
}

// DefaultLifecycleSettings is used until an admin saves their own.
func DefaultLifecycleSettings() LifecycleSettings {
	return LifecycleSettings{
		Categories: map[string]CategoryLife{
			CategoryLaptop:  {UsefulLifeYears: 4},
			CategoryDesktop: {UsefulLifeYears: 5},
			CategoryServer:  {UsefulLifeYears: 5},
			CategoryOther:   {UsefulLifeYears: 5},
		},
		MinRAMGB:           8,
		MinDiskFreePercent: 10,
	}
}

// Validate refuses settings the computation cannot use.
func (s LifecycleSettings) Validate() error {
	if len(s.Categories) != len(Categories) {
		return fmt.Errorf("categories must be exactly %s", strings.Join(Categories, ", "))
	}
	for _, c := range Categories {
		v, ok := s.Categories[c]
		if !ok {
			return fmt.Errorf("categories must be exactly %s", strings.Join(Categories, ", "))
		}
		if v.UsefulLifeYears < 1 || v.UsefulLifeYears > 15 {
			return fmt.Errorf("%s: useful life must be 1 to 15 years", c)
		}
		if v.ReplacementCost < 0 || math.IsNaN(v.ReplacementCost) || math.IsInf(v.ReplacementCost, 0) {
			return fmt.Errorf("%s: replacement cost must be 0 or more", c)
		}
	}
	if s.MinRAMGB < 0 || s.MinRAMGB > 1024 || math.IsNaN(s.MinRAMGB) {
		return errors.New("min_ram_gb must be 0 to 1024")
	}
	if s.MinDiskFreePercent < 0 || s.MinDiskFreePercent > 100 || math.IsNaN(s.MinDiskFreePercent) {
		return errors.New("min_disk_free_percent must be 0 to 100")
	}
	return nil
}

const lifecycleSettingsID = "default"

// GetLifecycleSettings returns the saved settings, or the defaults when none
// were saved. A stored row that no longer decodes or validates is an error,
// not a silent fall back to defaults the admin never chose.
func (r *Repository) GetLifecycleSettings(ctx context.Context) (LifecycleSettings, error) {
	var raw string
	err := r.db.GetContext(ctx, &raw, `SELECT settings_json FROM asset_lifecycle_settings WHERE id = ?`, lifecycleSettingsID)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultLifecycleSettings(), nil
	}
	if err != nil {
		return LifecycleSettings{}, fmt.Errorf("read lifecycle settings: %w", err)
	}
	var s LifecycleSettings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return LifecycleSettings{}, fmt.Errorf("decode lifecycle settings: %w", err)
	}
	if err := s.Validate(); err != nil {
		return LifecycleSettings{}, fmt.Errorf("stored lifecycle settings: %w", err)
	}
	return s, nil
}

// SaveLifecycleSettings validates and stores the settings.
func (r *Repository) SaveLifecycleSettings(ctx context.Context, s LifecycleSettings, actor string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `UPDATE asset_lifecycle_settings SET settings_json = ?, updated_by = ?, updated_at = ? WHERE id = ?`,
		string(raw), actor, now, lifecycleSettingsID)
	if err != nil {
		return fmt.Errorf("save lifecycle settings: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx, `INSERT INTO asset_lifecycle_settings (id, settings_json, updated_by, updated_at) VALUES (?, ?, ?, ?)`,
		lifecycleSettingsID, string(raw), actor, now); err != nil {
		return fmt.Errorf("save lifecycle settings: %w", err)
	}
	return nil
}

// UnitFacts is one machine (or other asset) as read from the database: an
// asset from the register, or an active device that no asset points at.
type UnitFacts struct {
	AssetID           string
	AssetTag          string
	Model             string
	Site              string
	Category          string // the asset's own category; empty for a device without an asset
	PurchaseDate      *time.Time
	PurchaseCost      float64
	WarrantyExpiresAt *time.Time

	DeviceID     string
	Hostname     string
	ChassisType  string // laptop | desktop | server, from the agent; empty when unknown
	BIOSDate     *time.Time
	OSInstall    *time.Time
	HasInventory bool
	RAMBytes     *int64
	DiskFreePct  *float64
}

// Unit is one row of the dashboard.
type Unit struct {
	AssetID  string `json:"asset_id,omitempty"`
	AssetTag string `json:"asset_tag,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Model    string `json:"model,omitempty"`
	Site     string `json:"site,omitempty"`
	// Category and where it came from: "asset" (set on the asset), "device"
	// (the agent's chassis type) or "default" (neither, so "other").
	Category       string `json:"category"`
	CategorySource string `json:"category_source"`

	AgeSource       string     `json:"age_source"`
	StartDate       *time.Time `json:"start_date,omitempty"`
	AgeYears        *float64   `json:"age_years,omitempty"`
	UsefulLifeYears int        `json:"useful_life_years"`
	DueDate         *time.Time `json:"due_date,omitempty"`
	Overdue         bool       `json:"overdue"`

	// ReplacementCost is nil when neither the category nor the asset has a
	// cost; CostSource is "category" or "purchase".
	ReplacementCost *float64 `json:"replacement_cost,omitempty"`
	CostSource      string   `json:"cost_source,omitempty"`
	PurchaseCost    float64  `json:"purchase_cost"`
	BookValue       *float64 `json:"book_value,omitempty"`

	RAMBytes    *int64   `json:"ram_bytes,omitempty"`
	DiskFreePct *float64 `json:"disk_free_pct,omitempty"`
	// BelowSpec lists "ram" and/or "disk".
	BelowSpec []string `json:"below_spec"`

	WarrantyExpiresAt *time.Time `json:"warranty_expires_at,omitempty"`
}

// AgeBucket is one bar of the age distribution.
type AgeBucket struct {
	Key   string `json:"key"` // 0-1 | 1-3 | 3-5 | 5+ | unknown
	Count int    `json:"count"`
}

// BudgetLine is the replacements falling due in one period.
type BudgetLine struct {
	Label string    `json:"label"` // "2027-Q1" or "2027"
	Start time.Time `json:"start"`
	End   time.Time `json:"end"` // exclusive
	Units int       `json:"units"`
	Cost  float64   `json:"cost"`
	// UnitsWithoutCost are due in this period but have no replacement cost,
	// so Cost understates the period by that many units.
	UnitsWithoutCost int `json:"units_without_cost"`
}

// Forecast is the replacement budget for the next three years.
type Forecast struct {
	// Overdue is every unit already past its useful life: due now.
	Overdue  BudgetLine   `json:"overdue"`
	Quarters []BudgetLine `json:"quarters"`
	Years    []BudgetLine `json:"years"`
	// MissingAge units have no age at all and are left out of every line.
	MissingAge int `json:"missing_age"`
}

// BookValueSummary is purchase value against straight-line book value.
type BookValueSummary struct {
	PurchaseTotal float64 `json:"purchase_total"`
	BookTotal     float64 `json:"book_total"`
	Assets        int     `json:"assets"`
	// Excluded assets lack a purchase date or a purchase cost.
	Excluded int `json:"excluded"`
}

// LicenseUsage is a license with the devices it is allocated to and the
// active devices that have it installed.
type LicenseUsage struct {
	License   SoftwareLicense
	Allocated []string // active devices only
}

// LicenseWaste is one license's unused and unproven seats, in rupiah.
type LicenseWaste struct {
	LicenseID    string     `json:"license_id"`
	SoftwareName string     `json:"software_name"`
	LicenseType  string     `json:"license_type"`
	TotalSeats   int        `json:"total_seats"`
	Cost         float64    `json:"cost"`
	SeatCost     *float64   `json:"seat_cost,omitempty"`
	Allocated    int        `json:"allocated"`
	Installed    int        `json:"installed"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	// InUse is the devices that are allocated or have it installed, counted
	// once each.
	InUse       int      `json:"in_use"`
	UnusedSeats int      `json:"unused_seats"`
	UnusedValue *float64 `json:"unused_value,omitempty"`
	// AllocatedNotInstalled are allocated devices whose inventory does not
	// show the software. AllocatedUnverified have no inventory to check.
	AllocatedNotInstalled      int      `json:"allocated_not_installed"`
	AllocatedNotInstalledValue *float64 `json:"allocated_not_installed_value,omitempty"`
	AllocatedUnverified        int      `json:"allocated_unverified"`
	// Applicable is false for a site license, which has no per-seat meaning.
	Applicable bool `json:"applicable"`
}

// LicenseExpiry is a license ending within the window (or already ended).
type LicenseExpiry struct {
	LicenseID    string    `json:"license_id"`
	SoftwareName string    `json:"software_name"`
	ExpiresAt    time.Time `json:"expires_at"`
	DaysLeft     int       `json:"days_left"`
	Cost         float64   `json:"cost"`
}

// WarrantyExpiry is an asset whose warranty ends within the window.
type WarrantyExpiry struct {
	AssetID   string    `json:"asset_id"`
	AssetTag  string    `json:"asset_tag"`
	Hostname  string    `json:"hostname,omitempty"`
	Model     string    `json:"model"`
	ExpiresAt time.Time `json:"expires_at"`
	DaysLeft  int       `json:"days_left"`
}

// LifecycleReport is the whole dashboard.
type LifecycleReport struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Settings    LifecycleSettings `json:"settings"`
	Units       []Unit            `json:"units"`
	AgeSources  map[string]int    `json:"age_sources"`
	AgeBuckets  []AgeBucket       `json:"age_buckets"`
	Overdue     int               `json:"overdue"`
	BelowSpec   int               `json:"below_spec"`
	Forecast    Forecast          `json:"forecast"`
	BookValue   BookValueSummary  `json:"book_value"`

	Licenses              []LicenseWaste `json:"licenses"`
	UnusedSeatValue       float64        `json:"unused_seat_value"`
	AllocatedNotInstValue float64        `json:"allocated_not_installed_value"`

	ExpiringLicenses   []LicenseExpiry  `json:"expiring_licenses"`
	ExpiringWarranties []WarrantyExpiry `json:"expiring_warranties"`
	WarrantiesExpired  int              `json:"warranties_expired"`
}

// ExpiryWindowDays is how far ahead licenses and warranties are listed.
const ExpiryWindowDays = 90

// ForecastQuarters is the forecast horizon: three years.
const ForecastQuarters = 12

const daysPerYear = 365.25

func ageYears(from, now time.Time) float64 {
	y := now.Sub(from).Hours() / 24 / daysPerYear
	if y < 0 {
		return 0
	}
	return y
}

func daysLeft(until, now time.Time) int {
	return int(math.Floor(until.Sub(now).Hours() / 24))
}

func ageBucket(y float64) string {
	switch {
	case y < 1:
		return "0-1"
	case y < 3:
		return "1-3"
	case y < 5:
		return "3-5"
	}
	return "5+"
}

func quarterStart(t time.Time) time.Time {
	q := (int(t.Month()) - 1) / 3
	return time.Date(t.Year(), time.Month(q*3+1), 1, 0, 0, 0, 0, t.Location())
}

// ComputeLifecycle builds the dashboard from facts. now is in the zone the
// quarters and years are cut in (WIB for this deployment).
func ComputeLifecycle(s LifecycleSettings, facts []UnitFacts, licenses []LicenseUsage,
	installed map[string][]string, now time.Time) LifecycleReport {
	rep := LifecycleReport{
		GeneratedAt: now.UTC(),
		Settings:    s,
		Units:       make([]Unit, 0, len(facts)),
		AgeSources: map[string]int{
			AgeFromPurchase: 0, AgeFromBIOS: 0, AgeFromOSInstall: 0, AgeUnknown: 0,
		},
		Licenses:           []LicenseWaste{},
		ExpiringLicenses:   []LicenseExpiry{},
		ExpiringWarranties: []WarrantyExpiry{},
	}
	buckets := map[string]int{}

	// Forecast periods.
	q0 := quarterStart(now)
	for i := 0; i < ForecastQuarters; i++ {
		start := q0.AddDate(0, 3*i, 0)
		rep.Forecast.Quarters = append(rep.Forecast.Quarters, BudgetLine{
			Label: fmt.Sprintf("%d-Q%d", start.Year(), (int(start.Month())-1)/3+1),
			Start: start, End: start.AddDate(0, 3, 0),
		})
	}
	horizonEnd := q0.AddDate(0, 3*ForecastQuarters, 0)
	for y := q0.Year(); y <= horizonEnd.Add(-time.Nanosecond).Year(); y++ {
		start := time.Date(y, 1, 1, 0, 0, 0, 0, now.Location())
		end := start.AddDate(1, 0, 0)
		if start.Before(q0) {
			start = q0
		}
		if end.After(horizonEnd) {
			end = horizonEnd
		}
		rep.Forecast.Years = append(rep.Forecast.Years, BudgetLine{Label: fmt.Sprint(y), Start: start, End: end})
	}
	rep.Forecast.Overdue = BudgetLine{Label: "overdue"}
	addTo := func(l *BudgetLine, u Unit) {
		l.Units++
		if u.ReplacementCost != nil {
			l.Cost += *u.ReplacementCost
		} else {
			l.UnitsWithoutCost++
		}
	}

	for _, f := range facts {
		u := Unit{
			AssetID: f.AssetID, AssetTag: f.AssetTag, DeviceID: f.DeviceID, Hostname: f.Hostname,
			Model: f.Model, Site: f.Site, PurchaseCost: f.PurchaseCost,
			WarrantyExpiresAt: f.WarrantyExpiresAt, BelowSpec: []string{},
		}
		// Category.
		switch {
		case isCategory(f.Category):
			u.Category, u.CategorySource = f.Category, "asset"
		case f.ChassisType != "" && isCategory(f.ChassisType):
			u.Category, u.CategorySource = f.ChassisType, "device"
		default:
			u.Category, u.CategorySource = CategoryOther, "default"
		}
		computer := f.AssetID == "" || isComputer(f.Category)

		// Age: purchase date, else the agent's BIOS date, else OS install.
		switch {
		case f.PurchaseDate != nil:
			u.AgeSource, u.StartDate = AgeFromPurchase, f.PurchaseDate
		case computer && f.BIOSDate != nil:
			u.AgeSource, u.StartDate = AgeFromBIOS, f.BIOSDate
		case computer && f.OSInstall != nil:
			u.AgeSource, u.StartDate = AgeFromOSInstall, f.OSInstall
		default:
			u.AgeSource = AgeUnknown
		}
		rep.AgeSources[u.AgeSource]++

		life := s.Categories[u.Category]
		u.UsefulLifeYears = life.UsefulLifeYears
		switch {
		case life.ReplacementCost > 0:
			c := life.ReplacementCost
			u.ReplacementCost, u.CostSource = &c, "category"
		case f.PurchaseCost > 0:
			c := f.PurchaseCost
			u.ReplacementCost, u.CostSource = &c, "purchase"
		}

		if u.StartDate != nil {
			a := ageYears(*u.StartDate, now)
			u.AgeYears = &a
			buckets[ageBucket(a)]++
			due := u.StartDate.AddDate(life.UsefulLifeYears, 0, 0)
			u.DueDate = &due
			u.Overdue = !due.After(now)
			if u.Overdue {
				rep.Overdue++
				addTo(&rep.Forecast.Overdue, u)
			} else {
				for i := range rep.Forecast.Quarters {
					if q := &rep.Forecast.Quarters[i]; !due.Before(q.Start) && due.Before(q.End) {
						addTo(q, u)
					}
				}
				for i := range rep.Forecast.Years {
					if y := &rep.Forecast.Years[i]; !due.Before(y.Start) && due.Before(y.End) {
						addTo(y, u)
					}
				}
			}
		} else {
			buckets[AgeUnknown]++
			rep.Forecast.MissingAge++
		}

		// Book value: straight line over the useful life, from the purchase
		// date only. An estimated age is not a basis for an accounting value.
		if f.AssetID != "" {
			if f.PurchaseDate != nil && f.PurchaseCost > 0 {
				frac := 1 - ageYears(*f.PurchaseDate, now)/float64(life.UsefulLifeYears)
				if frac < 0 {
					frac = 0
				}
				if frac > 1 {
					frac = 1
				}
				bv := f.PurchaseCost * frac
				u.BookValue = &bv
				rep.BookValue.Assets++
				rep.BookValue.PurchaseTotal += f.PurchaseCost
				rep.BookValue.BookTotal += bv
			} else {
				rep.BookValue.Excluded++
			}
		}

		// Spec floor, from the latest inventory, for computers only.
		if computer && f.HasInventory {
			u.RAMBytes, u.DiskFreePct = f.RAMBytes, f.DiskFreePct
			if s.MinRAMGB > 0 && f.RAMBytes != nil && float64(*f.RAMBytes) < s.MinRAMGB*(1<<30) {
				u.BelowSpec = append(u.BelowSpec, "ram")
			}
			if s.MinDiskFreePercent > 0 && f.DiskFreePct != nil && *f.DiskFreePct < s.MinDiskFreePercent {
				u.BelowSpec = append(u.BelowSpec, "disk")
			}
			if len(u.BelowSpec) > 0 {
				rep.BelowSpec++
			}
		}

		// Warranty.
		if f.AssetID != "" && f.WarrantyExpiresAt != nil {
			dl := daysLeft(*f.WarrantyExpiresAt, now)
			switch {
			case f.WarrantyExpiresAt.Before(now):
				rep.WarrantiesExpired++
			case dl <= ExpiryWindowDays:
				rep.ExpiringWarranties = append(rep.ExpiringWarranties, WarrantyExpiry{
					AssetID: f.AssetID, AssetTag: f.AssetTag, Hostname: f.Hostname, Model: f.Model,
					ExpiresAt: *f.WarrantyExpiresAt, DaysLeft: dl,
				})
			}
		}

		rep.Units = append(rep.Units, u)
	}
	for _, k := range []string{"0-1", "1-3", "3-5", "5+", AgeUnknown} {
		rep.AgeBuckets = append(rep.AgeBuckets, AgeBucket{Key: k, Count: buckets[k]})
	}
	sort.SliceStable(rep.ExpiringWarranties, func(i, j int) bool {
		return rep.ExpiringWarranties[i].ExpiresAt.Before(rep.ExpiringWarranties[j].ExpiresAt)
	})

	// Licenses.
	for _, lu := range licenses {
		l := lu.License
		w := LicenseWaste{
			LicenseID: l.ID, SoftwareName: l.SoftwareName, LicenseType: l.LicenseType,
			TotalSeats: l.TotalSeats, Cost: l.Cost, ExpiresAt: l.ExpiresAt,
			Allocated: len(lu.Allocated), Applicable: l.LicenseType != "site_license",
		}
		needle := strings.ToLower(strings.TrimSpace(l.SoftwareName))
		has := func(dev string) bool {
			for _, n := range installed[dev] {
				if needle != "" && strings.Contains(n, needle) {
					return true
				}
			}
			return false
		}
		inUse := map[string]bool{}
		for dev := range installed {
			if has(dev) {
				w.Installed++
				inUse[dev] = true
			}
		}
		for _, dev := range lu.Allocated {
			inUse[dev] = true
			if _, inv := installed[dev]; !inv {
				w.AllocatedUnverified++
			} else if !has(dev) {
				w.AllocatedNotInstalled++
			}
		}
		w.InUse = len(inUse)
		if w.Applicable {
			if w.UnusedSeats = l.TotalSeats - w.InUse; w.UnusedSeats < 0 {
				w.UnusedSeats = 0
			}
			if l.Cost > 0 && l.TotalSeats > 0 {
				per := l.Cost / float64(l.TotalSeats)
				un := per * float64(w.UnusedSeats)
				ani := per * float64(w.AllocatedNotInstalled)
				w.SeatCost, w.UnusedValue, w.AllocatedNotInstalledValue = &per, &un, &ani
				rep.UnusedSeatValue += un
				rep.AllocatedNotInstValue += ani
			}
		}
		rep.Licenses = append(rep.Licenses, w)

		if l.ExpiresAt != nil {
			if dl := daysLeft(*l.ExpiresAt, now); dl <= ExpiryWindowDays {
				rep.ExpiringLicenses = append(rep.ExpiringLicenses, LicenseExpiry{
					LicenseID: l.ID, SoftwareName: l.SoftwareName, ExpiresAt: *l.ExpiresAt, DaysLeft: dl, Cost: l.Cost,
				})
			}
		}
	}
	sort.SliceStable(rep.ExpiringLicenses, func(i, j int) bool {
		return rep.ExpiringLicenses[i].ExpiresAt.Before(rep.ExpiringLicenses[j].ExpiresAt)
	})
	return rep
}
