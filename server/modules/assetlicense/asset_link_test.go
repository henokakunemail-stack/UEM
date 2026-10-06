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

func assetFixture(t *testing.T) (*sqlx.DB, chi.Router) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	r := chi.NewRouter()
	NewHandler(NewRepository(d), nopAuditor{}, func(next http.Handler) http.Handler { return next }).Register(r)
	return d, r
}

func seedDevice(t *testing.T, d *sqlx.DB, id, hostname string) {
	t.Helper()
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO devices (id, hostname, os_name, status, enrolled_at,
		                     device_secret_hash, created_at, updated_at)
		VALUES (?, ?, 'windows', 'online', ?, 'hash', ?, ?)`,
		id, hostname, now, now, now)
	if err != nil {
		t.Fatalf("seed device %s: %v", id, err)
	}
}

func createAsset(t *testing.T, r chi.Router, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/assets", strings.NewReader(body))
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleTechnician))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The console can only show which machine an asset belongs to if the hostname
// comes back from the list. This is the whole reason the query joins.
func TestListAssetsCarriesTheLinkedDeviceHostname(t *testing.T) {
	d, r := assetFixture(t)
	seedDevice(t, d, "dev-1", "FIN-JKT-01")

	rec := createAsset(t, r, `{"asset_tag":"AST-1","model_name":"Latitude 3420","device_id":"dev-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleViewer))
	list := httptest.NewRecorder()
	r.ServeHTTP(list, req)

	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var got []struct {
		DeviceID       string `json:"device_id"`
		DeviceHostname string `json:"device_hostname"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, list.Body.String())
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1", len(got))
	}
	if got[0].DeviceHostname != "FIN-JKT-01" {
		t.Errorf("device_hostname = %q, want FIN-JKT-01", got[0].DeviceHostname)
	}
}

// device_id is ON DELETE SET NULL, so a retired device leaves the asset behind
// with nothing to join to. An inner join would drop exactly the assets an
// operator most wants to find — the ones whose machine is gone.
func TestAnAssetOutlivesTheDeviceItWasLinkedTo(t *testing.T) {
	d, r := assetFixture(t)
	seedDevice(t, d, "dev-1", "FIN-JKT-01")

	if rec := createAsset(t, r, `{"asset_tag":"AST-1","model_name":"Latitude 3420","device_id":"dev-1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := d.Exec(`DELETE FROM devices WHERE id = 'dev-1'`); err != nil {
		t.Fatalf("delete device: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleViewer))
	list := httptest.NewRecorder()
	r.ServeHTTP(list, req)

	var got []struct {
		AssetTag       string `json:"asset_tag"`
		DeviceHostname string `json:"device_hostname"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d assets, want 1 — the LEFT JOIN dropped a row", len(got))
	}
	if got[0].DeviceHostname != "" {
		t.Errorf("device_hostname = %q, want empty for a deleted device", got[0].DeviceHostname)
	}
}

// Without the existence check the foreign key does the rejecting, and reports
// itself as SQLite's error text on a 500. An operator reads that as a server
// fault rather than as the wrong device id that it is.
func TestAnUnknownDeviceIsRefusedWithAMessageNotAForeignKeyError(t *testing.T) {
	_, r := assetFixture(t)
	badRef := `{"asset_tag":"AST-1","model_name":"M","device_id":"nope"}`

	rec := createAsset(t, r, badRef)
	assertRefused(t, "create", rec)

	// The id is server-generated, so the seed asset has to be read back rather
	// than guessed — otherwise this only proves a 404 for a missing row.
	seed := createAsset(t, r, `{"asset_tag":"AST-2","model_name":"M"}`)
	if seed.Code != http.StatusCreated {
		t.Fatalf("seed create: %d %s", seed.Code, seed.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(seed.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode seed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/assets/"+created.ID, strings.NewReader(badRef))
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleTechnician))
	upd := httptest.NewRecorder()
	r.ServeHTTP(upd, req)
	assertRefused(t, "update", upd)
}

func assertRefused(t *testing.T, name string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("%s: status %d, want 400: %s", name, rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "foreign key") {
		t.Errorf("%s leaked the raw constraint error: %s", name, body)
	}
	if !strings.Contains(body, "linked device does not exist") {
		t.Errorf("%s: response = %s, want the readable reason", name, body)
	}
}

// One machine legitimately owns several assets — a laptop, a dock, a monitor —
// so a second link is a normal state, not a conflict. Refusing it would be
// wrong about the domain, which is why device_id is a plain index.
func TestOneDeviceCanCarrySeveralAssets(t *testing.T) {
	d, r := assetFixture(t)
	seedDevice(t, d, "dev-1", "FIN-JKT-01")

	for _, tag := range []string{"AST-LAPTOP", "AST-DOCK"} {
		rec := createAsset(t, r, `{"asset_tag":"`+tag+`","model_name":"M","device_id":"dev-1"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", tag, rec.Code, rec.Body.String())
		}
	}

	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM hardware_assets WHERE device_id = 'dev-1'`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("assets on dev-1 = %d, want 2", n)
	}
}

// A duplicate tag is an ordinary operator mistake, and the console needs to be
// able to say so. Before this was checked, SQLite's own message came back with
// a 500: "create asset: constraint failed: UNIQUE constraint failed:
// hardware_assets.asset_tag (2067)" -- the wrong status, and a string that
// tells the operator nothing about which field to change.
func TestADuplicateAssetTagIsRefusedWithAMessageNotAConstraintError(t *testing.T) {
	_, r := assetFixture(t)

	if rec := createAsset(t, r, `{"asset_tag":"AST-1","model_name":"M"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}

	rec := createAsset(t, r, `{"asset_tag":"AST-1","model_name":"Other"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate tag answered %d, want 400: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "asset_tag is already in use") {
		t.Errorf("duplicate tag response does not name the field: %s", body)
	}
	for _, leak := range []string{"UNIQUE", "constraint failed", "2067"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaks the driver error %q: %s", leak, body)
		}
	}
}

// The duplicate check must not fire on an asset keeping its own tag, which is
// what every edit that does not rename does.
func TestAnAssetKeepingItsOwnTagIsNotAConflict(t *testing.T) {
	_, r := assetFixture(t)

	rec := createAsset(t, r, `{"asset_tag":"AST-1","model_name":"M"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created HardwareAsset
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	body := `{"asset_tag":"AST-1","model_name":"Renamed Model","vendor":"V"}`
	req := httptest.NewRequest(http.MethodPut, "/api/assets/"+created.ID, strings.NewReader(body))
	req = req.WithContext(rbac.WithRole(req.Context(), rbac.RoleTechnician))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", created.ID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("edit keeping its own tag answered %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// And a rename ONTO another asset's tag is still refused.
	rec2 := createAsset(t, r, `{"asset_tag":"AST-2","model_name":"M"}`)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second create: %d %s", rec2.Code, rec2.Body.String())
	}

	req3 := httptest.NewRequest(http.MethodPut, "/api/assets/"+created.ID, strings.NewReader(`{"asset_tag":"AST-2","model_name":"M"}`))
	req3 = req3.WithContext(rbac.WithRole(req3.Context(), rbac.RoleTechnician))
	route3 := chi.NewRouteContext()
	route3.URLParams.Add("id", created.ID)
	req3 = req3.WithContext(context.WithValue(req3.Context(), chi.RouteCtxKey, route3))
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("rename onto a taken tag answered %d, want 400: %s", rec3.Code, rec3.Body.String())
	}
}
