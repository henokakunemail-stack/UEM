package alerting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// stubAudit records that the handler was allowed to reach the audit call at
// all. updateRule cannot report "rule not found" and then audit a change, so a
// growing count is the cheap proof the read succeeded.
type stubAudit struct{ calls int }

func (s *stubAudit) Log(context.Context, string, string, string, string, map[string]string) error {
	s.calls++
	return nil
}

func newRuleFixture(t *testing.T) (*Handler, *sqlx.DB) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "rules.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	repo := NewRepository(database)
	return NewHandler(repo, NewEvaluator(database, repo), &stubAudit{}, nil), database
}

// A partial PUT must only touch the fields the client sent. updateRule decoded
// into a plain value struct, so the omitted fields arrived as their zero values
// and were assigned onto the row: rule_type became "", which the evaluator's
// switch does not have a case for. The rule then stays enabled, is still
// counted in RulesEvaluated, and never matches anything -- a disk-low alert
// dies silently while the console shows it as configured.
func TestUpdateRuleKeepsTheFieldsTheClientDidNotSend(t *testing.T) {
	h, database := newRuleFixture(t)

	rule := &AlertRule{
		Name:         "disk-low",
		RuleType:     "disk_low",
		ThresholdVal: 15,
		Severity:     "critical",
		WebhookURL:   "https://hooks.example.com/disk",
		IsEnabled:    true,
		CreatedBy:    "u1",
	}
	if err := h.repo.CreateRule(context.Background(), rule); err != nil {
		t.Fatal(err)
	}

	// Two fields sent, four deliberately omitted.
	req := httptest.NewRequest(http.MethodPut, "/api/alerts/rules/"+rule.ID,
		strings.NewReader(`{"name":"renamed","is_enabled":false}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", rule.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec := httptest.NewRecorder()
	h.updateRule(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("partial update answered %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got AlertRule
	if err := database.Get(&got, `
		SELECT name, rule_type, threshold_val, severity, webhook_url, is_enabled
		FROM alert_rules WHERE id = ?`, rule.ID); err != nil {
		t.Fatal(err)
	}

	if got.Name != "renamed" || got.IsEnabled {
		t.Fatalf("sent fields did not apply: name=%q is_enabled=%v", got.Name, got.IsEnabled)
	}

	// These four are what the evaluator switch and the webhook dispatch read.
	// Any of them being zeroed is a rule that no longer fires.
	if got.RuleType != "disk_low" {
		t.Errorf("rule_type was blanked to %q: the evaluator's switch has no case for it", got.RuleType)
	}
	if got.ThresholdVal != 15 {
		t.Errorf("threshold_val was blanked to %v, want 15", got.ThresholdVal)
	}
	if got.Severity != "critical" {
		t.Errorf("severity was blanked to %q, want critical", got.Severity)
	}
	if got.WebhookURL != "https://hooks.example.com/disk" {
		t.Errorf("webhook_url was blanked to %q, incidents would no longer be dispatched", got.WebhookURL)
	}
}
