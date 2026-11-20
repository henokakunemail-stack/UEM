package assetlicense

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	rpt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/compliance"
)

var rptRupiah = rpt.Rupiah

// lifecycleNow is 20 November 2026 10:00 WIB.
var lifecycleNow = time.Date(2026, 11, 20, 3, 0, 0, 0, time.UTC)

type recAuditor struct {
	actions []string
	details []map[string]string
}

func (a *recAuditor) Log(_ context.Context, _, _, action, _ string, details map[string]string) error {
	a.actions = append(a.actions, action)
	a.details = append(a.details, details)
	return nil
}

func lifecycleFixture(t *testing.T) (*sqlx.DB, chi.Router, *recAuditor) {
	t.Helper()
	d, _ := assetFixture(t)
	aud := &recAuditor{}
	h := NewHandler(NewRepository(d), aud, func(next http.Handler) http.Handler { return next })
	h.Now = func() time.Time { return lifecycleNow }
	h.Organization = "PT Uji"
	r := chi.NewRouter()
	h.Register(r)
	return d, r, aud
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

type seedAsset struct {
	tag, category, device string
	purchase              string
	cost                  float64
	status                string
}

func insertAsset(t *testing.T, d *sqlx.DB, a seedAsset) {
	t.Helper()
	var pd any
	if a.purchase != "" {
		pd = day(a.purchase)
	}
	var dev any
	if a.device != "" {
		dev = a.device
	}
	if a.status == "" {
		a.status = "in_use"
	}
	now := time.Now().UTC()
	d.MustExec(`INSERT INTO hardware_assets (id, asset_tag, device_id, model_name, purchase_date, purchase_cost,
		status, category, created_at, updated_at) VALUES (?, ?, ?, 'Model', ?, ?, ?, ?, ?, ?)`,
		"id-"+a.tag, a.tag, dev, pd, a.cost, a.status, a.category, now, now)
}

func setInventory(t *testing.T, d *sqlx.DB, device, hw, software, osDetail string) {
	t.Helper()
	now := time.Now().UTC()
	d.MustExec(`INSERT INTO device_inventory (id, device_id, hw, software, os_detail, collected_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, "inv-"+device, device, hw, software, osDetail, now, now)
}

func setPostureBIOS(t *testing.T, d *sqlx.DB, device, bios string) {
	t.Helper()
	now := time.Now().UTC()
	d.MustExec(`INSERT INTO device_posture (device_id, posture_json, disk_encryption, antivirus, firewall,
		password_policy, screen_lock, collected_at, updated_at) VALUES (?, ?, 'unknown', 'unknown', 'unknown',
		'unknown', 'unknown', ?, ?)`, device, `{"schema_version":1,"bios_release_date":"`+bios+`"}`, now, now)
}

func call(t *testing.T, r chi.Router, method, path, body string, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(rbac.WithRole(req.Context(), role))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func getLifecycle(t *testing.T, r chi.Router) LifecycleReport {
	t.Helper()
	rec := call(t, r, http.MethodGet, "/api/assets/lifecycle", "", rbac.RoleViewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("lifecycle: %d %s", rec.Code, rec.Body.String())
	}
	var rep LifecycleReport
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func unitByTag(t *testing.T, rep LifecycleReport, tag string) Unit {
	t.Helper()
	for _, u := range rep.Units {
		if u.AssetTag == tag {
			return u
		}
	}
	t.Fatalf("no unit %s", tag)
	return Unit{}
}

// seedPlanFleet is the test data the plan's acceptance criterion names: ten
// assets, three without a purchase date (two of whose devices report a BIOS
// date, one nothing), and a 10-seat Rp 10.000.000 license allocated to six
// devices and installed on four of them.
func seedPlanFleet(t *testing.T, d *sqlx.DB) {
	t.Helper()
	for i := 1; i <= 10; i++ {
		id := "dev" + string(rune('0'+i%10))
		if i == 10 {
			id = "dev10"
		}
		seedDevice(t, d, id, "PC-"+id)
	}
	insertAsset(t, d, seedAsset{tag: "A01", category: "laptop", device: "dev1", purchase: "2020-01-10", cost: 12_000_000})
	insertAsset(t, d, seedAsset{tag: "A02", category: "desktop", device: "dev2", purchase: "2023-02-01", cost: 9_000_000})
	insertAsset(t, d, seedAsset{tag: "A03", category: "laptop", device: "dev3", purchase: "2024-03-05", cost: 14_000_000})
	insertAsset(t, d, seedAsset{tag: "A04", category: "desktop", device: "dev4", purchase: "2026-05-01", cost: 10_000_000})
	insertAsset(t, d, seedAsset{tag: "A05", category: "desktop", device: "dev5", purchase: "2025-06-01", cost: 8_000_000})
	insertAsset(t, d, seedAsset{tag: "A06", category: "desktop", device: "dev6", purchase: "2025-06-01"})
	insertAsset(t, d, seedAsset{tag: "A07", category: "desktop", device: "dev7", purchase: "2025-06-01", cost: 8_000_000})
	// No purchase date: dev8 reports its BIOS in the posture (Windows) and a
	// desktop chassis; dev9 reports both in the hardware model (Linux); dev10
	// reports nothing at all.
	insertAsset(t, d, seedAsset{tag: "A08", device: "dev8"})
	insertAsset(t, d, seedAsset{tag: "A09", device: "dev9"})
	insertAsset(t, d, seedAsset{tag: "A10", device: "dev10"})
	setPostureBIOS(t, d, "dev8", "2019-03-01")
	setInventory(t, d, "dev8", `{"model":{"product":"OptiPlex","chassis_type":"desktop"}}`, `[]`, `{}`)
	setInventory(t, d, "dev9", `{"model":{"product":"ThinkPad","chassis_type":"laptop","bios_date":"2020-06-15"}}`, `[]`, `{}`)

	office := `[{"name":"Microsoft Office Professional Plus 2021"}]`
	for i := 1; i <= 6; i++ {
		dev := "dev" + string(rune('0'+i))
		sw := `[{"name":"7-Zip"}]`
		if i <= 4 {
			sw = office
		}
		setInventory(t, d, dev, `{}`, sw, `{}`)
	}
	d.MustExec(`UPDATE device_inventory SET hw_ram_bytes = ? WHERE device_id = 'dev2'`, int64(4)<<30)

	repo := NewRepository(d)
	lic := &SoftwareLicense{SoftwareName: "Microsoft Office", LicenseType: "per_device", TotalSeats: 10, Cost: 10_000_000}
	if err := repo.CreateLicense(context.Background(), lic); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 6; i++ {
		if err := repo.AllocateLicense(context.Background(), lic.ID, "dev"+string(rune('0'+i)), "u"); err != nil {
			t.Fatal(err)
		}
	}
	s := DefaultLifecycleSettings()
	s.Categories[CategoryLaptop] = CategoryLife{UsefulLifeYears: 4, ReplacementCost: 15_000_000}
	if err := repo.SaveLifecycleSettings(context.Background(), s, "u"); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleMatchesThePlanCriterion(t *testing.T) {
	d, r, _ := lifecycleFixture(t)
	seedPlanFleet(t, d)
	rep := getLifecycle(t, r)

	if len(rep.Units) != 10 {
		t.Fatalf("units = %d, want 10 (every device has an asset, so no bare devices)", len(rep.Units))
	}
	if rep.AgeSources[AgeFromPurchase] != 7 || rep.AgeSources[AgeFromBIOS] != 2 || rep.AgeSources[AgeUnknown] != 1 {
		t.Errorf("age sources = %v, want 7 purchase, 2 bios, 1 unknown", rep.AgeSources)
	}
	if u := unitByTag(t, rep, "A08"); u.AgeSource != AgeFromBIOS || !u.StartDate.Equal(day("2019-03-01")) ||
		u.Category != CategoryDesktop || u.CategorySource != "device" {
		t.Errorf("A08 = %+v, want the posture BIOS date and the device's desktop chassis", u)
	}
	if u := unitByTag(t, rep, "A09"); u.AgeSource != AgeFromBIOS || !u.StartDate.Equal(day("2020-06-15")) || u.Category != CategoryLaptop {
		t.Errorf("A09 = %+v, want the model BIOS date and a laptop", u)
	}
	if u := unitByTag(t, rep, "A10"); u.AgeSource != AgeUnknown || u.AgeYears != nil || u.DueDate != nil ||
		u.Category != CategoryOther || u.CategorySource != "default" {
		t.Errorf("A10 = %+v, want no age and the default category", u)
	}

	// The unit without an age is in no budget line, only in missing_age.
	f := rep.Forecast
	if f.MissingAge != 1 {
		t.Errorf("missing age = %d, want 1", f.MissingAge)
	}
	inLines := f.Overdue.Units
	for _, y := range f.Years {
		inLines += y.Units
	}
	// A04..A07 are desktops bought in 2025-2026: due 2030-2031, past the horizon.
	if inLines != 5 {
		t.Errorf("units in the forecast = %d, want 5 (3 overdue + 2 in 2028)", inLines)
	}
	// Overdue: A01 (laptop 2020 + 4), A08 (desktop BIOS 2019 + 5), A09
	// (laptop BIOS 2020 + 4). Laptops cost the category's 15M; A08 has no
	// category cost and no purchase cost.
	if f.Overdue.Units != 3 || f.Overdue.Cost != 30_000_000 || f.Overdue.UnitsWithoutCost != 1 || rep.Overdue != 3 {
		t.Errorf("overdue line = %+v (count %d), want 3 units, Rp 30.000.000, 1 without cost", f.Overdue, rep.Overdue)
	}
	// A02 desktop 2023-02-01 + 5 (purchase cost 9M) and A03 laptop
	// 2024-03-05 + 4 (category 15M) both fall in 2028-Q1.
	var q2028 *BudgetLine
	for i := range f.Quarters {
		if f.Quarters[i].Label == "2028-Q1" {
			q2028 = &f.Quarters[i]
		}
	}
	if q2028 == nil || q2028.Units != 2 || q2028.Cost != 24_000_000 {
		t.Errorf("2028-Q1 = %+v, want 2 units, Rp 24.000.000", q2028)
	}
	if len(f.Quarters) != 12 || f.Quarters[0].Label != "2026-Q4" || f.Quarters[11].Label != "2029-Q3" {
		t.Errorf("quarters %s..%s (%d), want 2026-Q4..2029-Q3", f.Quarters[0].Label, f.Quarters[len(f.Quarters)-1].Label, len(f.Quarters))
	}
	var years []string
	for _, y := range f.Years {
		years = append(years, y.Label)
		if y.Label == "2028" && (y.Units != 2 || y.Cost != 24_000_000) {
			t.Errorf("2028 = %+v", y)
		}
	}
	if strings.Join(years, ",") != "2026,2027,2028,2029" {
		t.Errorf("years = %v", years)
	}

	wantBuckets := map[string]int{"0-1": 1, "1-3": 4, "3-5": 1, "5+": 3, "unknown": 1}
	for _, b := range rep.AgeBuckets {
		if b.Count != wantBuckets[b.Key] {
			t.Errorf("bucket %s = %d, want %d", b.Key, b.Count, wantBuckets[b.Key])
		}
	}

	// Book value: only assets with both a purchase date and a cost.
	if rep.BookValue.Assets != 6 || rep.BookValue.Excluded != 4 || rep.BookValue.PurchaseTotal != 61_000_000 {
		t.Errorf("book value = %+v, want 6 assets, 4 excluded, Rp 61.000.000 bought", rep.BookValue)
	}
	a04 := unitByTag(t, rep, "A04")
	want := 10_000_000 * (1 - lifecycleNow.Sub(day("2026-05-01")).Hours()/24/365.25/5)
	if a04.BookValue == nil || math.Abs(*a04.BookValue-want) > 1 {
		t.Errorf("A04 book value = %v, want %.0f", a04.BookValue, want)
	}
	if a01 := unitByTag(t, rep, "A01"); a01.BookValue == nil || *a01.BookValue != 0 {
		t.Errorf("A01 is past its life: book value %v, want 0", a01.BookValue)
	}

	if a02 := unitByTag(t, rep, "A02"); strings.Join(a02.BelowSpec, ",") != "ram" || rep.BelowSpec != 1 {
		t.Errorf("A02 below spec = %v (total %d), want ram only", a02.BelowSpec, rep.BelowSpec)
	}

	if len(rep.Licenses) != 1 {
		t.Fatalf("licenses = %d", len(rep.Licenses))
	}
	l := rep.Licenses[0]
	if l.Allocated != 6 || l.Installed != 4 || l.UnusedSeats != 4 || l.AllocatedNotInstalled != 2 || l.AllocatedUnverified != 0 {
		t.Errorf("license = %+v, want 6 allocated, 4 installed, 4 unused, 2 allocated without install", l)
	}
	if l.UnusedValue == nil || *l.UnusedValue != 4_000_000 || l.AllocatedNotInstalledValue == nil || *l.AllocatedNotInstalledValue != 2_000_000 {
		t.Errorf("license values = %v / %v, want Rp 4.000.000 and Rp 2.000.000", l.UnusedValue, l.AllocatedNotInstalledValue)
	}
}

// A monitor linked to a PC must not take the PC's BIOS date as its age; an
// active device with no asset is still in the fleet; retired devices and
// retired assets are not.
func TestLifecycleFleetBoundaries(t *testing.T) {
	d, r, _ := lifecycleFixture(t)
	seedDevice(t, d, "pc", "PC-ONE")
	seedDevice(t, d, "bare", "PC-BARE")
	seedDevice(t, d, "gone", "PC-GONE")
	d.MustExec(`UPDATE devices SET retired_at = ? WHERE id = 'gone'`, time.Now().UTC())
	setPostureBIOS(t, d, "pc", "2018-01-01")
	setPostureBIOS(t, d, "bare", "2021-07-07")
	setInventory(t, d, "bare", `{"model":{"chassis_type":"laptop"}}`, `[]`, `{"install_timestamp":1500000000}`)
	insertAsset(t, d, seedAsset{tag: "PC", device: "pc", purchase: "2022-01-01"})
	insertAsset(t, d, seedAsset{tag: "MON", category: "other", device: "pc"})
	insertAsset(t, d, seedAsset{tag: "OLD", device: "pc", purchase: "2010-01-01", status: "retired"})
	insertAsset(t, d, seedAsset{tag: "GONE-ASSET", device: "gone", purchase: "2015-01-01", status: "disposed"})

	rep := getLifecycle(t, r)
	if len(rep.Units) != 3 {
		t.Fatalf("units = %d (%+v), want PC, MON and the bare device", len(rep.Units), rep.Units)
	}
	if mon := unitByTag(t, rep, "MON"); mon.AgeSource != AgeUnknown || mon.Category != CategoryOther {
		t.Errorf("monitor = %+v, want no borrowed age", mon)
	}
	var bare *Unit
	for i := range rep.Units {
		if rep.Units[i].DeviceID == "bare" {
			bare = &rep.Units[i]
		}
	}
	if bare == nil || bare.AssetID != "" || bare.Hostname != "PC-BARE" || bare.AgeSource != AgeFromBIOS ||
		!bare.StartDate.Equal(day("2021-07-07")) || bare.Category != CategoryLaptop {
		t.Errorf("bare device = %+v, want its BIOS date and laptop chassis", bare)
	}
	for _, u := range rep.Units {
		if u.DeviceID == "gone" || u.AssetTag == "OLD" {
			t.Errorf("retired unit in the fleet: %+v", u)
		}
	}
}

// With no BIOS date the OS install date is the last estimate; a firmware
// placeholder date is no date.
func TestLifecycleFallsBackToOSInstallAndRejectsPlaceholders(t *testing.T) {
	d, r, _ := lifecycleFixture(t)
	seedDevice(t, d, "a", "A")
	seedDevice(t, d, "b", "B")
	setPostureBIOS(t, d, "a", "1970-01-01")
	setInventory(t, d, "a", `{}`, `[]`, `{"install_timestamp":1600000000}`)
	setInventory(t, d, "b", `{"model":{"bios_date":"0000-00-00"}}`, `[]`, `{"install_timestamp":0}`)
	rep := getLifecycle(t, r)
	for _, u := range rep.Units {
		switch u.DeviceID {
		case "a":
			if u.AgeSource != AgeFromOSInstall || !u.StartDate.Equal(time.Unix(1600000000, 0)) {
				t.Errorf("a = %+v, want the OS install date", u)
			}
		case "b":
			if u.AgeSource != AgeUnknown {
				t.Errorf("b = %+v, want unknown", u)
			}
		}
	}
}

func TestLicenseSeatsAllocatedToDevicesWithoutInventoryAreUnverified(t *testing.T) {
	now := lifecycleNow
	lic := SoftwareLicense{ID: "l", SoftwareName: "Tool", LicenseType: "per_device", TotalSeats: 4, Cost: 400}
	installed := map[string][]string{"a": {"tool pro"}, "b": {}, "x": {"tool"}}
	rep := ComputeLifecycle(DefaultLifecycleSettings(), nil,
		[]LicenseUsage{{License: lic, Allocated: []string{"a", "b", "c"}}}, installed, now)
	l := rep.Licenses[0]
	// in use: a, b, c (allocated) and x (installed without allocation) = 4.
	if l.InUse != 4 || l.UnusedSeats != 0 || l.Installed != 2 || l.AllocatedNotInstalled != 1 || l.AllocatedUnverified != 1 {
		t.Errorf("license = %+v", l)
	}
	if *l.AllocatedNotInstalledValue != 100 {
		t.Errorf("value = %v, want 100", *l.AllocatedNotInstalledValue)
	}

	site := SoftwareLicense{ID: "s", SoftwareName: "Site", LicenseType: "site_license", TotalSeats: 1, Cost: 1000}
	rep = ComputeLifecycle(DefaultLifecycleSettings(), nil, []LicenseUsage{{License: site}}, nil, now)
	if l := rep.Licenses[0]; l.Applicable || l.UnusedSeats != 0 || l.UnusedValue != nil || rep.UnusedSeatValue != 0 {
		t.Errorf("site license = %+v, want no seat waste", l)
	}
}

func TestExpiringLicensesAndWarranties(t *testing.T) {
	now := lifecycleNow
	in := func(d int) *time.Time { t := now.AddDate(0, 0, d); return &t }
	lics := []LicenseUsage{
		{License: SoftwareLicense{ID: "soon", SoftwareName: "Soon", TotalSeats: 1, ExpiresAt: in(30)}},
		{License: SoftwareLicense{ID: "late", SoftwareName: "Late", TotalSeats: 1, ExpiresAt: in(200)}},
		{License: SoftwareLicense{ID: "past", SoftwareName: "Past", TotalSeats: 1, ExpiresAt: in(-10)}},
	}
	facts := []UnitFacts{
		{AssetID: "w1", AssetTag: "W1", WarrantyExpiresAt: in(60)},
		{AssetID: "w2", AssetTag: "W2", WarrantyExpiresAt: in(120)},
		{AssetID: "w3", AssetTag: "W3", WarrantyExpiresAt: in(-1)},
	}
	rep := ComputeLifecycle(DefaultLifecycleSettings(), facts, lics, nil, now)
	if len(rep.ExpiringLicenses) != 2 || rep.ExpiringLicenses[0].LicenseID != "past" || rep.ExpiringLicenses[0].DaysLeft != -10 ||
		rep.ExpiringLicenses[1].DaysLeft != 30 {
		t.Errorf("expiring licenses = %+v", rep.ExpiringLicenses)
	}
	if len(rep.ExpiringWarranties) != 1 || rep.ExpiringWarranties[0].AssetTag != "W1" || rep.WarrantiesExpired != 1 {
		t.Errorf("warranties = %+v, expired %d", rep.ExpiringWarranties, rep.WarrantiesExpired)
	}
}

func TestLifecycleSettingsAreValidatedAndAudited(t *testing.T) {
	d, r, aud := lifecycleFixture(t)
	get := call(t, r, http.MethodGet, "/api/assets/lifecycle/settings", "", rbac.RoleViewer)
	var s LifecycleSettings
	if err := json.Unmarshal(get.Body.Bytes(), &s); err != nil || s.Categories[CategoryLaptop].UsefulLifeYears != 4 || s.MinRAMGB != 8 {
		t.Fatalf("defaults = %s", get.Body.String())
	}

	good := `{"categories":{"laptop":{"useful_life_years":3,"replacement_cost":12000000},"desktop":{"useful_life_years":5,"replacement_cost":0},` +
		`"server":{"useful_life_years":6,"replacement_cost":0},"other":{"useful_life_years":5,"replacement_cost":0}},"min_ram_gb":16,"min_disk_free_percent":15}`
	if rec := call(t, r, http.MethodPut, "/api/assets/lifecycle/settings", good, rbac.RoleTechnician); rec.Code != http.StatusForbidden {
		t.Errorf("technician PUT = %d, want 403", rec.Code)
	}
	for name, body := range map[string]string{
		"life 0":         strings.Replace(good, `"useful_life_years":3`, `"useful_life_years":0`, 1),
		"negative cost":  strings.Replace(good, `"replacement_cost":12000000`, `"replacement_cost":-1`, 1),
		"missing server": strings.Replace(good, `"server":{"useful_life_years":6,"replacement_cost":0},`, ``, 1),
		"unknown field":  strings.Replace(good, `"min_ram_gb":16`, `"min_ram_gb":16,"extra":1`, 1),
		"disk over 100":  strings.Replace(good, `"min_disk_free_percent":15`, `"min_disk_free_percent":101`, 1),
		"extra category": strings.Replace(good, `"other":`, `"tablet":{"useful_life_years":3,"replacement_cost":0},"other":`, 1),
	} {
		if rec := call(t, r, http.MethodPut, "/api/assets/lifecycle/settings", body, rbac.RoleAdmin); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: PUT = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if len(aud.actions) != 0 {
		t.Fatalf("refused settings were audited: %v", aud.actions)
	}
	if rec := call(t, r, http.MethodPut, "/api/assets/lifecycle/settings", good, rbac.RoleAdmin); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body.String())
	}
	// Saving twice updates the one row.
	if rec := call(t, r, http.MethodPut, "/api/assets/lifecycle/settings", good, rbac.RoleAdmin); rec.Code != http.StatusOK {
		t.Fatalf("second PUT = %d %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM asset_lifecycle_settings`); err != nil || n != 1 {
		t.Errorf("settings rows = %d (%v), want 1", n, err)
	}
	if len(aud.actions) != 2 || aud.actions[0] != "asset.lifecycle.settings_update" || aud.details[0]["laptop_life_years"] != "3" ||
		aud.details[0]["laptop_replacement_cost"] != "12000000" {
		t.Errorf("audit = %v %v", aud.actions, aud.details)
	}
	rep := getLifecycle(t, r)
	if rep.Settings.Categories[CategoryLaptop].UsefulLifeYears != 3 || rep.Settings.MinRAMGB != 16 {
		t.Errorf("dashboard settings = %+v", rep.Settings)
	}

	// A stored row that no longer validates is an error, not the defaults.
	d.MustExec(`UPDATE asset_lifecycle_settings SET settings_json = '{"categories":{}}'`)
	if rec := call(t, r, http.MethodGet, "/api/assets/lifecycle", "", rbac.RoleViewer); rec.Code != http.StatusInternalServerError {
		t.Errorf("broken settings: dashboard = %d, want 500", rec.Code)
	}
}

func TestAssetCategoryIsValidatedAndStored(t *testing.T) {
	_, r, _ := lifecycleFixture(t)
	if rec := call(t, r, http.MethodPost, "/api/assets", `{"asset_tag":"X","model_name":"M","category":"tablet"}`, rbac.RoleTechnician); rec.Code != http.StatusBadRequest {
		t.Errorf("bad category create = %d, want 400", rec.Code)
	}
	rec := call(t, r, http.MethodPost, "/api/assets", `{"asset_tag":"X","model_name":"M","category":"laptop"}`, rbac.RoleTechnician)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var a HardwareAsset
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	if rec := call(t, r, http.MethodPut, "/api/assets/"+a.ID, `{"category":"tablet"}`, rbac.RoleTechnician); rec.Code != http.StatusBadRequest {
		t.Errorf("bad category update = %d, want 400", rec.Code)
	}
	if rec := call(t, r, http.MethodPut, "/api/assets/"+a.ID, `{"notes":"n"}`, rbac.RoleTechnician); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	got := call(t, r, http.MethodGet, "/api/assets/"+a.ID, "", rbac.RoleViewer)
	var back HardwareAsset
	_ = json.Unmarshal(got.Body.Bytes(), &back)
	if back.Category != "laptop" {
		t.Errorf("category after a partial update = %q, want laptop kept", back.Category)
	}
}

func pdfText(t *testing.T, pdf []byte) string {
	t.Helper()
	var out strings.Builder
	re := regexp.MustCompile(`(?s)stream\r?\n(.*?)\r?\nendstream`)
	for _, m := range re.FindAllSubmatch(pdf, -1) {
		zr, err := zlib.NewReader(bytes.NewReader(m[1]))
		if err != nil {
			out.Write(m[1])
			continue
		}
		b, _ := io.ReadAll(zr)
		out.Write(b)
	}
	// PDF strings escape their parentheses.
	return strings.NewReplacer(`\(`, "(", `\)`, ")").Replace(out.String())
}

func TestLifecycleReportPDF(t *testing.T) {
	d, r, aud := lifecycleFixture(t)
	seedPlanFleet(t, d)
	rec := call(t, r, http.MethodGet, "/api/assets/lifecycle/report", "", rbac.RoleViewer)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("report = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.Bytes()
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if rec.Header().Get("X-Report-SHA256") != hash {
		t.Errorf("header hash %s, body hash %s", rec.Header().Get("X-Report-SHA256"), hash)
	}
	if len(aud.actions) != 1 || aud.actions[0] != "asset.lifecycle.report" || aud.details[0]["file_sha256"] != hash ||
		aud.details[0]["units"] != "10" || aud.details[0]["overdue"] != "3" {
		t.Errorf("audit = %v %v", aud.actions, aud.details)
	}
	text := pdfText(t, body)
	for _, want := range []string{
		"Ringkasan Siklus Hidup Aset", "PT Uji", "AL-20261120-100000",
		"10 unit, 3 sudah melewati umur pakai",
		"Rp 30.000.000 (+1 unit tanpa biaya)",
		"Perkiraan dari tanggal BIOS",
		"Data kurang: 1 unit",
		"4 seat (Rp 4.000.000)", "2 alokasi (Rp 2.000.000)",
		"RAM di bawah batas",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF lacks %q", want)
		}
	}
	// Same data, same clock: same bytes.
	again := call(t, r, http.MethodGet, "/api/assets/lifecycle/report", "", rbac.RoleViewer)
	if !bytes.Equal(again.Body.Bytes(), body) {
		t.Error("rendering is not deterministic")
	}
}

func TestRupiahIsGroupedInThousands(t *testing.T) {
	for in, want := range map[float64]string{0: "Rp 0", 999: "Rp 999", 1000: "Rp 1.000", 12_500_000: "Rp 12.500.000", 1234.6: "Rp 1.235"} {
		if got := rptRupiah(in); got != want {
			t.Errorf("%v = %q, want %q", in, got, want)
		}
	}
}
