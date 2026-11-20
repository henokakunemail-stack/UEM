package compliance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
)

// Evidence statuses. They say what the application's own data shows about a
// control, never whether the organisation complies with the regulation.
const (
	EvidenceSupported = "supported" // the data shows the control in place
	EvidencePartial   = "partial"   // in place for some devices, or only in part
	EvidenceGap       = "gap"       // the data shows it is not in place
	EvidenceNoData    = "no_data"   // nothing to judge from
)

// EvidenceResult is one evidence source's finding.
type EvidenceResult struct {
	Source  string   `json:"source"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

// ServerAuditResult is the server security audit's headline, supplied by the
// caller so this package does not depend on the audit module.
type ServerAuditResult struct {
	Grade        string
	Score        int
	Passed       int
	Warned       int
	Failed       int
	NotVerified  int
	FailedTitles []string
}

// EvidenceContext is what evidence providers read from.
type EvidenceContext struct {
	Gen     *Generator
	Now     time.Time
	Profile Profile
	// Device is the device evaluation under Profile, computed once.
	Device  FleetSummary
	Results []DeviceResult
}

// evidenceTitles names every known source; an unknown id in a template is
// reported, not silently skipped.
var evidenceTitles = map[string]string{
	"device.disk_encryption":   "Enkripsi disk perangkat",
	"device.antivirus":         "Antivirus perangkat",
	"device.firewall":          "Firewall perangkat",
	"device.patches":           "Update keamanan perangkat",
	"device.password_policy":   "Kebijakan password perangkat",
	"device.screen_lock":       "Kunci layar otomatis",
	"inventory.coverage":       "Inventaris perangkat yang mutakhir",
	"assets.register":          "Register aset perangkat keras",
	"licenses.tracked":         "Pencatatan lisensi perangkat lunak",
	"policy.usb_block":         "Pembatasan media USB",
	"policy.admin_tools_block": "Pemblokiran alat administrasi",
	"policy.applied":           "Penerapan Kebijakan Grup",
	"network.web_filter":       "Penyaringan web",
	"backup.recent":            "Backup database server",
	"audit.chain":              "Audit log berantai hash",
	"access.rbac":              "Akses console berbasis peran",
	"server.security_audit":    "Pemeriksaan keamanan server",
	"software.deployment":      "Distribusi perangkat lunak terkontrol",
}

// KnownEvidence reports whether a source id exists.
func KnownEvidence(id string) bool { _, ok := evidenceTitles[id]; return ok }

// ratioStatus maps "n of total devices are fine" to a status.
func ratioStatus(n, total int) string {
	switch {
	case total == 0:
		return EvidenceNoData
	case n == total:
		return EvidenceSupported
	case n == 0:
		return EvidenceGap
	default:
		return EvidencePartial
	}
}

// Collect evaluates one evidence source.
func (ec *EvidenceContext) Collect(ctx context.Context, ref EvidenceRef) EvidenceResult {
	res := EvidenceResult{Source: ref.Source, Title: evidenceTitles[ref.Source]}
	if res.Title == "" {
		res.Title = ref.Source
		res.Status = EvidenceNoData
		res.Summary = "Sumber bukti tidak dikenal oleh versi server ini; periksa file template."
		return res
	}
	var err error
	if strings.HasPrefix(ref.Source, "device.") {
		ec.deviceEvidence(strings.TrimPrefix(ref.Source, "device."), &res)
		return res
	}
	switch ref.Source {
	case "inventory.coverage":
		err = ec.inventoryCoverage(ctx, &res)
	case "assets.register":
		err = ec.assetsRegister(ctx, &res)
	case "licenses.tracked":
		err = ec.licensesTracked(ctx, &res)
	case "policy.usb_block":
		err = ec.policySettings(ctx, &res, "pembatasan USB (block atau read-only)", func(s map[string]json.RawMessage) bool {
			var mode string
			_ = json.Unmarshal(s["usb_storage_mode"], &mode)
			return mode == "block" || mode == "read_only"
		}, protocol.SettingUSBStorageMode)
	case "policy.admin_tools_block":
		err = ec.policySettings(ctx, &res, "pemblokiran Command Prompt, Registry Editor, dan PowerShell", func(s map[string]json.RawMessage) bool {
			return jsonTrue(s["disable_cmd"]) && jsonTrue(s["disable_registry_tools"]) && jsonTrue(s["disable_powershell"])
		}, protocol.SettingDisableCMD, protocol.SettingDisableRegistryTools, protocol.SettingDisablePowerShell)
	case "policy.applied":
		err = ec.policyApplied(ctx, &res)
	case "network.web_filter":
		err = ec.webFilter(ctx, &res)
	case "backup.recent":
		err = ec.backupRecent(&res, ref.Params)
	case "audit.chain":
		err = ec.auditChain(ctx, &res)
	case "access.rbac":
		err = ec.accessRBAC(ctx, &res)
	case "server.security_audit":
		err = ec.serverAudit(ctx, &res)
	case "software.deployment":
		err = ec.softwareDeployment(ctx, &res)
	}
	if err != nil {
		res.Status = EvidenceNoData
		res.Summary = "Bukti tidak dapat dibaca: " + err.Error()
		res.Details = nil
	}
	return res
}

func jsonTrue(raw json.RawMessage) bool {
	var b bool
	return json.Unmarshal(raw, &b) == nil && b
}

func (ec *EvidenceContext) deviceEvidence(control string, res *EvidenceResult) {
	var cs *ControlSummary
	for i := range ec.Device.Controls {
		if ec.Device.Controls[i].Control == control {
			cs = &ec.Device.Controls[i]
		}
	}
	if cs == nil {
		res.Status = EvidenceNoData
		res.Summary = fmt.Sprintf("Kontrol %s tidak dinilai dalam profil kepatuhan \"%s\".", ControlLabel(control), ec.Profile.Name)
		return
	}
	res.Status = ratioStatus(cs.Pass, cs.Pass+cs.Fail)
	// A device that could not be judged cannot be vouched for: the control
	// is at best partly evidenced while any device is unknown.
	if res.Status == EvidenceSupported && cs.Unknown > 0 {
		res.Status = EvidencePartial
	}
	res.Summary = fmt.Sprintf("Lulus di %d dari %d perangkat yang dapat dinilai (profil \"%s\")", cs.Pass, cs.Pass+cs.Fail, ec.Profile.Name)
	if cs.Unknown > 0 {
		res.Summary += fmt.Sprintf("; %d perangkat tidak dapat dinilai", cs.Unknown)
	}
	res.Summary += "."
	var failing []string
	for _, d := range ec.Results {
		if c, ok := d.Control(control); ok && c.Status == StatusFail {
			failing = append(failing, d.Device.Hostname+": "+c.Reason)
		}
	}
	res.Details = capList(failing, 15, "perangkat lain gagal")
}

func capList(items []string, max int, more string) []string {
	if len(items) <= max {
		return items
	}
	return append(items[:max:max], fmt.Sprintf("... dan %d %s", len(items)-max, more))
}

func (ec *EvidenceContext) activeDevices(ctx context.Context) (int, error) {
	var n int
	err := ec.Gen.Repo.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM devices WHERE retired_at IS NULL`)
	return n, err
}

func (ec *EvidenceContext) inventoryCoverage(ctx context.Context, res *EvidenceResult) error {
	total, err := ec.activeDevices(ctx)
	if err != nil {
		return err
	}
	cutoff := ec.Now.Add(-7 * 24 * time.Hour)
	var fresh int
	if err := ec.Gen.Repo.db.GetContext(ctx, &fresh, `SELECT COUNT(*) FROM devices d
		JOIN device_inventory i ON i.device_id = d.id
		WHERE d.retired_at IS NULL AND i.collected_at >= ?`, cutoff); err != nil {
		return err
	}
	res.Status = ratioStatus(fresh, total)
	res.Summary = fmt.Sprintf("%d dari %d perangkat aktif mengirim inventory dalam 7 hari terakhir.", fresh, total)
	return nil
}

func (ec *EvidenceContext) assetsRegister(ctx context.Context, res *EvidenceResult) error {
	total, err := ec.activeDevices(ctx)
	if err != nil {
		return err
	}
	var linked, assets int
	if err := ec.Gen.Repo.db.GetContext(ctx, &assets, `SELECT COUNT(*) FROM hardware_assets
		WHERE status NOT IN ('retired', 'disposed')`); err != nil {
		return err
	}
	if err := ec.Gen.Repo.db.GetContext(ctx, &linked, `SELECT COUNT(DISTINCT d.id) FROM devices d
		JOIN hardware_assets a ON a.device_id = d.id
		WHERE d.retired_at IS NULL AND a.status NOT IN ('retired', 'disposed')`); err != nil {
		return err
	}
	if assets == 0 {
		res.Status = EvidenceGap
		res.Summary = "Belum ada aset perangkat keras yang dicatat di register aset."
		return nil
	}
	res.Status = ratioStatus(linked, total)
	res.Summary = fmt.Sprintf("%d aset perangkat keras aktif tercatat; %d dari %d perangkat aktif tertaut ke aset.", assets, linked, total)
	return nil
}

func (ec *EvidenceContext) licensesTracked(ctx context.Context, res *EvidenceResult) error {
	var rows []struct {
		Name      string     `db:"software_name"`
		Seats     int        `db:"total_seats"`
		ExpiresAt *time.Time `db:"expires_at"`
		Allocated int        `db:"allocated"`
	}
	if err := ec.Gen.Repo.db.SelectContext(ctx, &rows, `SELECT l.software_name, l.total_seats, l.expires_at,
		(SELECT COUNT(*) FROM license_allocations a JOIN devices d ON d.id = a.device_id
		  WHERE a.license_id = l.id AND d.retired_at IS NULL) AS allocated
		FROM software_licenses l ORDER BY l.software_name`); err != nil {
		return err
	}
	if len(rows) == 0 {
		res.Status = EvidenceNoData
		res.Summary = "Belum ada lisensi perangkat lunak yang dicatat."
		return nil
	}
	var problems []string
	for _, r := range rows {
		if r.ExpiresAt != nil && r.ExpiresAt.Before(ec.Now) {
			problems = append(problems, fmt.Sprintf("%s: kedaluwarsa %s", r.Name, r.ExpiresAt.Format("2006-01-02")))
		}
		if r.Seats > 0 && r.Allocated > r.Seats {
			problems = append(problems, fmt.Sprintf("%s: %d alokasi melebihi %d seat", r.Name, r.Allocated, r.Seats))
		}
	}
	res.Summary = fmt.Sprintf("%d lisensi tercatat", len(rows))
	if len(problems) == 0 {
		res.Status = EvidenceSupported
		res.Summary += "; tidak ada yang kedaluwarsa atau melebihi seat."
		return nil
	}
	res.Status = EvidencePartial
	res.Summary += fmt.Sprintf("; %d bermasalah.", len(problems))
	res.Details = capList(problems, 15, "lisensi lain bermasalah")
	return nil
}

type policyStateRow struct {
	Hostname string `db:"hostname"`
	Status   string `db:"status"`
	Applied  string `db:"applied_settings_json"`
	Results  string `db:"results_json"`
}

func (ec *EvidenceContext) policyStates(ctx context.Context) ([]policyStateRow, int, error) {
	total, err := ec.activeDevices(ctx)
	if err != nil {
		return nil, 0, err
	}
	var rows []policyStateRow
	err = ec.Gen.Repo.db.SelectContext(ctx, &rows, `SELECT d.hostname, s.status,
		COALESCE(s.applied_settings_json, '') AS applied_settings_json, COALESCE(s.results_json, '[]') AS results_json
		FROM devices d JOIN device_system_policy_states s ON s.device_id = d.id
		WHERE d.retired_at IS NULL ORDER BY d.hostname`)
	return rows, total, err
}

// policySettings counts devices whose applied policy sets the wanted value and
// whose agent reported every listed setting as applied. A setting the agent
// could not apply, or never reported, does not count.
func (ec *EvidenceContext) policySettings(ctx context.Context, res *EvidenceResult, what string,
	wanted func(map[string]json.RawMessage) bool, keys ...string) error {
	rows, total, err := ec.policyStates(ctx)
	if err != nil {
		return err
	}
	ok := 0
	var notes []string
	for _, r := range rows {
		var settings map[string]json.RawMessage
		_ = json.Unmarshal([]byte(r.Applied), &settings)
		if settings == nil || !wanted(settings) {
			continue
		}
		var results []protocol.SettingResult
		_ = json.Unmarshal([]byte(r.Results), &results)
		outcome := map[string]protocol.SettingResult{}
		for _, x := range results {
			outcome[x.Setting] = x
		}
		applied := true
		for _, k := range keys {
			if o, found := outcome[k]; !found || o.Outcome != protocol.OutcomeApplied {
				applied = false
				reason := "belum dilaporkan"
				if found {
					reason = o.Outcome
					if o.Reason != "" {
						reason += " (" + o.Reason + ")"
					}
				}
				notes = append(notes, fmt.Sprintf("%s: %s %s", r.Hostname, k, reason))
			}
		}
		if applied {
			ok++
		}
	}
	res.Status = ratioStatus(ok, total)
	res.Summary = fmt.Sprintf("%s diterapkan dan dikonfirmasi agen di %d dari %d perangkat aktif.", capFirst(what), ok, total)
	res.Details = capList(notes, 15, "catatan lain")
	return nil
}

func (ec *EvidenceContext) policyApplied(ctx context.Context, res *EvidenceResult) error {
	var policies int
	if err := ec.Gen.Repo.db.GetContext(ctx, &policies, `SELECT COUNT(*) FROM system_policies WHERE is_enabled = 1`); err != nil {
		return err
	}
	if policies == 0 {
		res.Status = EvidenceGap
		res.Summary = "Tidak ada Kebijakan Grup yang aktif."
		return nil
	}
	rows, total, err := ec.policyStates(ctx)
	if err != nil {
		return err
	}
	synced := 0
	var notes []string
	for _, r := range rows {
		if r.Status == "synced" {
			synced++
		} else {
			notes = append(notes, r.Hostname+": "+r.Status)
		}
	}
	res.Status = ratioStatus(synced, total)
	res.Summary = fmt.Sprintf("%d Kebijakan Grup aktif; diterapkan penuh di %d dari %d perangkat aktif.", policies, synced, total)
	res.Details = capList(notes, 15, "perangkat lain")
	return nil
}

func (ec *EvidenceContext) webFilter(ctx context.Context, res *EvidenceResult) error {
	var policies int
	if err := ec.Gen.Repo.db.GetContext(ctx, &policies, `SELECT COUNT(*) FROM filter_policies WHERE is_enabled = 1`); err != nil {
		return err
	}
	if policies == 0 {
		res.Status = EvidenceGap
		res.Summary = "Tidak ada kebijakan filter jaringan yang aktif."
		return nil
	}
	total, err := ec.activeDevices(ctx)
	if err != nil {
		return err
	}
	var synced, degraded int
	if err := ec.Gen.Repo.db.GetContext(ctx, &synced, `SELECT COUNT(*) FROM device_filter_states f
		JOIN devices d ON d.id = f.device_id
		WHERE d.retired_at IS NULL AND f.status = 'synced' AND f.rules_applied > 0`); err != nil {
		return err
	}
	if err := ec.Gen.Repo.db.GetContext(ctx, &degraded, `SELECT COUNT(*) FROM device_filter_states f
		JOIN devices d ON d.id = f.device_id
		WHERE d.retired_at IS NULL AND f.status = 'degraded'`); err != nil {
		return err
	}
	res.Status = ratioStatus(synced, total)
	res.Summary = fmt.Sprintf("Filter diterapkan penuh di %d dari %d perangkat aktif", synced, total)
	if degraded > 0 {
		res.Summary += fmt.Sprintf("; %d perangkat hanya sebagian (lapisan firewall ditolak)", degraded)
	}
	res.Summary += "."
	return nil
}

// backupRecent reads the newest SQLite backup's file time. A PostgreSQL
// deployment is backed up outside the application, so there is nothing here
// to show.
func (ec *EvidenceContext) backupRecent(res *EvidenceResult, params map[string]any) error {
	g := ec.Gen
	if g.DBDriver != "" && !strings.HasPrefix(g.DBDriver, "sqlite") {
		res.Status = EvidenceNoData
		res.Summary = "Database PostgreSQL: backup dikelola di luar aplikasi, tidak ada bukti di sini."
		return nil
	}
	maxAge := 2 * g.BackupInterval
	if v, ok := params["max_age_hours"].(float64); ok && v > 0 {
		maxAge = time.Duration(v * float64(time.Hour))
	}
	if maxAge <= 0 {
		maxAge = 48 * time.Hour
	}
	if g.BackupDir == "" {
		res.Status = EvidenceNoData
		res.Summary = "Direktori backup tidak diatur."
		return nil
	}
	entries, err := os.ReadDir(g.BackupDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var newest time.Time
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".db" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		count++
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if count == 0 {
		res.Status = EvidenceGap
		res.Summary = "Tidak ada file backup database di " + g.BackupDir + "."
		return nil
	}
	age := ec.Now.Sub(newest)
	res.Summary = fmt.Sprintf("%d file backup; terbaru %s (%s lalu, batas %s).", count, fmtTime(newest),
		humanDuration(age), humanDuration(maxAge))
	if age > maxAge {
		res.Status = EvidenceGap
	} else {
		res.Status = EvidenceSupported
	}
	return nil
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%d hari", int(d.Hours()/24))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%d jam", int(d.Hours()))
	}
	return fmt.Sprintf("%d menit", int(d.Minutes()))
}

func (ec *EvidenceContext) auditChain(ctx context.Context, res *EvidenceResult) error {
	rep, err := audit.VerifyChain(ctx, ec.Gen.Repo.db)
	if err != nil {
		return err
	}
	switch {
	case !rep.OK:
		res.Status = EvidenceGap
		res.Summary = fmt.Sprintf("Rantai hash audit log PUTUS di entri %s: %s.", rep.BreakID, rep.Reason)
	case rep.Checked == 0:
		res.Status = EvidenceNoData
		res.Summary = "Belum ada entri audit log yang berantai hash."
	default:
		res.Status = EvidenceSupported
		res.Summary = fmt.Sprintf("%d entri audit log terverifikasi utuh (tamper-evident).", rep.Checked)
	}
	return nil
}

func (ec *EvidenceContext) accessRBAC(ctx context.Context, res *EvidenceResult) error {
	var rows []struct {
		Role string `db:"role"`
		N    int    `db:"n"`
	}
	if err := ec.Gen.Repo.db.SelectContext(ctx, &rows, `SELECT role, COUNT(*) AS n FROM users
		WHERE is_active = 1 GROUP BY role ORDER BY role`); err != nil {
		return err
	}
	if len(rows) == 0 {
		res.Status = EvidenceNoData
		res.Summary = "Tidak ada pengguna console yang aktif."
		return nil
	}
	var parts []string
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s %d", r.Role, r.N))
	}
	sort.Strings(parts)
	// The console has roles and revocable sessions, but no second factor:
	// that is a real gap for authentication controls, so the result is
	// partial rather than supported.
	res.Status = EvidencePartial
	res.Summary = "Pengguna console aktif per peran: " + strings.Join(parts, ", ") +
		". Sesi tercatat di server dan dapat dicabut. Autentikasi multi-faktor (MFA) belum tersedia."
	return nil
}

func (ec *EvidenceContext) serverAudit(ctx context.Context, res *EvidenceResult) error {
	if ec.Gen.ServerAudit == nil {
		res.Status = EvidenceNoData
		res.Summary = "Pemeriksaan keamanan server tidak tersedia."
		return nil
	}
	r, err := ec.Gen.ServerAudit(ctx)
	if err != nil {
		return err
	}
	res.Summary = fmt.Sprintf("Nilai %s (skor %d): %d lulus, %d peringatan, %d gagal, %d tidak terverifikasi.",
		r.Grade, r.Score, r.Passed, r.Warned, r.Failed, r.NotVerified)
	switch {
	case r.Passed == 0:
		res.Status = EvidenceNoData
	case r.Failed == 0:
		res.Status = EvidenceSupported
	default:
		res.Status = EvidencePartial
		res.Details = capList(r.FailedTitles, 15, "pemeriksaan lain gagal")
	}
	return nil
}

func (ec *EvidenceContext) softwareDeployment(ctx context.Context, res *EvidenceResult) error {
	since := ec.Now.AddDate(0, 0, -90)
	var deployments, ok, failed int
	if err := ec.Gen.Repo.db.GetContext(ctx, &deployments, `SELECT COUNT(*) FROM software_deployments WHERE created_at >= ?`, since); err != nil {
		return err
	}
	if deployments == 0 {
		res.Status = EvidenceNoData
		res.Summary = "Tidak ada distribusi perangkat lunak dalam 90 hari terakhir."
		return nil
	}
	if err := ec.Gen.Repo.db.GetContext(ctx, &ok, `SELECT COUNT(*) FROM deployment_tasks WHERE created_at >= ? AND status = 'success'`, since); err != nil {
		return err
	}
	if err := ec.Gen.Repo.db.GetContext(ctx, &failed, `SELECT COUNT(*) FROM deployment_tasks WHERE created_at >= ? AND status = 'failed'`, since); err != nil {
		return err
	}
	res.Status = EvidenceSupported
	res.Summary = fmt.Sprintf("%d distribusi dalam 90 hari terakhir, tercatat dengan pelaksana dan hasil per perangkat (%d berhasil, %d gagal).",
		deployments, ok, failed)
	return nil
}
