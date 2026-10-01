package taskscheduler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

func scheduleFixture(t *testing.T) (*sqlx.DB, chi.Router) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	repo := NewRepository(d)
	// The scheduler and the device lookup are not on the path this test
	// exercises, and the RBAC layer is left off so the 400 comes from the
	// schedule_type check rather than from a missing admin claim.
	h := NewHandler(repo, NewScheduler(repo, stubHub{}), nopAuditor{}, passthroughAuth, nil)

	r := chi.NewRouter()
	h.Register(r)
	return d, r
}

func passthroughAuth(next http.Handler) http.Handler { return next }

func TestAScheduleTypeTheSchedulerDoesNotRunIsRefused(t *testing.T) {
	_, r := scheduleFixture(t)

	for _, scheduleType := range []string{"cron", "once", "INTERVAL", "daily"} {
		t.Run("type="+scheduleType, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"name":          "nightly",
				"script_id":     "no-such-script",
				"target_type":   "device",
				"target_id":     "dev-1",
				"schedule_type": scheduleType,
				"schedule_expr": "60",
				"is_enabled":    true,
			})
			req := httptest.NewRequest(http.MethodPost, "/api/schedules", strings.NewReader(string(body)))
			ctx := context.WithValue(req.Context(), auth.CtxUserID, "admin-1")
			req = req.WithContext(rbac.WithRole(ctx, rbac.RoleAdmin))

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			// The script does not exist either, so this only proves the
			// schedule_type check runs first. That ordering is deliberate: the
			// operator is told what is wrong with their schedule rather than
			// sent to look up a script id that may be fine.
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("schedule_type %q returned %d, want 400", scheduleType, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "schedule_type") {
				t.Errorf("schedule_type %q: response does not name the field: %s", scheduleType, rec.Body.String())
			}
		})
	}
}

// The one type the scheduler does run still has to get through, otherwise the
// check above has simply disabled the feature.
func TestTheTypeTheSchedulerRunsIsAccepted(t *testing.T) {
	if !isRunnableScheduleType("interval") {
		t.Error("interval is rejected; the scheduler fires only this type, so nothing would ever run")
	}
}
