package reports

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

func auditExport(t *testing.T, rows, limit int) *httptest.ResponseRecorder {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tx := d.MustBegin()
	now := time.Now().UTC()
	for i := 0; i < rows; i++ {
		tx.MustExec(`INSERT INTO audit_logs (id, actor_type, actor_id, action, created_at, seq) VALUES (?, 'user', 'u', 'x', ?, ?)`,
			"a"+strconv.Itoa(i), now.Add(time.Duration(i)*time.Millisecond), i)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h := &Handler{repo: NewRepository(d)}
	rec := httptest.NewRecorder()
	h.exportAudit(rec, httptest.NewRequest(http.MethodGet, "/api/reports/audit?format=json&limit="+strconv.Itoa(limit), nil))
	return rec
}

// A report that reached its cap looked complete. It now says so.
func TestAnExportThatHitsItsCapSaysSo(t *testing.T) {
	rec := auditExport(t, 20, 10)
	if rec.Header().Get("X-Truncated") != "true" || rec.Header().Get("X-Row-Count") != "10" {
		t.Fatalf("headers = %v, want X-Truncated true and X-Row-Count 10", rec.Header())
	}
	rec = auditExport(t, 5, 10)
	if rec.Header().Get("X-Truncated") != "" {
		t.Fatal("a complete export claimed to be truncated")
	}
}

// Asking for more than the maximum gave 1000, not the maximum.
func TestAnAuditLimitAboveTheMaximumGivesTheMaximum(t *testing.T) {
	rec := auditExport(t, 1200, 99999)
	if got := rec.Header().Get("X-Row-Count"); got != "1200" {
		t.Fatalf("rows = %s, want 1200", got)
	}
}
