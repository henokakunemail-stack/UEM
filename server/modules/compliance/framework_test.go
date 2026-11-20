package compliance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadBuiltin(t *testing.T, g *Generator) map[string]Template {
	t.Helper()
	ts, errs := g.LoadTemplates()
	if len(errs) > 0 {
		t.Fatalf("built-in templates do not load: %+v", errs)
	}
	m := map[string]Template{}
	for _, x := range ts {
		m[x.ID] = x
	}
	return m
}

func TestBuiltinTemplatesLoad(t *testing.T) {
	g := newGen(t, newTestDB(t))
	m := loadBuiltin(t, g)
	for _, id := range []string{"iso27001-2022", "uu-pdp-27-2022", "ojk-bank-umum-11-2022", "ojk-bpr-34-2025", "ojk-bpr-75-2016", "ojk-ljknb-4-2021"} {
		if _, ok := m[id]; !ok {
			t.Errorf("built-in template %s missing", id)
		}
	}
	iso := m["iso27001-2022"]
	if len(iso.Controls) != 93 {
		t.Fatalf("ISO 27001:2022 Annex A has 93 controls, template has %d", len(iso.Controls))
	}
	groups := map[string]int{}
	for _, c := range iso.Controls {
		groups[strings.SplitN(c.Group, " ", 2)[0]]++
	}
	if groups["5"] != 37 || groups["6"] != 8 || groups["7"] != 14 || groups["8"] != 34 {
		t.Fatalf("Annex A themes 37/8/14/34, got %v", groups)
	}
}

// The plan's acceptance case: every mapped ISO control has a status, and the
// unmapped ones are listed as out of scope, never as compliant.
func TestISOAssessmentCoversEveryControl(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	snap, err := g.AssessFramework(context.Background(), "iso27001", "", "2026-11")
	if err != nil {
		t.Fatal(err)
	}
	s := snap.Summary
	if s.Controls != 93 || s.Supported+s.Partial+s.Gap+s.NoData+s.OutOfScope != 93 {
		t.Fatalf("summary does not add up: %+v", s)
	}
	if s.OutOfScope != 93-25 {
		t.Fatalf("out of scope = %d, want 68", s.OutOfScope)
	}
	byRef := map[string]ControlAssessment{}
	for _, c := range snap.Controls {
		byRef[c.Ref] = c
	}
	if c := byRef["A.6.1"]; c.Status != AssessOutOfScope || c.Reason == "" {
		t.Fatalf("A.6.1 should be out of scope with a reason: %+v", c)
	}
	// A.8.7 malware: 2 of 2 evaluable devices pass, but the old agent could
	// not be judged, so it is not fully evidenced.
	if c := byRef["A.8.7"]; c.Status != EvidencePartial {
		t.Fatalf("A.8.7 = %+v", c)
	}
	// A.8.8 patches: all three devices were scanned and pass, coverage partial.
	if c := byRef["A.8.8"]; c.Status != EvidencePartial || c.Evidence[0].Status != EvidenceSupported {
		t.Fatalf("A.8.8 = %+v", c)
	}
	// A.8.24 cryptography: one device has BitLocker off -> partial.
	if c := byRef["A.8.24"]; c.Status != EvidencePartial {
		t.Fatalf("A.8.24 = %+v", c)
	}
}

func TestCombineEvidenceAndCoverageCap(t *testing.T) {
	e := func(ss ...string) []EvidenceResult {
		var out []EvidenceResult
		for _, s := range ss {
			out = append(out, EvidenceResult{Status: s})
		}
		return out
	}
	cases := []struct {
		in   []EvidenceResult
		want string
	}{
		{e(EvidenceSupported, EvidenceSupported), EvidenceSupported},
		{e(EvidenceSupported, EvidenceNoData), EvidencePartial},
		{e(EvidenceSupported, EvidenceGap), EvidencePartial},
		{e(EvidenceGap, EvidenceNoData), EvidenceGap},
		{e(EvidenceNoData, EvidenceNoData), EvidenceNoData},
		{e(EvidencePartial), EvidencePartial},
		{nil, EvidenceNoData},
	}
	for _, c := range cases {
		if got := combineEvidence(c.in); got != c.want {
			t.Errorf("combine(%v) = %s, want %s", c.in, got, c.want)
		}
	}

	d := newTestDB(t)
	seedDevice(t, d, "PC-OK", "", goodPosture(), true)
	g := newGen(t, d)
	ec := &EvidenceContext{Gen: g, Now: time.Now().UTC()}
	p, _ := g.Repo.GetProfile(context.Background(), "")
	ec.Profile = p
	ec.Results, _ = g.Repo.Evaluate(context.Background(), p, ec.Now)
	ec.Device = Summarize(p.Rules, ec.Results)
	tpl := Template{ID: "x", Name: "x", Controls: []TemplateControl{
		{Ref: "full", Title: "t", Evidence: []EvidenceRef{{Source: "device.antivirus"}}, Coverage: CoverageFull},
		{Ref: "part", Title: "t", Evidence: []EvidenceRef{{Source: "device.antivirus"}}, Coverage: CoveragePartial},
	}}
	got, catalog, _ := ec.Assess(context.Background(), tpl)
	if got[0].Status != EvidenceSupported || got[1].Status != EvidencePartial {
		t.Fatalf("partial coverage must cap a perfect result at partial: %s / %s", got[0].Status, got[1].Status)
	}
	if len(catalog) != 1 {
		t.Fatalf("the same evidence should be collected once, catalog has %d", len(catalog))
	}
}

// Version selection inside one family follows the period: the BPR template
// switches regulation in December 2026.
func TestSelectTemplateByPeriod(t *testing.T) {
	g := newGen(t, newTestDB(t))
	ts, _ := g.LoadTemplates()
	for period, want := range map[string]string{
		"2026-11": "ojk-bpr-75-2016",
		"2026-12": "ojk-bpr-34-2025", // judged on the period's last day
		"2027-03": "ojk-bpr-34-2025",
	} {
		got, err := SelectTemplate(ts, "ojk-bpr", period)
		if err != nil || got.ID != want {
			t.Errorf("%s: got %s (%v), want %s", period, got.ID, err, want)
		}
	}
	if got, err := SelectTemplate(ts, "ojk-bpr-75-2016", "2027-03"); err != nil || got.ID != "ojk-bpr-75-2016" {
		t.Errorf("an explicit id must be honoured regardless of period: %s %v", got.ID, err)
	}
	if _, err := SelectTemplate(ts, "nope", "2026-11"); err != ErrNotFound {
		t.Errorf("unknown framework: %v", err)
	}
}

// Editing a template on disk changes the next report without a rebuild; a
// broken file is reported and the built-in one stays in use.
func TestCustomTemplateOverridesWithoutRebuild(t *testing.T) {
	g := newGen(t, newTestDB(t))
	if err := os.MkdirAll(g.TemplateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := builtinFrameworks.ReadFile("frameworks/uu-pdp-27-2022.json")
	var tpl map[string]any
	_ = json.Unmarshal(b, &tpl)
	controls := tpl["controls"].([]any)
	tpl["controls"] = append(controls, map[string]any{"ref": "Pasal 99", "title": "Kontrol tambahan uji", "evidence": []string{"audit.chain"}})
	out, _ := json.Marshal(tpl)
	if err := os.WriteFile(filepath.Join(g.TemplateDir(), "pdp.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.TemplateDir(), "broken.json"), []byte(`{"id":"iso27001-2022","name":"x","controls":[{"ref":"a","title":"b","evidence":["no.such.source"]}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, errs := g.LoadTemplates()
	if len(errs) != 1 || errs[0].File != "broken.json" || !strings.Contains(errs[0].Error, "no.such.source") {
		t.Fatalf("broken file should be reported: %+v", errs)
	}
	var pdp, iso Template
	for _, x := range ts {
		switch x.ID {
		case "uu-pdp-27-2022":
			pdp = x
		case "iso27001-2022":
			iso = x
		}
	}
	if pdp.Origin != "custom" || pdp.Controls[len(pdp.Controls)-1].Ref != "Pasal 99" {
		t.Fatalf("custom template not used: origin %s", pdp.Origin)
	}
	if iso.Origin != "builtin" || len(iso.Controls) != 93 {
		t.Fatal("a broken custom file must not replace the built-in template")
	}
}

func TestGenerateFrameworkReport(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	ctx := context.Background()
	rep, err := g.GenerateFramework(ctx, FrameworkRequest{Framework: "ojk-bpr", Period: "2026-11", ActorType: "user", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.FrameworkID != "ojk-bpr-75-2016" || rep.FrameworkSummary == nil || !strings.HasPrefix(rep.ID, "KR-") {
		t.Fatalf("report %+v", rep)
	}
	pdf, err := os.ReadFile(g.ReportPath(rep.ID))
	if err != nil {
		t.Fatal(err)
	}
	text := pdfText(t, pdf)
	for _, want := range []string{rep.ContentSHA256, "Laporan Bukti Kepatuhan Regulasi", "Penafian", "bukan sertifikasi",
		"rujukan belum", "Di luar cakupan aplikasi"} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF text does not contain %q", want)
		}
	}

	// Archived and listed with its framework; the device report trend and the
	// monthly schedule do not see it.
	reps, _ := g.Repo.ListReports(ctx, 10)
	if len(reps) != 1 || reps[0].FrameworkName == "" || reps[0].FrameworkSummary == nil || reps[0].Summary != nil {
		t.Fatalf("archive %+v", reps)
	}
	p, _ := g.Repo.GetProfile(ctx, "")
	if prev, _ := g.Repo.previousReport(ctx, p.ID, ""); prev != nil {
		t.Fatal("a framework report leaked into the device report trend")
	}
	var details string
	if err := d.Get(&details, `SELECT details FROM audit_logs WHERE action = ? AND target_id = ?`, AuditAction, rep.ID); err != nil || !strings.Contains(details, rep.FileSHA256) {
		t.Fatalf("audit entry: %v %s", err, details)
	}

	rep2, err := g.GenerateFramework(ctx, FrameworkRequest{Framework: "ojk-bpr-75-2016", ActorType: "user", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pdf2, _ := os.ReadFile(g.ReportPath(rep2.ID))
	if !strings.Contains(pdfText(t, pdf2), rep.ID) {
		t.Fatal("the second framework report does not reference the first")
	}
}

func TestFrameworkEndpoints(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	h := serve(g)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compliance/frameworks", nil))
	var list struct {
		Frameworks []frameworkInfo `json:"frameworks"`
		Errors     []TemplateError `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Frameworks) != 6 || list.Errors == nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compliance/frameworks/uu-pdp/assessment?period=2026-11", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"Pasal 35"`) {
		t.Fatalf("assessment: %d %.200s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compliance/frameworks/nope/assessment", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown framework: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compliance/frameworks/reports", strings.NewReader(`{"framework":"iso27001"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", rec.Code, rec.Body.String())
	}
}

// A reference marked unverified is never printed, so a guessed article
// number cannot reach an auditor as if it were checked.
func TestUnverifiedReferenceIsNotPrinted(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	if err := os.MkdirAll(g.TemplateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	tpl := `{"id":"uji","name":"Uji","regulation":"Uji","reviewed_at":"2026-11-19","controls":[
		{"ref":"Pasal 77","verified":false,"title":"Kontrol tanpa nomor","evidence":["audit.chain"]},
		{"ref":"Pasal 88","verified":false,"title":"Kontrol di luar cakupan"},
		{"ref":"Pasal 12","title":"Kontrol terverifikasi","evidence":["audit.chain"]}]}`
	if err := os.WriteFile(filepath.Join(g.TemplateDir(), "uji.json"), []byte(tpl), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := g.GenerateFramework(context.Background(), FrameworkRequest{Framework: "uji", ActorType: "user", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pdf, _ := os.ReadFile(g.ReportPath(rep.ID))
	text := pdfText(t, pdf)
	if strings.Contains(text, "Pasal 77") || strings.Contains(text, "Pasal 88") {
		t.Fatal("an unverified reference was printed")
	}
	if !strings.Contains(text, "Pasal 12") || !strings.Contains(text, "Kontrol tanpa nomor") {
		t.Fatal("verified reference or control title missing")
	}
}
