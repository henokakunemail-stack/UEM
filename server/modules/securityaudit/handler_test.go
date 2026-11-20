package securityaudit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

type recordingAuditor struct {
	action  string
	details map[string]string
}

func (a *recordingAuditor) Log(_ context.Context, _, _, action, _ string, details map[string]string) error {
	a.action, a.details = action, details
	return nil
}

// The audit entry for a run used to carry string(rune(score)): a score of 85
// was logged as "U" and 31 passed checks as an unprintable control
// character. The entry must hold the same numbers the run returned.
func TestRunLogsScoreAndPassedAsNumbers(t *testing.T) {
	aud := &recordingAuditor{}
	h := NewHandler(evalCfg(t), credDB(t), aud, nil)

	rec := httptest.NewRecorder()
	h.runSecurityAudit(rec, httptest.NewRequest(http.MethodPost, "/api/system/security-audit/run", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("run: %d", rec.Code)
	}
	var report AuditReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if aud.action != "security.audit_run" {
		t.Fatalf("no audit entry written (action %q)", aud.action)
	}
	if got, want := aud.details["score"], strconv.Itoa(report.Score); got != want {
		t.Errorf("score logged as %q, run returned %s", got, want)
	}
	if got, want := aud.details["passed"], strconv.Itoa(report.Passed); got != want {
		t.Errorf("passed logged as %q, run returned %s", got, want)
	}
	if aud.details["grade"] != report.Grade {
		t.Errorf("grade logged as %q, run returned %q", aud.details["grade"], report.Grade)
	}
}
