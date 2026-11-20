package compliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Disclaimer is printed on every framework report (plan item C4).
const Disclaimer = "Laporan ini adalah paket bukti teknis dari data Endpoint Manager, bukan sertifikasi, hasil audit, " +
	"atau opini hukum. Pemetaan kontrol ke regulasi adalah interpretasi teknis dan sebaiknya ditinjau oleh tim " +
	"kepatuhan atau hukum sebelum diserahkan kepada auditor atau regulator."

// FrameworkSnapshot is everything a framework report says. Its JSON is what
// the printed content hash covers.
type FrameworkSnapshot struct {
	ReportID      string              `json:"report_id"`
	Organization  string              `json:"organization"`
	Period        string              `json:"period"`
	GeneratedAt   time.Time           `json:"generated_at"`
	GeneratedBy   string              `json:"generated_by"`
	Template      Template            `json:"template"`
	ProfileID     string              `json:"profile_id"`
	ProfileName   string              `json:"profile_name"`
	Scope         string              `json:"scope"`
	DeviceSummary FleetSummary        `json:"device_summary"`
	Summary       FrameworkSummary    `json:"summary"`
	Previous      *PreviousFramework  `json:"previous,omitempty"`
	Controls      []ControlAssessment `json:"controls"`
	Evidence      []EvidenceResult    `json:"evidence"`
}

// PreviousFramework is the earlier report of the same framework.
type PreviousFramework struct {
	ReportID    string           `json:"report_id"`
	GeneratedAt time.Time        `json:"generated_at"`
	Summary     FrameworkSummary `json:"summary"`
}

// FrameworkRequest is one framework report to generate.
type FrameworkRequest struct {
	// Framework is a template id or a family (the version in force for the
	// period is chosen).
	Framework string
	ProfileID string
	Period    string
	ActorType string
	ActorID   string
	ActorName string
}

// AssessFramework evaluates a framework now, without archiving anything.
func (g *Generator) AssessFramework(ctx context.Context, framework, profileID, period string) (*FrameworkSnapshot, error) {
	now := g.now()
	if period == "" {
		period = now.In(reportZone).Format("2006-01")
	}
	templates, _ := g.LoadTemplates()
	t, err := SelectTemplate(templates, framework, period)
	if err != nil {
		return nil, err
	}
	p, err := g.Repo.GetProfile(ctx, profileID)
	if err != nil {
		return nil, err
	}
	results, err := g.Repo.Evaluate(ctx, p, now)
	if err != nil {
		return nil, err
	}
	ec := &EvidenceContext{Gen: g, Now: now, Profile: p, Device: Summarize(p.Rules, results), Results: results}
	controls, evidence, sum := ec.Assess(ctx, t)
	return &FrameworkSnapshot{
		Organization: g.Organization, Period: period, GeneratedAt: now, Template: t,
		ProfileID: p.ID, ProfileName: p.Name, Scope: g.scopeText(ctx, p),
		DeviceSummary: ec.Device, Summary: sum, Controls: controls, Evidence: evidence,
	}, nil
}

// GenerateFramework assesses, renders, archives and audits a framework report.
func (g *Generator) GenerateFramework(ctx context.Context, req FrameworkRequest) (Report, error) {
	snap, err := g.AssessFramework(ctx, req.Framework, req.ProfileID, req.Period)
	if err != nil {
		return Report{}, err
	}
	snap.GeneratedBy = req.ActorName
	snap.ReportID = "KR-" + snap.GeneratedAt.In(reportZone).Format("20060102-150405") + "-" + newID()[:6]
	if prev, err := g.Repo.previousReport(ctx, snap.ProfileID, snap.Template.ID); err != nil {
		return Report{}, err
	} else if prev != nil && prev.FrameworkSummary != nil {
		snap.Previous = &PreviousFramework{ReportID: prev.ID, GeneratedAt: prev.GeneratedAt, Summary: *prev.FrameworkSummary}
	}

	b, err := json.Marshal(snap)
	if err != nil {
		return Report{}, err
	}
	sum := sha256.Sum256(b)
	contentHash := hex.EncodeToString(sum[:])
	pdfBytes, err := RenderFrameworkPDF(*snap, contentHash)
	if err != nil {
		return Report{}, fmt.Errorf("render pdf: %w", err)
	}
	fileSum := sha256.Sum256(pdfBytes)
	summaryJSON, _ := json.Marshal(snap.Summary)
	fs := snap.Summary
	rep := Report{
		ID: snap.ReportID, ProfileID: snap.ProfileID, ProfileName: snap.ProfileName, Period: snap.Period,
		Kind: KindManual, GeneratedBy: req.ActorName, GeneratedAt: snap.GeneratedAt,
		FileSHA256: hex.EncodeToString(fileSum[:]), ContentSHA256: contentHash, SizeBytes: int64(len(pdfBytes)),
		SummaryJSON: string(summaryJSON), FrameworkID: snap.Template.ID, FrameworkName: snap.Template.Name,
		FrameworkSummary: &fs,
	}
	if err := g.store(ctx, rep, pdfBytes, req.ActorType, req.ActorID, map[string]string{
		"framework": snap.Template.ID,
		"supported": fmt.Sprint(fs.Supported), "partial": fmt.Sprint(fs.Partial), "gap": fmt.Sprint(fs.Gap),
		"no_data": fmt.Sprint(fs.NoData), "out_of_scope": fmt.Sprint(fs.OutOfScope),
	}); err != nil {
		return Report{}, err
	}
	return rep, nil
}

// store writes the PDF, audits both hashes and archives the row. A report
// that could not be audited is not kept.
func (g *Generator) store(ctx context.Context, rep Report, pdfBytes []byte, actorType, actorID string, extra map[string]string) error {
	if err := os.MkdirAll(g.ReportDir(), 0o750); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	path := g.ReportPath(rep.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pdfBytes, 0o640); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store report: %w", err)
	}
	details := map[string]string{
		"file_sha256": rep.FileSHA256, "content_sha256": rep.ContentSHA256,
		"profile": rep.ProfileName, "period": rep.Period, "kind": rep.Kind,
	}
	for k, v := range extra {
		details[k] = v
	}
	if err := g.Audit.Log(ctx, actorType, actorID, AuditAction, rep.ID, details); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("audit report: %w", err)
	}
	if err := g.Repo.insertReport(ctx, rep); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("archive report: %w", err)
	}
	return nil
}

// AssessLabel is a control or evidence status in Indonesian.
func AssessLabel(s string) string {
	switch s {
	case EvidenceSupported:
		return "Didukung bukti"
	case EvidencePartial:
		return "Sebagian"
	case EvidenceGap:
		return "Ada celah"
	case AssessOutOfScope:
		return "Di luar cakupan aplikasi"
	}
	return "Belum ada data"
}

func assessFill(s string) *rgb {
	switch s {
	case EvidenceSupported:
		return colPass
	case EvidencePartial:
		return colUnknown
	case EvidenceGap:
		return colFail
	}
	return colNA
}

func refText(c ControlAssessment) string {
	if c.RefVerified {
		return c.Ref
	}
	return "(rujukan belum diverifikasi)"
}

// RenderFrameworkPDF draws a framework report.
func RenderFrameworkPDF(s FrameworkSnapshot, contentHash string) ([]byte, error) {
	r := newRenderer("Laporan Bukti Kepatuhan Regulasi", s.ReportID, s.Organization, s.GeneratedAt)
	t := s.Template

	// Cover.
	r.pdf.AddPage()
	r.pdf.Ln(24)
	r.font("B", 22)
	r.pdf.SetTextColor(colHeader.r, colHeader.g, colHeader.b)
	r.pdf.MultiCell(r.cw, 10, r.t("Laporan Bukti Kepatuhan Regulasi"), "", "L", false)
	r.font("B", 13)
	r.pdf.MultiCell(r.cw, 7, r.t(t.Name), "", "L", false)
	r.pdf.SetTextColor(0, 0, 0)
	r.font("", 13)
	org := s.Organization
	if strings.TrimSpace(org) == "" {
		org = "(nama organisasi belum diatur)"
	}
	r.pdf.MultiCell(r.cw, 7, r.t(org), "", "L", false)
	r.pdf.Ln(10)
	r.kv("Regulasi acuan", t.Regulation)
	reviewed := t.ReviewedAt
	if d, err := parseDate(t.ReviewedAt); err == nil {
		reviewed = fmtDate(d)
	}
	r.kv("Template ditinjau", reviewed+" ("+map[string]string{"builtin": "bawaan", "custom": "file kustom " + t.File}[t.Origin]+")")
	r.kv("Periode", PeriodLabel(s.Period))
	r.kv("Status data per", fmtTime(s.GeneratedAt))
	r.kv("Bukti perangkat", "Profil \""+s.ProfileName+"\" · "+s.Scope)
	r.kv("Dibuat oleh", s.GeneratedBy)
	r.kv("Nomor laporan", s.ReportID)
	r.pdf.Ln(10)
	r.font("B", 10)
	r.pdf.SetFillColor(colUnknown.r, colUnknown.g, colUnknown.b)
	r.pdf.MultiCell(r.cw, 5.5, r.t("Penafian: "+Disclaimer), "1", "L", true)

	// 1. Summary.
	r.pdf.AddPage()
	r.heading("1. Ringkasan")
	sum := s.Summary
	r.para(fmt.Sprintf("Template berisi %d kontrol. %d dapat dibuktikan sebagian atau seluruhnya dari data aplikasi, "+
		"dan %d berada di luar cakupan aplikasi (kebijakan, proses, atau kontrol fisik yang harus dibuktikan dengan cara lain).",
		sum.Controls, sum.Controls-sum.OutOfScope, sum.OutOfScope))
	prevCount := func(f func(FrameworkSummary) int) string {
		if s.Previous == nil {
			return "-"
		}
		return fmt.Sprint(f(s.Previous.Summary))
	}
	r.table([]col{{"Status kontrol", 90, "L"}, {"Jumlah", 30, "R"}, {"Laporan sebelumnya", 40, "R"}}, [][]cell{
		{{text: "Didukung bukti", fill: colPass}, {text: fmt.Sprint(sum.Supported)}, {text: prevCount(func(f FrameworkSummary) int { return f.Supported })}},
		{{text: "Sebagian", fill: colUnknown}, {text: fmt.Sprint(sum.Partial)}, {text: prevCount(func(f FrameworkSummary) int { return f.Partial })}},
		{{text: "Ada celah", fill: colFail}, {text: fmt.Sprint(sum.Gap)}, {text: prevCount(func(f FrameworkSummary) int { return f.Gap })}},
		{{text: "Belum ada data", fill: colNA}, {text: fmt.Sprint(sum.NoData)}, {text: prevCount(func(f FrameworkSummary) int { return f.NoData })}},
		{{text: "Di luar cakupan aplikasi"}, {text: fmt.Sprint(sum.OutOfScope)}, {text: prevCount(func(f FrameworkSummary) int { return f.OutOfScope })}},
		{{text: "Total", bold: true}, {text: fmt.Sprint(sum.Controls), bold: true}, {text: prevCount(func(f FrameworkSummary) int { return f.Controls })}},
	})
	if s.Previous != nil {
		r.para(fmt.Sprintf("Laporan sebelumnya: %s (%s).", s.Previous.ReportID, fmtDate(s.Previous.GeneratedAt)))
	}
	if len(t.Pending) > 0 {
		r.sub("Belum tercakup dalam template ini")
		for _, p := range t.Pending {
			r.para("- " + p)
		}
	}

	// 2. Mapped controls.
	r.pdf.AddPage()
	r.heading("2. Kontrol yang dapat dibuktikan aplikasi")
	var rows [][]cell
	for _, c := range s.Controls {
		if c.Status == AssessOutOfScope {
			continue
		}
		var ev []string
		for _, e := range c.Evidence {
			ev = append(ev, fmt.Sprintf("[%s] %s: %s", AssessLabel(e.Status), e.Title, e.Summary))
		}
		text := strings.Join(ev, "\n")
		if c.Note != "" {
			text += "\nCatatan: " + c.Note
		}
		rows = append(rows, []cell{{text: refText(c), bold: true}, {text: c.Title},
			{text: AssessLabel(c.Status), fill: assessFill(c.Status)}, {text: text}})
	}
	if len(rows) == 0 {
		r.para("Tidak ada kontrol yang dipetakan ke bukti aplikasi.")
	} else {
		r.table([]col{{"Rujukan", 24, "L"}, {"Kontrol", 40, "L"}, {"Status", 22, "L"}, {"Bukti", 94, "L"}}, rows)
	}

	// 3. Out of scope.
	r.pdf.AddPage()
	r.heading("3. Di luar cakupan aplikasi")
	r.para("Kontrol berikut tidak dapat dibuktikan dari data Endpoint Manager. Status ini bukan pernyataan bahwa " +
		"kontrol tidak dipenuhi; buktinya harus diperoleh dari sumber lain (dokumen kebijakan, catatan proses, " +
		"pemeriksaan fisik).")
	rows = nil
	for _, c := range s.Controls {
		if c.Status != AssessOutOfScope {
			continue
		}
		reason := ""
		if c.Reason != t.DefaultOutOfScopeReason {
			reason = c.Reason
		}
		rows = append(rows, []cell{{text: refText(c), bold: true}, {text: c.Group}, {text: c.Title}, {text: reason}})
	}
	if len(rows) == 0 {
		r.para("Semua kontrol dalam template ini dipetakan ke bukti aplikasi.")
	} else {
		if t.DefaultOutOfScopeReason != "" {
			r.para("Alasan umum: " + t.DefaultOutOfScopeReason)
		}
		r.table([]col{{"Rujukan", 24, "L"}, {"Kelompok", 40, "L"}, {"Kontrol", 70, "L"}, {"Alasan khusus", 46, "L"}}, rows)
	}

	// 4. Evidence appendix.
	r.pdf.AddPage()
	r.heading("4. Lampiran bukti")
	r.para("Setiap sumber bukti dibaca sekali saat laporan dibuat dan dipakai oleh semua kontrol yang merujuknya.")
	for _, e := range s.Evidence {
		r.ensure(24)
		r.sub(e.Title + " - " + AssessLabel(e.Status))
		r.para(e.Summary)
		for _, d := range e.Details {
			r.para("- " + d)
		}
	}

	// 5. Methodology.
	r.pdf.AddPage()
	r.heading("5. Metodologi dan keterbatasan")
	r.sub("Arti status")
	r.table([]col{{"Status", 40, "L"}, {"Arti", 140, "L"}}, [][]cell{
		{{text: "Didukung bukti", fill: colPass}, {text: "Semua sumber bukti untuk kontrol ini menunjukkan kontrol diterapkan, dan aplikasi dapat membuktikan kontrol tersebut secara utuh."}},
		{{text: "Sebagian", fill: colUnknown}, {text: "Kontrol diterapkan di sebagian perangkat atau sebagian sumber bukti, ATAU aplikasi hanya dapat membuktikan sebagian dari isi kontrol (cakupan sebagian), walaupun datanya sempurna."}},
		{{text: "Ada celah", fill: colFail}, {text: "Data menunjukkan kontrol tidak diterapkan di mana pun."}},
		{{text: "Belum ada data", fill: colNA}, {text: "Belum ada data untuk dinilai (fitur belum dipakai atau perangkat belum melapor). Tidak dianggap terpenuhi."}},
		{{text: "Di luar cakupan aplikasi"}, {text: "Kontrol organisasi, orang, fisik, atau proses yang tidak dapat dibuktikan dari data aplikasi."}},
	})
	r.sub("Sumber template")
	if t.SourceNote != "" {
		r.para(t.SourceNote)
	}
	r.para("Rujukan yang belum diverifikasi dari teks resmi ditulis \"(rujukan belum diverifikasi)\" dan tidak boleh " +
		"dikutip sebagai nomor pasal. Bukti perangkat memakai profil kepatuhan \"" + s.ProfileName + "\"; skor " +
		"perangkat saat laporan dibuat: " + pct(s.DeviceSummary.Score) + fmt.Sprintf(" (%d patuh, %d tidak patuh, %d tidak diketahui).",
		s.DeviceSummary.Compliant, s.DeviceSummary.NonCompliant, s.DeviceSummary.Unknown))
	r.sub("Penafian")
	r.para(Disclaimer)

	r.verification("6", s.ReportID, s.GeneratedAt, contentHash)
	return r.output()
}
