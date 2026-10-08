package rbac

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequirePermission(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		permission string
		userRole   string
		wantCode   int
	}{
		{"viewer reads devices", PermDevicesRead, RoleViewer, http.StatusOK},
		{"viewer manages devices", PermDevicesManage, RoleViewer, http.StatusForbidden},
		{"technician executes commands", PermCommandsExecute, RoleTechnician, http.StatusOK},
		{"viewer executes commands", PermCommandsExecute, RoleViewer, http.StatusForbidden},
		{"admin manages users", PermUserManage, RoleAdmin, http.StatusOK},
		{"technician manages users", PermUserManage, RoleTechnician, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mw := RequirePermission(tt.permission)(handler)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(WithRole(req.Context(), tt.userRole))
			rec := httptest.NewRecorder()

			mw.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Errorf("got HTTP %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}

func TestUnknownPermissionPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Errorf("expected panic for unknown permission")
		}
	}()
	_ = RequirePermission("nonexistent.permission")
}
