package assetlicense

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
)

type nopAuditor struct{}

func (nopAuditor) Log(context.Context, string, string, string, string, map[string]string) error {
	return nil
}

func licenseFixture(t *testing.T) (*sqlx.DB, chi.Router) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	repo := NewRepository(d)
	h := NewHandler(repo, nopAuditor{}, func(next http.Handler) http.Handler { return next })

	r := chi.NewRouter()
	h.Register(r)
	return d, r
}

const secretKey = "AAAA-BBBB-CCCC-DDDD-EEEE"

// A license key is a redeemable credential: it activates a seat at the vendor.
// Both read routes are RoleViewer and both serialise SoftwareLicense as-is, so
// without this a viewer could collect every key in the estate and try them
// against the vendor's activation portal. The console never displays it and
// never sends it on create, so nothing in the UI is losing anything.
func TestAViewerDoesNotReceiveTheLicenseKey(t *testing.T) {
	d, r := licenseFixture(t)

	now := time.Now().UTC()
	if _, err := d.Exec(`INSERT INTO software_licenses
		(id, software_name, publisher, license_key, license_type, total_seats, cost, created_at, updated_at)
		VALUES ('lic-1', 'Design Suite', 'Vendor Co', ?, 'subscription', 10, 999, ?, ?)`,
		secretKey, now, now); err != nil {
		t.Fatalf("seed license: %v", err)
	}

	for _, path := range []string{"/api/licenses", "/api/licenses/lic-1"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleViewer))

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), secretKey) {
				t.Fatalf("%s leaked the license key to a viewer", path)
			}
			// The rest of the row still has to be there. A redaction that
			// dropped the whole license would pass the check above.
			if !strings.Contains(rec.Body.String(), "Design Suite") {
				t.Errorf("%s lost the license itself: %s", path, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"license_key":""`) {
				t.Errorf("%s did not blank the field, it omitted it: %s", path, rec.Body.String())
			}
		})
	}
}

// A technician or an admin manages the estate and has to be able to copy a key
// out of the console. Redacting it from everyone would trade one problem for a
// worse one.
func TestAnAdminStillReceivesTheLicenseKey(t *testing.T) {
	d, r := licenseFixture(t)

	now := time.Now().UTC()
	if _, err := d.Exec(`INSERT INTO software_licenses
		(id, software_name, publisher, license_key, license_type, total_seats, cost, created_at, updated_at)
		VALUES ('lic-1', 'Design Suite', 'Vendor Co', ?, 'subscription', 10, 999, ?, ?)`,
		secretKey, now, now); err != nil {
		t.Fatalf("seed license: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/licenses/lic-1", nil)
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleAdmin))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), secretKey) {
		t.Fatalf("an admin did not receive the license key: %s", rec.Body.String())
	}

	var got struct {
		LicenseKey string `json:"license_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.LicenseKey != secretKey {
		t.Fatalf("license_key = %q, want %q", got.LicenseKey, secretKey)
	}
}

// RoleFromContext must read back exactly what WithRole stored and what
// RequireRole gates on, or the redaction above is deciding on a different role
// than the one the request was admitted for.
func TestRoleFromContextReadsBackWhatRequireRoleGatesOn(t *testing.T) {
	for _, role := range []string{rbac.RoleViewer, rbac.RoleTechnician, rbac.RoleAdmin} {
		if got := rbac.RoleFromContext(rbac.WithRole(context.Background(), role)); got != role {
			t.Errorf("RoleFromContext = %q, want %q", got, role)
		}
	}
	if got := rbac.RoleFromContext(context.Background()); got != "" {
		t.Errorf("RoleFromContext with no role = %q, want empty", got)
	}
}
