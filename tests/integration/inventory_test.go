package integration

// E2E test for the Fase 2 device-management API: inventory retrieval, retire and
// restore lifecycle, static groups, and on-demand collection requests.
//
// What this proves:
//   - the routes are mounted and protected by the real JWT middleware, not the
//     test no-op
//   - a viewer can read inventory but cannot retire a device or manage groups
//   - retire removes the device from the default list and revokes its secret;
//     restore brings the row back without resurrecting the old credential
//   - group membership is idempotent and appears in the group-filtered device list
//   - an inventory.collect request to an offline device answers 202 queued
//     rather than pretending the command was sent

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/auth"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/rbac"
	devicemgmt "github.com/henokakunemail-stack/Endpoint-Manager/server/modules/device-management"
)

// offlineHub satisfies devicemgmt.hubSender; no device is ever online in these
// tests, so collect requests must answer "queued".
type offlineHub struct{}

func (offlineHub) Online(string) bool         { return false }
func (offlineHub) SendTo(string, []byte) bool { return false }

func newInventoryEnv(t *testing.T) (*httptest.Server, *sqlx.DB) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "inv-e2e.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	jwtSvc := auth.NewJWTService("e2e-inv-secret-0123456789ab", time.Minute, time.Hour)
	repo := devicemgmt.NewRepository(d)
	invRepo := devicemgmt.NewInventoryRepository(d)

	// The hub is offline for every device by construction.
	var hub interface {
		Online(string) bool
		SendTo(string, []byte) bool
	} = offlineHub{}
	invH := devicemgmt.NewInventoryHandler(invRepo, d, hub).
		WithAuth(jwtSvc.RequireAuth)

	r := chi.NewRouter()
	auth.NewLoginHandler(d, jwtSvc).Register(r)
	devicemgmt.NewHandler(repo, d, jwtSvc, 30*time.Minute).Register(r)
	invH.Register(r)

	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts, d
}

// seedInvDevice enrols a device and stores one inventory snapshot for it.
func seedInvDevice(t *testing.T, d *sqlx.DB, hostname string) string {
	t.Helper()
	id := devicemgmt.NewID()
	now := time.Now().UTC()
	_, err := d.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version,
		status, last_seen_at, enrolled_at, device_secret_hash, site, created_at, updated_at, capabilities)
		VALUES (?, ?, 'windows', '11', '0.2.0', 'online', ?, ?, 'secret-hash', 'hq', ?, ?, ?)`,
		id, hostname, now, now, now, now, `["ping","inventory.collect"]`)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}

	// The stored shape is exactly what acceptInventory writes: JSON sections plus
	// cached columns. Seeding it directly proves the API reads the same row the
	// agent path writes.
	_, err = d.Exec(`INSERT INTO device_inventory (id, device_id, hw, software, os_detail,
		hw_ram_bytes, hw_disk_free_pct, hw_cpu_model, collected_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		devicemgmt.NewID(), id,
		`{"cpu":{"name":"Intel i5-1135G7","number_of_cores":4,"logical_processors":8},"ram_total_bytes":16905961472,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":102238302208}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`,
		`[{"name":"7-Zip","version":"23.01"},{"name":"Firefox","version":"128.0"}]`,
		`{"edition":"Pro","build_number":"22631","architecture":"x64"}`,
		16905961472, 20.0, "Intel i5-1135G7", now, now)
	if err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	return id
}

func TestE2EInventoryLifecycle(t *testing.T) {
	ts, d := newInventoryEnv(t)

	adminID := seedUser(t, d, "invadmin", "pass-admin", rbac.RoleAdmin)
	viewerID := seedUser(t, d, "invviewer", "pass-viewer", rbac.RoleViewer)
	t.Logf("seeded admin=%s viewer=%s", adminID, viewerID)

	adminTok := login(t, ts.URL, "invadmin", "pass-admin")
	viewerTok := login(t, ts.URL, "invviewer", "pass-viewer")
	if adminTok == "" || viewerTok == "" {
		t.Fatal("logins must succeed")
	}

	deviceID := seedInvDevice(t, d, "PC-CABANG-01")
	t.Logf("seeded device=%s", deviceID)

	// --- read inventory as viewer ---
	inv := invGet(t, ts.URL+"/api/devices/"+deviceID+"/inventory", viewerTok)
	if got := inv["cpu_model"]; got != "Intel i5-1135G7" {
		t.Fatalf("cpu_model, got %v", got)
	}
	if got, ok := inv["ram_bytes"].(float64); !ok || int64(got) != 16905961472 {
		t.Fatalf("ram_bytes, got %v", inv["ram_bytes"])
	}
	hw, _ := inv["hw"].(map[string]any)
	model, _ := hw["model"].(map[string]any)
	if got := model["serial_number"]; got != "ABCD123" {
		t.Fatalf("serial in hw section, got %v", got)
	}
	sw, _ := inv["software"].([]any)
	if len(sw) != 2 {
		t.Fatalf("software entries, got %d", len(sw))
	}

	// --- the device list must carry the capability array ---
	list := invGet(t, ts.URL+"/api/devices", adminTok)
	devices, _ := list["devices"].([]any)
	if len(devices) != 1 {
		t.Fatalf("device list, got %d entries", len(devices))
	}
	first, _ := devices[0].(map[string]any)
	caps, _ := first["capabilities"].([]any)
	if len(caps) != 2 || caps[0] != "ping" {
		t.Fatalf("capabilities on the device row, got %v", caps)
	}
	if _, present := first["retired_at"]; present {
		t.Fatal("an active device must not expose retired_at")
	}

	// --- viewer CANNOT retire (403) ---
	if code := invPostCode(t, ts.URL+"/api/devices/"+deviceID+"/retire", viewerTok, nil); code != http.StatusForbidden {
		t.Fatalf("viewer retiring: expected 403, got %d", code)
	}

	// --- admin retires ---
	res := invPost(t, ts.URL+"/api/devices/"+deviceID+"/retire", adminTok, nil)
	if res["status"] != "retired" {
		t.Fatalf("retire response, got %v", res)
	}

	// A retired device must be gone from the fleet list and unable to authenticate.
	list = invGet(t, ts.URL+"/api/devices", adminTok)
	if devices, _ = list["devices"].([]any); len(devices) != 0 {
		t.Fatalf("retired device must not appear in the device list, got %d", len(devices))
	}
	var secretHash string
	if err := d.Get(&secretHash, `SELECT device_secret_hash FROM devices WHERE id = ?`, deviceID); err != nil {
		t.Fatalf("read device row: %v", err)
	}
	if secretHash != "" {
		t.Fatal("retire must clear the device secret so the agent cannot reconnect")
	}

	// --- restore ---
	res = invPost(t, ts.URL+"/api/devices/"+deviceID+"/restore", adminTok, nil)
	if res["status"] != "offline" {
		t.Fatalf("restore response, got %v", res)
	}
	var retired *string
	if err := d.Get(&retired, `SELECT retired_at FROM devices WHERE id = ?`, deviceID); err != nil {
		t.Fatalf("read retired_at: %v", err)
	}
	if retired != nil {
		t.Fatal("restore must clear retired_at")
	}
	if err := d.Get(&secretHash, `SELECT device_secret_hash FROM devices WHERE id = ?`, deviceID); err != nil {
		t.Fatalf("read secret after restore: %v", err)
	}
	if secretHash != "" {
		t.Fatal("restore must not resurrect the old secret; re-enrollment is required")
	}

	// --- collect while offline must answer 202, never "sent" ---
	res = invPost(t, ts.URL+"/api/devices/"+deviceID+"/inventory/collect", adminTok, nil)
	if res["status"] != "queued" {
		t.Fatalf("collect on an offline device must be queued, got %v", res)
	}
	if got := res["detail"]; got == nil {
		t.Fatal("queued response should explain when collection will happen")
	}

	// --- collect on an unknown device must be 404, not a queue ---
	if code := invPostCode(t, ts.URL+"/api/devices/does-not-exist/inventory/collect", adminTok, nil); code != http.StatusNotFound {
		t.Fatalf("collect on unknown device: expected 404, got %d", code)
	}

	// --- inventory for a device with no collection yet ---
	emptyID := devicemgmt.NewID()
	_, err := d.Exec(`INSERT INTO devices (id, hostname, os_name, status, enrolled_at, device_secret_hash, created_at, updated_at)
		VALUES (?, 'PC-CABANG-02', 'windows', 'offline', ?, 'h', ?, ?)`,
		emptyID, time.Now().UTC(), time.Now().UTC(), time.Now().UTC())
	if err != nil {
		t.Fatalf("seed second device: %v", err)
	}
	if code := invGetCode(t, ts.URL+"/api/devices/"+emptyID+"/inventory", adminTok); code != http.StatusNotFound {
		t.Fatalf("inventory for an uncollected device: expected 404, got %d", code)
	}
}

func TestE2EGroups(t *testing.T) {
	ts, d := newInventoryEnv(t)

	adminID := seedUser(t, d, "grpadmin", "pass-admin", rbac.RoleAdmin)
	viewerID := seedUser(t, d, "grpviewer", "pass-viewer", rbac.RoleViewer)
	t.Logf("seeded admin=%s viewer=%s", adminID, viewerID)

	adminTok := login(t, ts.URL, "grpadmin", "pass-admin")
	viewerTok := login(t, ts.URL, "grpviewer", "pass-viewer")

	d1 := seedInvDevice(t, d, "PC-HQ-01")
	d2 := seedInvDevice(t, d, "PC-HQ-02")

	// --- viewer cannot create a group ---
	if code := invPostCode(t, ts.URL+"/api/groups", viewerTok, map[string]string{"name": "PatchTuesday"}); code != http.StatusForbidden {
		t.Fatalf("viewer creating group: expected 403, got %d", code)
	}

	// --- admin creates a group ---
	grp := invPost(t, ts.URL+"/api/groups", adminTok, map[string]string{
		"name": "PatchTuesday", "description": "patch group for HQ",
	})
	groupID, _ := grp["id"].(string)
	if groupID == "" {
		t.Fatalf("create group returned no id: %v", grp)
	}

	list := invGet(t, ts.URL+"/api/groups", viewerTok) // viewer CAN read
	groups, _ := list["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("group list, got %d entries", len(groups))
	}
	g0, _ := groups[0].(map[string]any)
	if g0["name"] != "PatchTuesday" {
		t.Fatalf("group name, got %v", g0["name"])
	}
	if g0["member_count"].(float64) != 0 {
		t.Fatalf("fresh group must have 0 members, got %v", g0["member_count"])
	}

	// --- add members; re-adding must be a no-op, not a duplicate ---
	res := invPost(t, ts.URL+"/api/groups/"+groupID+"/members", adminTok,
		map[string][]string{"device_ids": {d1, d2}})
	if res["added"].(float64) != 2 {
		t.Fatalf("added count, got %v", res["added"])
	}
	res = invPost(t, ts.URL+"/api/groups/"+groupID+"/members", adminTok,
		map[string][]string{"device_ids": {d1}})
	if res["added"].(float64) != 0 {
		t.Fatalf("re-adding an existing member must add 0, got %v", res["added"])
	}

	// --- the group-filtered list sees the members and honours pagination ---
	members := invGet(t, ts.URL+"/api/groups/"+groupID+"/devices?limit=1", adminTok)
	if members["count"].(float64) != 1 {
		t.Fatalf("page size 1 must return 1 device, got %v", members["count"])
	}
	if members["total"].(float64) != 2 {
		t.Fatalf("group total must be 2, got %v", members["total"])
	}

	// --- remove one member ---
	res = invDelete(t, ts.URL+"/api/groups/"+groupID+"/members/"+d1, adminTok)
	if res["status"] != "removed" {
		t.Fatalf("remove member, got %v", res)
	}
	members = invGet(t, ts.URL+"/api/groups/"+groupID+"/devices", adminTok)
	if members["total"].(float64) != 1 {
		t.Fatalf("after removal group total must be 1, got %v", members["total"])
	}

	// --- removing a non-member must be 404 ---
	if code := invDeleteCode(t, ts.URL+"/api/groups/"+groupID+"/members/"+d1, adminTok); code != http.StatusNotFound {
		t.Fatalf("removing a non-member: expected 404, got %d", code)
	}

	// --- deleting the group drops its memberships too ---
	res = invDelete(t, ts.URL+"/api/groups/"+groupID, adminTok)
	if res["status"] != "deleted" {
		t.Fatalf("delete group, got %v", res)
	}
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM device_group_members WHERE group_id = ?`, groupID); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if n != 0 {
		t.Fatalf("deleting a group must remove its memberships, got %d", n)
	}
	// The device rows themselves are untouched.
	var devs int
	if err := d.Get(&devs, `SELECT COUNT(*) FROM devices WHERE retired_at IS NULL`); err != nil {
		t.Fatalf("count devices: %v", err)
	}
	if devs != 2 {
		t.Fatalf("group deletion must not touch devices, got %d", devs)
	}

	// --- audit trail must record every mutating call ---
	var actions []string
	if err := d.Select(&actions, `SELECT action FROM audit_logs ORDER BY created_at`); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	want := map[string]bool{
		"group.create":         true,
		"group.members.add":    true,
		"group.members.remove": true,
		"group.delete":         true,
	}
	for _, a := range actions {
		delete(want, a)
	}
	if len(want) > 0 {
		t.Fatalf("audit trail missing actions: %v; saw %v", want, actions)
	}
}

// --- helpers ---

func invGet(t *testing.T, url, token string) map[string]any {
	t.Helper()
	code, body := invDo(t, http.MethodGet, url, token, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d: %s", url, code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("GET %s: bad json %q: %v", url, body, err)
	}
	return out
}

func invGetCode(t *testing.T, url, token string) int {
	t.Helper()
	code, _ := invDo(t, http.MethodGet, url, token, nil)
	return code
}

func invPost(t *testing.T, url, token string, body any) map[string]any {
	t.Helper()
	code, raw := invDo(t, http.MethodPost, url, token, body)
	// 202 Accepted is a success response: the collect request to an offline
	// device is legitimately queued rather than delivered.
	if code != http.StatusOK && code != http.StatusCreated && code != http.StatusAccepted {
		t.Fatalf("POST %s: expected 200/201/202, got %d: %s", url, code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("POST %s: bad json %q: %v", url, raw, err)
	}
	return out
}

func invPostCode(t *testing.T, url, token string, body any) int {
	t.Helper()
	code, _ := invDo(t, http.MethodPost, url, token, body)
	return code
}

func invDelete(t *testing.T, url, token string) map[string]any {
	t.Helper()
	code, raw := invDo(t, http.MethodDelete, url, token, nil)
	if code != http.StatusOK {
		t.Fatalf("DELETE %s: expected 200, got %d: %s", url, code, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("DELETE %s: bad json %q: %v", url, raw, err)
	}
	return out
}

func invDeleteCode(t *testing.T, url, token string) int {
	t.Helper()
	code, _ := invDo(t, http.MethodDelete, url, token, nil)
	return code
}

func invDo(t *testing.T, method, url, token string, body any) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		buf.Write(b)
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	buf.Reset()
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return resp.StatusCode, buf.String()
}
