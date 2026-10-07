package devicemanagement

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/henokakunemail-stack/Endpoint-Manager/server/core/db"
)

// newTestDB returns a migrated SQLite DB in a temp dir, for inventory tests.
// Each test gets its own isolated database — no shared state, no cleanup of a
// production file.
func newTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "inv.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// recordingAudit captures audit calls so a test can assert what would have been
// written to the audit trail without a database round trip.
type recordingAudit struct {
	calls []recordedAudit
}

type recordedAudit struct {
	actorType, actorID, action, targetID string
	details                              map[string]string
}

func (a *recordingAudit) Log(_ context.Context, actorType, actorID, action, targetID string, details map[string]string) error {
	a.calls = append(a.calls, recordedAudit{actorType, actorID, action, targetID, details})
	return nil
}

// offlineHub is the hubSender stub for tests: no device is ever online.
type offlineHub struct{}

func (offlineHub) Online(string) bool         { return false }
func (offlineHub) SendTo(string, []byte) bool { return false }
func (offlineHub) Disconnect(string)          {}

// TestDiffInventoryNoChange checks that an identical snapshot produces no
// audit-worthy change: the common case, which must stay out of the audit trail.
func TestDiffInventoryNoChange(t *testing.T) {
	hw := `{"cpu":{"name":"Intel i5-1135G7","number_of_cores":4,"logical_processors":8},"ram_total_bytes":16905961472,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":311191511040}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`
	osd := `{"edition":"Pro","build_number":"22631","architecture":"x64"}`

	if d := diffInventory(hw, hw, osd, osd); d.auditWorthy() {
		t.Fatalf("identical snapshot should not be audit-worthy, got %+v", d)
	}
}

// TestDiffInventoryHardwareChange covers the case that matters: a machine that
// was re-provisioned or had parts swapped between collections.
func TestDiffInventoryHardwareChange(t *testing.T) {
	old := `{"cpu":{"name":"Intel i5-1135G7","number_of_cores":4,"logical_processors":8},"ram_total_bytes":16905961472,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":311191511040}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`
	ramBumped := `{"cpu":{"name":"Intel i5-1135G7","number_of_cores":4,"logical_processors":8},"ram_total_bytes":33811922944,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":311191511040}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`

	d := diffInventory(old, ramBumped, `{"build_number":"22631"}`, `{"build_number":"22631"}`)
	if !d.RAMChanged || !d.auditWorthy() {
		t.Fatalf("memory upgrade must be audit-worthy, got %+v", d)
	}
	if d.CPUChanged || d.DiskCountChanged || d.ModelChanged || d.OSBuildChanged {
		t.Fatalf("only memory should have changed, got %+v", d)
	}
	if s := d.summary(softwareDiff{}); s != "changed: memory" {
		t.Fatalf("summary, got %q", s)
	}
}

// TestDiffInventoryModelChanged is the theft / re-provisioning signal.
func TestDiffInventoryModelChanged(t *testing.T) {
	old := `{"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`
	next := `{"model":{"vendor":"Dell Inc.","product":"Latitude 5420","serial_number":"WXYZ987"}}`

	if d := diffInventory(old, next, "", ""); !d.ModelChanged {
		t.Fatal("different serial + product must count as a model change")
	}
}

// TestDiffInventoryNilVsEmptyModel guards the asymmetry that would otherwise spam
// the audit log: an agent that reports no model must not diff against an agent
// that reports an empty model.
func TestDiffInventoryNilVsEmptyModel(t *testing.T) {
	nilModel := `{"cpu":{"name":"i5"},"ram_total_bytes":1024}`
	emptyModel := `{"cpu":{"name":"i5"},"ram_total_bytes":1024,"model":{"vendor":"","product":"","serial_number":""}}`

	if d := diffInventory(nilModel, emptyModel, "", ""); d.auditWorthy() {
		t.Fatalf("nil model must not diff against empty model, got %+v", d)
	}
}

// TestDiffInventoryDiskCountChanged catches a volume added or removed — for a
// VM that is a resize, for a physical machine a disk went missing.
func TestDiffInventoryDiskCountChanged(t *testing.T) {
	old := `{"disks":[{"name":"C:"},{"name":"D:"}]}`
	next := `{"disks":[{"name":"C:"}]}`

	if d := diffInventory(old, next, "", ""); !d.DiskCountChanged {
		t.Fatal("losing a disk must be audit-worthy")
	}
}

// TestDiffInventoryFreeSpaceDrift is the case that must NOT reach the audit
// trail: free space moves constantly, and it is already surfaced as a cached
// column on the dashboard.
func TestDiffInventoryFreeSpaceDrift(t *testing.T) {
	old := `{"disks":[{"name":"C:","total_bytes":1000,"free_bytes":900}]}`
	next := `{"disks":[{"name":"C:","total_bytes":1000,"free_bytes":100}]}`

	if d := diffInventory(old, next, "", ""); d.auditWorthy() {
		t.Fatal("free-space-only change must not be audit-worthy")
	}
}

// TestDiffInventoryOSBuild covers an in-place feature update.
func TestDiffInventoryOSBuild(t *testing.T) {
	if d := diffInventory(`{}`, `{}`, `{"build_number":"22621"}`, `{"build_number":"22631"}`); !d.OSBuildChanged {
		t.Fatal("OS build change must be audit-worthy")
	}
}

// TestDiffSoftwareAddRemove checks that installs and uninstalls are detected but
// not treated as hardware re-provisioning.
func TestDiffSoftwareAddRemove(t *testing.T) {
	old := `[{"name":"Firefox","version":"115.0"},{"name":"7-Zip","version":"23.01"}]`
	next := `[{"name":"Firefox","version":"128.0"},{"name":"VLC","version":"3.0.20"}]`

	d := diffSoftware(old, next)
	if !d.Changed {
		t.Fatal("software set changed")
	}
	if len(d.Added) != 1 || d.Added[0] != "VLC" {
		t.Fatalf("added, got %v", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "7-Zip" {
		t.Fatalf("removed, got %v", d.Removed)
	}
	// A version bump alone is not an add or a remove.
	if d2 := diffSoftware(old, old); d2.Changed {
		t.Fatal("identical software set must not report a change")
	}
}

// TestDiffSoftwareMalformed checks that a corrupt snapshot degrades to the
// conservative answer rather than panicking the read loop: with no readable
// previous state, everything currently installed counts as added. That is safe
// because software adds alone never trigger an audit entry — see auditWorthy,
// which only counts hardware and OS build changes.
func TestDiffSoftwareMalformed(t *testing.T) {
	d := diffSoftware("{not json", `[{"name":"Firefox"}]`)
	if !d.Changed || len(d.Added) != 1 {
		t.Fatalf("corrupt history must conservatively report adds, got %+v", d)
	}
	// An empty history (first collection) is the same case in practice.
	d = diffSoftware("", `[{"name":"Firefox"}]`)
	if !d.Changed || len(d.Added) != 1 {
		t.Fatalf("first collection must report all software as added, got %+v", d)
	}
	// Two unreadable snapshots must not claim a change.
	if d := diffSoftware("{broken", "{also broken"); d.Changed {
		t.Fatal("two corrupt snapshots must not report a change")
	}
}

// TestSummary covers the message written into the audit details field.
func TestSummary(t *testing.T) {
	if s := (inventoryDiff{}).summary(softwareDiff{}); s != "no significant change" {
		t.Fatalf("got %q", s)
	}
	got := (inventoryDiff{RAMChanged: true, OSBuildChanged: true}).
		summary(softwareDiff{Added: []string{"VLC"}, Removed: []string{"7-Zip"}})
	if got != "changed: memory, os-build, software+1, software-1" {
		t.Fatalf("got %q", got)
	}
}

// TestPageParams covers the bounds that keep a single dashboard load bounded.
func TestPageParams(t *testing.T) {
	cases := []struct {
		query     string
		wantLimit int
		wantOff   int
	}{
		{"", defaultLimit, 0},
		{"limit=10&offset=20", 10, 20},
		{"limit=99999", maxLimit, 0},
		{"limit=0", defaultLimit, 0},
		{"limit=-5", defaultLimit, 0},
		{"limit=abc", defaultLimit, 0},
		{"offset=abc", defaultLimit, 0},
		{"offset=-1", defaultLimit, 0},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/devices?"+tc.query, nil)
		limit, offset := pageParams(req)
		if limit != tc.wantLimit || offset != tc.wantOff {
			t.Errorf("query %q: limit=%d offset=%d, want %d/%d",
				tc.query, limit, offset, tc.wantLimit, tc.wantOff)
		}
	}
}

// TestFreePctOf guards the cached column the dashboard warns on.
func TestFreePctOf(t *testing.T) {
	if p := freePctOf(1000, 250); p != 25 {
		t.Fatalf("got %v", p)
	}
	if p := freePctOf(0, 0); p != 100 {
		t.Fatalf("unknown total must read as 100%% free, got %v", p)
	}
}

// seedDevice inserts a device row so inventory has a foreign key to point at.
// acceptInventory never creates the device: the enrollment flow does.
func seedDevice(t *testing.T, d *sqlx.DB, id string) {
	t.Helper()
	now := nowUTC()
	_, err := d.Exec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status,
		last_seen_at, enrolled_at, device_secret_hash, site, created_at, updated_at)
		VALUES (?, ?, 'windows', '11', '0.2.0', 'online', ?, ?, 'hash', 'hq', ?, ?)`,
		id, "TEST-"+id, now, now, now, now)
	if err != nil {
		t.Fatalf("seed device: %v", err)
	}
}

// TestAcceptInventoryRoundTrip is the real contract: a report an agent would
// send must decode, populate the cached columns, and be re-readable. It also
// proves the server never imports the agent's build-tagged types.
func TestAcceptInventoryRoundTrip(t *testing.T) {
	db := newTestDB(t)
	seedDevice(t, db, "dev-1")
	repo := newInventoryRepository(db)
	audit := &recordingAudit{}
	h := newInventoryHandler(repo, audit, offlineHub{})

	rep := inventoryReport{
		Hardware: json.RawMessage(`{"cpu":{"name":"Intel i5-1135G7","number_of_cores":4,"logical_processors":8},"ram_total_bytes":16905961472,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":102238302208}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`),
		Software: json.RawMessage(`[{"name":"7-Zip","version":"23.01"}]`),
		OS:       json.RawMessage(`{"edition":"Pro","build_number":"22631","architecture":"x64"}`),
	}
	raw, _ := json.Marshal(rep)

	if err := h.acceptInventory(t.Context(), "dev-1", raw); err != nil {
		t.Fatalf("accept: %v", err)
	}

	inv, err := repo.getInventory(t.Context(), "dev-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if inv.HWRAMBytes == nil || *inv.HWRAMBytes != 16905961472 {
		t.Fatalf("cached ram, got %v", inv.HWRAMBytes)
	}
	if inv.HWCPUModel == nil || *inv.HWCPUModel != "Intel i5-1135G7" {
		t.Fatalf("cached cpu, got %v", inv.HWCPUModel)
	}
	if inv.HWDiskFreePct == nil {
		t.Fatal("cached disk free pct missing")
	}
	if got := *inv.HWDiskFreePct; got < 19.9 || got > 20.1 {
		t.Fatalf("disk free pct, got %v", got)
	}
	var hw hwJSON
	if err := json.Unmarshal([]byte(inv.HW), &hw); err != nil {
		t.Fatalf("stored hw is not JSON: %v", err)
	}
	if hw.Model == nil || hw.Model.SerialNumber != "ABCD123" {
		t.Fatalf("stored model, got %v", hw.Model)
	}

	// Second report with swapped hardware must reach the audit trail.
	rep2 := rep
	rep2.Hardware = json.RawMessage(`{"cpu":{"name":"Intel i7-1265G7","number_of_cores":10,"logical_processors":12},"ram_total_bytes":33811922944,"disks":[{"name":"C:","total_bytes":511191511040,"free_bytes":102238302208}],"model":{"vendor":"Dell Inc.","product":"Latitude 3420","serial_number":"ABCD123"}}`)
	raw2, _ := json.Marshal(rep2)
	if err := h.acceptInventory(t.Context(), "dev-1", raw2); err != nil {
		t.Fatalf("accept 2: %v", err)
	}
	if len(audit.calls) != 1 {
		t.Fatalf("hardware swap must produce one audit entry, got %d", len(audit.calls))
	}
	if audit.calls[0].action != "inventory.hw_changed" {
		t.Fatalf("audit action, got %q", audit.calls[0].action)
	}

	// A third report identical to the second must not spam the trail.
	if err := h.acceptInventory(t.Context(), "dev-1", raw2); err != nil {
		t.Fatalf("accept 3: %v", err)
	}
	if len(audit.calls) != 1 {
		t.Fatalf("unchanged hardware must not audit again, got %d", len(audit.calls))
	}
}

// TestAcceptInventoryMalformedTimestamp proves a clock-skewed or partial report
// is still stored rather than rejected.
func TestAcceptInventoryMalformedTimestamp(t *testing.T) {
	db := newTestDB(t)
	seedDevice(t, db, "dev-1")
	repo := newInventoryRepository(db)
	h := newInventoryHandler(repo, &recordingAudit{}, offlineHub{})

	raw := json.RawMessage(`{"hardware":{},"software":[],"os":{},"collected_at":"not-a-date"}`)
	if err := h.acceptInventory(t.Context(), "dev-1", raw); err != nil {
		t.Fatalf("malformed timestamp should fall back to now, got: %v", err)
	}
	if _, err := repo.getInventory(t.Context(), "dev-1"); err != nil {
		t.Fatalf("report should have been stored: %v", err)
	}
}
