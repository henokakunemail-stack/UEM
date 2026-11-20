package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Report kinds.
const (
	KindManual    = "manual"
	KindScheduled = "scheduled"
)

// AuditAction is the audit log action written for every generated report.
const AuditAction = "compliance.report.generated"

// PreviousReport is the earlier report a new one is compared with.
type PreviousReport struct {
	ReportID    string       `json:"report_id"`
	GeneratedAt time.Time    `json:"generated_at"`
	Summary     FleetSummary `json:"summary"`
}

// Snapshot is everything a report says. Its canonical JSON is what the
// content hash covers, so two PDFs rendered from the same snapshot carry the
// same printed hash.
type Snapshot struct {
	ReportID     string          `json:"report_id"`
	Organization string          `json:"organization"`
	Period       string          `json:"period"`
	Kind         string          `json:"kind"`
	GeneratedAt  time.Time       `json:"generated_at"`
	GeneratedBy  string          `json:"generated_by"`
	ProfileID    string          `json:"profile_id"`
	ProfileName  string          `json:"profile_name"`
	Scope        string          `json:"scope"`
	Rules        Rules           `json:"rules"`
	Summary      FleetSummary    `json:"summary"`
	Previous     *PreviousReport `json:"previous,omitempty"`
	Devices      []DeviceResult  `json:"devices"`
}

// ContentHash is the SHA-256 of the snapshot's JSON.
func ContentHash(s Snapshot) (string, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Auditor appends to the tamper-evident audit log.
type Auditor interface {
	Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error
}

// Generator builds, archives and audits reports.
type Generator struct {
	Repo         *Repository
	Audit        Auditor
	DataDir      string
	Organization string
	Now          func() time.Time

	// Evidence inputs for framework reports.
	BackupDir      string
	BackupInterval time.Duration
	DBDriver       string
	ServerAudit    func(ctx context.Context) (*ServerAuditResult, error)
}

func (g *Generator) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// ReportDir is where archived PDFs live.
func (g *Generator) ReportDir() string { return filepath.Join(g.DataDir, "compliance-reports") }

// ReportPath is one archived PDF's file.
func (g *Generator) ReportPath(id string) string { return filepath.Join(g.ReportDir(), id+".pdf") }

func (g *Generator) scopeText(ctx context.Context, p Profile) string {
	var parts []string
	if p.ScopeSite != "" {
		parts = append(parts, "site "+p.ScopeSite)
	}
	if p.ScopeGroupID != "" {
		name := g.Repo.GroupName(ctx, p.ScopeGroupID)
		if name == "" {
			name = p.ScopeGroupID + " (grup sudah dihapus)"
		}
		parts = append(parts, "grup "+name)
	}
	if len(parts) == 0 {
		return "Semua perangkat aktif"
	}
	return "Perangkat aktif di " + strings.Join(parts, " dan ")
}

// Request is one report to generate.
type Request struct {
	ProfileID string
	Kind      string
	// Period is YYYY-MM; empty means the month of generation.
	Period string
	// ActorType/ActorID are who asked: a user, or "system" for the schedule.
	ActorType string
	ActorID   string
	// ActorName is printed on the cover.
	ActorName string
}

// Generate evaluates the profile now, renders the PDF, writes it to the
// archive and records both hashes in the audit log. A report that could not be
// audited is not kept.
func (g *Generator) Generate(ctx context.Context, req Request) (Report, error) {
	p, err := g.Repo.GetProfile(ctx, req.ProfileID)
	if err != nil {
		return Report{}, err
	}
	now := g.now()
	period := req.Period
	if period == "" {
		period = now.In(reportZone).Format("2006-01")
	}
	if _, err := time.Parse("2006-01", period); err != nil {
		return Report{}, fmt.Errorf("period must be YYYY-MM")
	}
	kind := req.Kind
	if kind != KindScheduled {
		kind = KindManual
	}

	results, err := g.Repo.Evaluate(ctx, p, now)
	if err != nil {
		return Report{}, err
	}
	summary := Summarize(p.Rules, results)
	snap := Snapshot{
		ReportID:     "KP-" + now.In(reportZone).Format("20060102-150405") + "-" + newID()[:6],
		Organization: g.Organization,
		Period:       period,
		Kind:         kind,
		GeneratedAt:  now,
		GeneratedBy:  req.ActorName,
		ProfileID:    p.ID,
		ProfileName:  p.Name,
		Scope:        g.scopeText(ctx, p),
		Rules:        p.Rules,
		Summary:      summary,
		Devices:      results,
	}
	if prev, err := g.Repo.previousReport(ctx, p.ID, ""); err != nil {
		return Report{}, err
	} else if prev != nil && prev.Summary != nil {
		snap.Previous = &PreviousReport{ReportID: prev.ID, GeneratedAt: prev.GeneratedAt, Summary: *prev.Summary}
	}

	contentHash, err := ContentHash(snap)
	if err != nil {
		return Report{}, err
	}
	pdfBytes, err := RenderPDF(snap, contentHash)
	if err != nil {
		return Report{}, fmt.Errorf("render pdf: %w", err)
	}
	fileSum := sha256.Sum256(pdfBytes)
	summaryJSON, _ := json.Marshal(summary)
	rep := Report{
		ID: snap.ReportID, ProfileID: p.ID, ProfileName: p.Name, Period: period, Kind: kind,
		GeneratedBy: req.ActorName, GeneratedAt: now, FileSHA256: hex.EncodeToString(fileSum[:]),
		ContentSHA256: contentHash, SizeBytes: int64(len(pdfBytes)), SummaryJSON: string(summaryJSON),
		Summary: &summary,
	}

	score := "-"
	if summary.Score != nil {
		score = fmt.Sprintf("%.1f", *summary.Score)
	}
	if err := g.store(ctx, rep, pdfBytes, req.ActorType, req.ActorID, map[string]string{
		"devices": fmt.Sprint(summary.Devices),
		"score":   score,
	}); err != nil {
		return Report{}, err
	}
	return rep, nil
}
