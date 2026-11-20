package compliance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
)

// ErrNotFound is returned for a missing profile or report.
var ErrNotFound = errors.New("not found")

// ErrDefaultProfile is returned when deleting the default profile.
var ErrDefaultProfile = errors.New("the default profile cannot be deleted; make another profile the default first")

// Profile is a compliance baseline and the devices it covers.
type Profile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Rules       Rules  `json:"rules"`
	// ScopeSite and ScopeGroupID narrow the profile; both empty = all devices.
	ScopeSite     string    `json:"scope_site"`
	ScopeGroupID  string    `json:"scope_group_id"`
	IsDefault     bool      `json:"is_default"`
	MonthlyReport bool      `json:"monthly_report"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type profileRow struct {
	ID            string    `db:"id"`
	Name          string    `db:"name"`
	Description   string    `db:"description"`
	RulesJSON     string    `db:"rules_json"`
	ScopeSite     string    `db:"scope_site"`
	ScopeGroupID  string    `db:"scope_group_id"`
	IsDefault     bool      `db:"is_default"`
	MonthlyReport bool      `db:"monthly_report"`
	CreatedAt     time.Time `db:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"`
}

func (r profileRow) profile() (Profile, error) {
	p := Profile{ID: r.ID, Name: r.Name, Description: r.Description, ScopeSite: r.ScopeSite,
		ScopeGroupID: r.ScopeGroupID, IsDefault: r.IsDefault, MonthlyReport: r.MonthlyReport,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if err := json.Unmarshal([]byte(r.RulesJSON), &p.Rules); err != nil {
		return p, fmt.Errorf("profile %s rules: %w", r.ID, err)
	}
	return p, nil
}

// Repository is the compliance store.
type Repository struct {
	db *sqlx.DB
}

// NewRepository wraps the database.
func NewRepository(db *sqlx.DB) *Repository { return &Repository{db: db} }

// b2i stores a flag as 0/1: the columns are INTEGER on both databases, and
// PostgreSQL rejects a bool bound to one.
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const profileCols = `id, name, description, rules_json, scope_site, scope_group_id, is_default, monthly_report, created_at, updated_at`

// EnsureDefaultProfile creates the baseline profile on a fresh install.
func (r *Repository) EnsureDefaultProfile(ctx context.Context) error {
	var n int
	if err := r.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM compliance_profiles`); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := r.CreateProfile(ctx, Profile{
		Name:          "Baseline default",
		Description:   "Profil awal: enkripsi disk, antivirus, firewall, patch, password, kunci layar.",
		Rules:         DefaultRules(),
		IsDefault:     true,
		MonthlyReport: true,
	})
	return err
}

// ListProfiles returns every profile, default first.
func (r *Repository) ListProfiles(ctx context.Context) ([]Profile, error) {
	var rows []profileRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+profileCols+` FROM compliance_profiles
		ORDER BY is_default DESC, name ASC`); err != nil {
		return nil, err
	}
	out := make([]Profile, 0, len(rows))
	for _, row := range rows {
		p, err := row.profile()
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// GetProfile returns one profile; id "" means the default profile.
func (r *Repository) GetProfile(ctx context.Context, id string) (Profile, error) {
	var row profileRow
	var err error
	if id == "" {
		err = r.db.GetContext(ctx, &row, `SELECT `+profileCols+` FROM compliance_profiles
			ORDER BY is_default DESC, created_at ASC LIMIT 1`)
	} else {
		err = r.db.GetContext(ctx, &row, `SELECT `+profileCols+` FROM compliance_profiles WHERE id = ?`, id)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	return row.profile()
}

func cleanProfile(p *Profile) error {
	p.Name = strings.TrimSpace(p.Name)
	p.ScopeSite = strings.TrimSpace(p.ScopeSite)
	p.ScopeGroupID = strings.TrimSpace(p.ScopeGroupID)
	if p.Name == "" || len(p.Name) > 120 {
		return fmt.Errorf("name is required (at most 120 characters)")
	}
	if len(p.Description) > 1000 {
		return fmt.Errorf("description is at most 1000 characters")
	}
	return p.Rules.Validate()
}

// CreateProfile stores a new profile.
func (r *Repository) CreateProfile(ctx context.Context, p Profile) (Profile, error) {
	if err := cleanProfile(&p); err != nil {
		return p, err
	}
	rules, _ := json.Marshal(p.Rules)
	now := time.Now().UTC()
	p.ID, p.CreatedAt, p.UpdatedAt = newID(), now, now
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if p.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE compliance_profiles SET is_default = 0`); err != nil {
			return p, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO compliance_profiles (`+profileCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, string(rules), p.ScopeSite, p.ScopeGroupID, b2i(p.IsDefault), b2i(p.MonthlyReport),
		p.CreatedAt, p.UpdatedAt); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// UpdateProfile replaces a profile's fields. Unsetting the only default is
// refused: the console and the monthly report always need one.
func (r *Repository) UpdateProfile(ctx context.Context, p Profile) (Profile, error) {
	if err := cleanProfile(&p); err != nil {
		return p, err
	}
	old, err := r.GetProfile(ctx, p.ID)
	if err != nil {
		return p, err
	}
	if old.IsDefault && !p.IsDefault {
		return p, fmt.Errorf("make another profile the default instead of unsetting this one")
	}
	rules, _ := json.Marshal(p.Rules)
	p.CreatedAt, p.UpdatedAt = old.CreatedAt, time.Now().UTC()
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if p.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE compliance_profiles SET is_default = 0 WHERE id <> ?`, p.ID); err != nil {
			return p, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE compliance_profiles SET name = ?, description = ?, rules_json = ?,
		scope_site = ?, scope_group_id = ?, is_default = ?, monthly_report = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.Description, string(rules), p.ScopeSite, p.ScopeGroupID, b2i(p.IsDefault), b2i(p.MonthlyReport),
		p.UpdatedAt, p.ID); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// DeleteProfile removes a non-default profile. Its archived reports stay.
func (r *Repository) DeleteProfile(ctx context.Context, id string) error {
	p, err := r.GetProfile(ctx, id)
	if err != nil {
		return err
	}
	if p.IsDefault {
		return ErrDefaultProfile
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM compliance_profiles WHERE id = ?`, id)
	return err
}

// GroupName resolves a group id for display; "" when it no longer exists.
func (r *Repository) GroupName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	var name string
	if err := r.db.GetContext(ctx, &name, `SELECT name FROM device_groups WHERE id = ?`, id); err != nil {
		return ""
	}
	return name
}

// Username resolves a user id for the report cover.
func (r *Repository) Username(ctx context.Context, id string) string {
	var name string
	if err := r.db.GetContext(ctx, &name, `SELECT username FROM users WHERE id = ?`, id); err != nil || name == "" {
		return id
	}
	return name
}

type factRow struct {
	ID                 string     `db:"id"`
	Hostname           string     `db:"hostname"`
	OSName             string     `db:"os_name"`
	OSVersion          *string    `db:"os_version"`
	Site               *string    `db:"site"`
	AgentVersion       *string    `db:"agent_version"`
	Status             string     `db:"status"`
	LastSeenAt         *time.Time `db:"last_seen_at"`
	LastPatchScanAt    *time.Time `db:"last_patch_scan_at"`
	PostureJSON        *string    `db:"posture_json"`
	PostureCollectedAt *time.Time `db:"collected_at"`
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// LoadFacts reads every active device in the profile's scope, with its
// posture and its missing security updates. A posture row that does not
// decode is treated as no posture, which evaluates to unknown.
func (r *Repository) LoadFacts(ctx context.Context, p Profile) ([]DeviceFacts, error) {
	q := `SELECT d.id, d.hostname, d.os_name, d.os_version, d.site, d.agent_version, d.status,
			d.last_seen_at, d.last_patch_scan_at, dp.posture_json, dp.collected_at
		FROM devices d
		LEFT JOIN device_posture dp ON dp.device_id = d.id
		WHERE d.retired_at IS NULL`
	var args []any
	if p.ScopeSite != "" {
		q += ` AND d.site = ?`
		args = append(args, p.ScopeSite)
	}
	if p.ScopeGroupID != "" {
		q += ` AND d.id IN (SELECT device_id FROM device_group_members WHERE group_id = ?)`
		args = append(args, p.ScopeGroupID)
	}
	q += ` ORDER BY d.hostname ASC, d.id ASC`
	var rows []factRow
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, fmt.Errorf("load devices: %w", err)
	}

	facts := make([]DeviceFacts, 0, len(rows))
	index := make(map[string]int, len(rows))
	for _, row := range rows {
		f := DeviceFacts{
			ID: row.ID, Hostname: row.Hostname, OSName: row.OSName, OSVersion: deref(row.OSVersion),
			Site: deref(row.Site), AgentVersion: deref(row.AgentVersion), Online: row.Status == "online",
			LastSeenAt: row.LastSeenAt, LastPatchScanAt: row.LastPatchScanAt,
		}
		if row.PostureJSON != nil {
			var pos protocol.Posture
			if err := json.Unmarshal([]byte(*row.PostureJSON), &pos); err == nil {
				f.Posture = &pos
				f.PostureCollectedAt = row.PostureCollectedAt
			}
		}
		index[f.ID] = len(facts)
		facts = append(facts, f)
	}
	if len(facts) == 0 {
		return facts, nil
	}

	var patches []struct {
		DeviceID     string    `db:"device_id"`
		PatchID      string    `db:"patch_id"`
		KB           *string   `db:"kb_id"`
		Title        string    `db:"title"`
		Severity     string    `db:"severity"`
		DiscoveredAt time.Time `db:"discovered_at"`
	}
	if err := r.db.SelectContext(ctx, &patches, `SELECT device_id, patch_id, kb_id, title, severity, discovered_at
		FROM device_patches
		WHERE installed_state = 'missing'
		  AND (category IN ('security', 'critical') OR severity = 'critical')`); err != nil {
		return nil, fmt.Errorf("load missing patches: %w", err)
	}
	for _, p := range patches {
		i, ok := index[p.DeviceID]
		if !ok {
			continue
		}
		facts[i].MissingSecurityPatches = append(facts[i].MissingSecurityPatches, MissingPatch{
			PatchID: p.PatchID, KB: deref(p.KB), Title: p.Title, Severity: p.Severity, DiscoveredAt: p.DiscoveredAt,
		})
	}
	return facts, nil
}

// Evaluate loads the profile's devices and judges them.
func (r *Repository) Evaluate(ctx context.Context, p Profile, now time.Time) ([]DeviceResult, error) {
	facts, err := r.LoadFacts(ctx, p)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceResult, 0, len(facts))
	for _, f := range facts {
		out = append(out, EvaluateDevice(p.Rules, f, now))
	}
	return out, nil
}

// Report is one archived PDF.
type Report struct {
	ID            string    `db:"id" json:"id"`
	ProfileID     string    `db:"profile_id" json:"profile_id"`
	ProfileName   string    `db:"profile_name" json:"profile_name"`
	Period        string    `db:"period" json:"period"`
	Kind          string    `db:"kind" json:"kind"`
	GeneratedBy   string    `db:"generated_by" json:"generated_by"`
	GeneratedAt   time.Time `db:"generated_at" json:"generated_at"`
	FileSHA256    string    `db:"file_sha256" json:"file_sha256"`
	ContentSHA256 string    `db:"content_sha256" json:"content_sha256"`
	SizeBytes     int64     `db:"size_bytes" json:"size_bytes"`
	SummaryJSON   string    `db:"summary_json" json:"-"`
	// FrameworkID is empty for a device compliance report.
	FrameworkID      string            `db:"framework_id" json:"framework_id"`
	FrameworkName    string            `db:"framework_name" json:"framework_name"`
	Summary          *FleetSummary     `db:"-" json:"summary,omitempty"`
	FrameworkSummary *FrameworkSummary `db:"-" json:"framework_summary,omitempty"`
}

func (rep *Report) decodeSummary() {
	if rep.FrameworkID != "" {
		var f FrameworkSummary
		if json.Unmarshal([]byte(rep.SummaryJSON), &f) == nil {
			rep.FrameworkSummary = &f
		}
		return
	}
	var s FleetSummary
	if json.Unmarshal([]byte(rep.SummaryJSON), &s) == nil {
		rep.Summary = &s
	}
}

const reportCols = `id, profile_id, profile_name, period, kind, generated_by, generated_at, file_sha256, content_sha256, size_bytes, summary_json, framework_id, framework_name`

func (r *Repository) insertReport(ctx context.Context, rep Report) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO compliance_reports (`+reportCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rep.ID, rep.ProfileID, rep.ProfileName, rep.Period, rep.Kind, rep.GeneratedBy, rep.GeneratedAt,
		rep.FileSHA256, rep.ContentSHA256, rep.SizeBytes, rep.SummaryJSON, rep.FrameworkID, rep.FrameworkName)
	return err
}

// ListReports returns the newest archived reports.
func (r *Repository) ListReports(ctx context.Context, limit int) ([]Report, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// Non-nil, so an empty archive encodes as [] rather than null.
	rows := []Report{}
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+reportCols+` FROM compliance_reports
		ORDER BY generated_at DESC LIMIT ?`, limit); err != nil {
		return nil, err
	}
	for i := range rows {
		rows[i].decodeSummary()
	}
	return rows, nil
}

// GetReport returns one archived report.
func (r *Repository) GetReport(ctx context.Context, id string) (Report, error) {
	var rep Report
	err := r.db.GetContext(ctx, &rep, `SELECT `+reportCols+` FROM compliance_reports WHERE id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return rep, ErrNotFound
	}
	if err == nil {
		rep.decodeSummary()
	}
	return rep, err
}

// FindByFileHash looks a PDF up by the SHA-256 of its bytes.
func (r *Repository) FindByFileHash(ctx context.Context, sum string) (Report, error) {
	var rep Report
	err := r.db.GetContext(ctx, &rep, `SELECT `+reportCols+` FROM compliance_reports WHERE file_sha256 = ?
		ORDER BY generated_at ASC LIMIT 1`, strings.ToLower(sum))
	if errors.Is(err, sql.ErrNoRows) {
		return rep, ErrNotFound
	}
	if err == nil {
		rep.decodeSummary()
	}
	return rep, err
}

// previousReport is the latest report for a profile, for the trend.
func (r *Repository) previousReport(ctx context.Context, profileID, frameworkID string) (*Report, error) {
	var rep Report
	err := r.db.GetContext(ctx, &rep, `SELECT `+reportCols+` FROM compliance_reports
		WHERE profile_id = ? AND framework_id = ?
		ORDER BY generated_at DESC LIMIT 1`, profileID, frameworkID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rep.decodeSummary()
	return &rep, nil
}

// hasScheduled reports whether a scheduled report exists for a profile and period.
func (r *Repository) hasScheduled(ctx context.Context, profileID, period string) (bool, error) {
	var n int
	err := r.db.GetContext(ctx, &n, `SELECT COUNT(*) FROM compliance_reports
		WHERE profile_id = ? AND period = ? AND kind = 'scheduled' AND framework_id = ''`, profileID, period)
	return n > 0, err
}
