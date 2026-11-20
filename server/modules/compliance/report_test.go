package compliance

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/protocol"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/audit"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type dbAuditor struct{ db *sqlx.DB }

func (a dbAuditor) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	return audit.Log(ctx, a.db, actorType, actorID, action, targetID, details)
}

func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func seedDevice(t *testing.T, d *sqlx.DB, id, site string, posture *protocol.Posture, scanned bool) {
	t.Helper()
	now := time.Now().UTC()
	var scan any
	if scanned {
		scan = now.Add(-time.Hour)
	}
	if _, err := d.Exec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash,
		site, last_patch_scan_at, created_at, updated_at) VALUES (?, ?, 'windows', 'online', ?, 'h', ?, ?, ?, ?)`,
		id, id, now, site, scan, now, now); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	if posture == nil {
		return
	}
	raw, _ := json.Marshal(posture)
	if _, err := d.Exec(`INSERT INTO device_posture (device_id, posture_json, disk_encryption, antivirus, firewall,
		password_policy, screen_lock, collected_at, updated_at) VALUES (?, ?, 'x', 'x', 'x', 'x', 'x', ?, ?)`,
		id, string(raw), now.Add(-time.Hour), now); err != nil {
		t.Fatalf("seed posture: %v", err)
	}
}

// seedFleet is the plan's three-device acceptance fleet.
func seedFleet(t *testing.T, d *sqlx.DB) {
	seedDevice(t, d, "PC-OK", "Pusat", goodPosture(), true)
	off := goodPosture()
	off.DiskEncryption.Volumes[0].Protected = false
	off.DiskEncryption.Volumes[0].Status = ""
	seedDevice(t, d, "PC-BITLOCKER-OFF", "Pusat", off, true)
	seedDevice(t, d, "PC-OLD-AGENT", "Cabang", nil, true)
}

func newGen(t *testing.T, d *sqlx.DB) *Generator {
	t.Helper()
	repo := NewRepository(d)
	if err := repo.EnsureDefaultProfile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &Generator{Repo: repo, Audit: dbAuditor{d}, DataDir: t.TempDir(), Organization: "PT Uji Coba"}
}

// pdfText inflates every compressed stream so tests can search the text the
// PDF draws.
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
	return out.String()
}

func TestGenerateArchivesAuditsAndPrintsHash(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	ctx := context.Background()

	rep, err := g.Generate(ctx, Request{ActorType: "user", ActorID: "u1", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Compliant != 1 || rep.Summary.NonCompliant != 1 || rep.Summary.Unknown != 1 || *rep.Summary.Score != 50 {
		t.Fatalf("summary %+v", rep.Summary)
	}

	pdf, err := os.ReadFile(g.ReportPath(rep.ID))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pdf)
	if hex.EncodeToString(sum[:]) != rep.FileSHA256 {
		t.Fatal("archived hash is not the hash of the stored file")
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}

	text := pdfText(t, pdf)
	for _, want := range []string{rep.ContentSHA256, "Laporan Kepatuhan Perangkat", "PT Uji Coba",
		"BitLocker off pada C:", "Skor kepatuhan armada: 50%", "PC-OLD-AGENT", "Verifikasi keaslian"} {
		if !strings.Contains(text, want) {
			t.Errorf("PDF text does not contain %q", want)
		}
	}

	// The audit log carries both hashes, on an entry in the hash chain.
	var details string
	if err := d.Get(&details, `SELECT details FROM audit_logs WHERE action = ? AND target_id = ?`, AuditAction, rep.ID); err != nil {
		t.Fatalf("audit entry: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal([]byte(details), &m)
	if m["file_sha256"] != rep.FileSHA256 || m["content_sha256"] != rep.ContentSHA256 {
		t.Fatalf("audit details %v", m)
	}
	chain, err := audit.VerifyChain(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if !chain.OK {
		t.Fatalf("audit chain broken: %+v", chain)
	}

	// The second report for the same profile shows the trend from the first.
	rep2, err := g.Generate(ctx, Request{ActorType: "user", ActorID: "u1", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	pdf2, _ := os.ReadFile(g.ReportPath(rep2.ID))
	if txt := pdfText(t, pdf2); !strings.Contains(txt, "Dibanding laporan sebelumnya") || !strings.Contains(txt, rep.ID) {
		i := strings.Index(txt, "Dibanding")
		t.Fatalf("second report does not compare with the first: %q", txt[i:min(len(txt), i+300)])
	}
}

func serve(g *Generator) http.Handler {
	r := chi.NewRouter()
	// Stands in for the auth middleware: an admin, so every route is reachable.
	NewHandler(g, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(rbac.WithRole(req.Context(), rbac.RoleAdmin)))
		})
	}).Register(r)
	return r
}

func verify(t *testing.T, h http.Handler, sum string) (bool, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"sha256": sum})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compliance/reports/verify", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Match  bool    `json:"match"`
		Report *Report `json:"report"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Report != nil {
		return resp.Match, resp.Report.ID
	}
	return resp.Match, ""
}

// A downloaded PDF verifies against the archive; the same file with one byte
// changed does not.
func TestDownloadAndVerify(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	rep, err := g.Generate(context.Background(), Request{ActorType: "user", ActorName: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	h := serve(g)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compliance/reports/"+rep.ID+"/pdf", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("download: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("X-Report-SHA256") != rep.FileSHA256 {
		t.Fatal("download does not carry the archived hash")
	}
	got := rec.Body.Bytes()
	sum := sha256.Sum256(got)
	if ok, id := verify(t, h, hex.EncodeToString(sum[:])); !ok || id != rep.ID {
		t.Fatalf("original file did not verify (match=%v id=%q)", ok, id)
	}
	// Upper-case hex, as certutil prints it, verifies too.
	if ok, _ := verify(t, h, strings.ToUpper(hex.EncodeToString(sum[:]))); !ok {
		t.Fatal("upper-case hash did not verify")
	}

	tampered := append([]byte(nil), got...)
	tampered[len(tampered)/2] ^= 0x01
	sum = sha256.Sum256(tampered)
	if ok, _ := verify(t, h, hex.EncodeToString(sum[:])); ok {
		t.Fatal("a file with one byte changed verified")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/compliance/reports/verify", strings.NewReader(`{"sha256":"abc"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed hash: %d", rec.Code)
	}
}

// An empty archive is [], not null: the console reads .length on it.
func TestEmptyArchiveIsAnEmptyList(t *testing.T) {
	g := newGen(t, newTestDB(t))
	rec := httptest.NewRecorder()
	serve(g).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/compliance/reports", nil))
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("empty archive = %s", got)
	}
}

func TestEvaluationEndpointAndScope(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	ctx := context.Background()
	p, err := g.Repo.CreateProfile(ctx, Profile{Name: "Cabang saja", Rules: DefaultRules(), ScopeSite: "Cabang"})
	if err != nil {
		t.Fatal(err)
	}
	h := serve(g)

	get := func(url string) evaluationResponse {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		var resp evaluationResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}
	all := get("/api/compliance/evaluation")
	if all.Summary.Devices != 3 || !all.Profile.IsDefault {
		t.Fatalf("default profile: %+v", all.Summary)
	}
	branch := get("/api/compliance/evaluation?profile_id=" + p.ID)
	if branch.Summary.Devices != 1 || branch.Devices[0].Device.ID != "PC-OLD-AGENT" || branch.Summary.Score != nil {
		t.Fatalf("site scope: %+v", branch.Summary)
	}
}

func TestRetiredDevicesAreLeftOut(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	if _, err := d.Exec(`UPDATE devices SET retired_at = ? WHERE id = 'PC-BITLOCKER-OFF'`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	g := newGen(t, d)
	p, _ := g.Repo.GetProfile(context.Background(), "")
	res, err := g.Repo.Evaluate(context.Background(), p, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("retired device evaluated: %d results", len(res))
	}
}

func TestMissingPatchFilter(t *testing.T) {
	d := newTestDB(t)
	seedDevice(t, d, "PC-1", "", goodPosture(), true)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	for _, p := range []struct{ id, sev, cat, state string }{
		{"sec", "important", "security", "missing"},
		{"crit-upd", "critical", "updates", "missing"},
		{"driver", "unspecified", "driver", "missing"},
		{"defs", "unspecified", "definition", "missing"},
		{"done", "critical", "security", "installed"},
	} {
		if _, err := d.Exec(`INSERT INTO device_patches (id, device_id, patch_id, title, severity, category,
			installed_state, discovered_at, updated_at) VALUES (?, 'PC-1', ?, ?, ?, ?, ?, ?, ?)`,
			p.id, p.id, p.id, p.sev, p.cat, p.state, old, old); err != nil {
			t.Fatal(err)
		}
	}
	g := newGen(t, d)
	p, _ := g.Repo.GetProfile(context.Background(), "")
	facts, err := g.Repo.LoadFacts(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range facts[0].MissingSecurityPatches {
		ids = append(ids, m.PatchID)
	}
	if strings.Join(ids, ",") != "sec,crit-upd" && strings.Join(ids, ",") != "crit-upd,sec" {
		t.Fatalf("missing security patches = %v", ids)
	}
}

func TestMonthlyScheduleIsIdempotent(t *testing.T) {
	d := newTestDB(t)
	seedFleet(t, d)
	g := newGen(t, d)
	ctx := context.Background()
	// The default profile was created just now, mid-month: last month's
	// report is not its to make.
	g.Now = func() time.Time { return time.Now().UTC().AddDate(0, 0, 1) }
	if n, err := g.RunMonthly(ctx); err != nil || n != 0 {
		t.Fatalf("a profile created this month published last month's report (%d, %v)", n, err)
	}
	if _, err := d.Exec(`UPDATE compliance_profiles SET created_at = ?`, time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	g.Now = func() time.Time { return time.Date(2026, 12, 1, 2, 0, 0, 0, time.UTC) }
	if n, err := g.RunMonthly(ctx); err != nil || n != 1 {
		t.Fatalf("first run: %d %v", n, err)
	}
	if n, err := g.RunMonthly(ctx); err != nil || n != 0 {
		t.Fatalf("second run made %d more (err %v)", n, err)
	}
	reps, _ := g.Repo.ListReports(ctx, 10)
	if len(reps) != 1 || reps[0].Period != "2026-11" || reps[0].Kind != KindScheduled {
		t.Fatalf("reports %+v", reps)
	}
}

func TestProfileRules(t *testing.T) {
	d := newTestDB(t)
	g := newGen(t, d)
	ctx := context.Background()
	def, err := g.Repo.GetProfile(ctx, "")
	if err != nil || !def.IsDefault {
		t.Fatalf("default profile: %+v %v", def, err)
	}
	if err := g.Repo.DeleteProfile(ctx, def.ID); err != ErrDefaultProfile {
		t.Fatalf("deleting the default: %v", err)
	}
	def.IsDefault = false
	if _, err := g.Repo.UpdateProfile(ctx, def); err == nil {
		t.Fatal("unsetting the only default was accepted")
	}
	other, err := g.Repo.CreateProfile(ctx, Profile{Name: "Ketat", Rules: DefaultRules(), IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := g.Repo.ListProfiles(ctx)
	defaults := 0
	for _, p := range ps {
		if p.IsDefault {
			defaults++
			if p.ID != other.ID {
				t.Fatal("the new default did not replace the old one")
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("%d defaults", defaults)
	}
	bad := DefaultRules()
	bad.MaxScreenLockMinutes = -5
	if _, err := g.Repo.CreateProfile(ctx, Profile{Name: "Rusak", Rules: bad}); err == nil {
		t.Fatal("invalid rules accepted")
	}
}
