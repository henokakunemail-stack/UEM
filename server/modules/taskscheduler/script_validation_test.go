package taskscheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type dummyAuditor struct{}

func (d *dummyAuditor) Log(ctx context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	return nil
}

func TestScriptTypeAllowlist(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo := NewRepository(d)
	passAuth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), auth.CtxUserID, "u-admin")
			ctx = rbac.WithRole(ctx, rbac.RoleAdmin)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	h := NewHandler(repo, nil, &dummyAuditor{}, passAuth, nil)
	router := chi.NewRouter()
	h.Register(router)

	// Test 1: Invalid script_type (e.g., 'python' or arbitrary executable)
	badBody, _ := json.Marshal(map[string]any{
		"name":           "Malicious Script",
		"script_type":    "python",
		"script_content": "import os; os.system('calc')",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/scripts", bytes.NewReader(badBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for python script_type, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid script_type") {
		t.Fatalf("expected error message to mention invalid script_type, got: %s", w.Body.String())
	}

	// Test 2: Valid script_type ('powershell')
	goodBody, _ := json.Marshal(map[string]any{
		"name":           "Good Script",
		"script_type":    "powershell",
		"script_content": "Get-Process",
	})
	req2 := httptest.NewRequest(http.MethodPost, "/api/scripts", bytes.NewReader(goodBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for powershell script_type, got %d: %s", w2.Code, w2.Body.String())
	}
}
