package directory

import (
	"bytes"
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

// fakeClient stands in for the directory. Everything above this line — the
// diff, the repository, the handlers, the RBAC — runs for real; only the
// network is absent.
type fakeClient struct {
	contacts []Contact
	// err, when set, is returned from Search. The bind-failure path needs it:
	// a real refused bind is an error, not an empty contact list, and code
	// that treats "0 entries" as success would sail straight past.
	err error
}

func (f *fakeClient) Search(context.Context, Config) ([]Contact, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.contacts, nil
}

func (f *fakeClient) Test(context.Context, Config) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	return len(f.contacts), nil
}

func contact(extID, name, dept string) Contact {
	return Contact{
		ExternalID:        extID,
		DistinguishedName: "CN=" + name + ",OU=IT,DC=example,DC=com",
		DisplayName:       name,
		Email:             name + "@example.com",
		Department:        dept,
		Source:            "ldap",
		IsActive:          true,
	}
}

func directoryFixture(t *testing.T, incoming []Contact) (*sqlx.DB, chi.Router) {
	t.Helper()
	d, r, _ := directoryFixtureWithClient(t, incoming)
	return d, r
}

// directoryFixtureWithClient also hands back the fake, so a test can change
// what the directory returns between two syncs — which is the only way to
// exercise somebody leaving the company.
func directoryFixtureWithClient(t *testing.T, incoming []Contact) (*sqlx.DB, chi.Router, *fakeClient) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	r := chi.NewRouter()
	client := &fakeClient{contacts: incoming}
	NewHandler(NewRepository(d), nopAuditor{}, client, "env-bind-secret",
		func(next http.Handler) http.Handler { return next }).Register(r)
	return d, r, client
}

func doJSON(t *testing.T, r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONAs(t, r, method, path, body, rbac.RoleAdmin)
}

func doJSONAs(t *testing.T, r chi.Router, method, path, body string, role string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	}
	req = req.WithContext(rbac.WithRole(req.Context(), role))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func saveConfig(t *testing.T, r chi.Router) {
	t.Helper()
	body := `{"host":"ldap.example.com","port":636,"use_tls":true,` +
		`"base_dn":"DC=example,DC=com","bind_dn":"CN=svc,DC=example,DC=com",` +
		`"search_filter":"(objectClass=person)"}`
	if rec := doJSON(t, r, "PUT", "/api/directory/config", body); rec.Code != 200 {
		t.Fatalf("save config: %d %s", rec.Code, rec.Body.String())
	}
}

// A first sync against an empty database has to add everything.
func TestAPersonNewToTheServerIsAnAdd(t *testing.T) {
	plan := diffContacts(nil, []Contact{
		contact("uuid-1", "Ana Putri", "Finance"),
		contact("uuid-2", "Budi Santoso", "IT"),
	})

	if len(plan.Adds) != 2 {
		t.Fatalf("adds = %d, want 2", len(plan.Adds))
	}
	if len(plan.Updates) != 0 || len(plan.Deactivations) != 0 {
		t.Errorf("a clean first sync reported %d updates and %d deactivations",
			len(plan.Updates), len(plan.Deactivations))
	}
	if plan.TotalInDirectory != 2 {
		t.Errorf("total_in_directory = %d, want 2", plan.TotalInDirectory)
	}
}

// The same people, unchanged, must be counted as unchanged and NOT rewritten.
// A diff that reported an update for every row would turn every sync into N
// writes that change nothing, and Settings would never stop saying "2 to
// update" on an estate that has not moved.
func TestAnUnchangedContactIsNotRewritten(t *testing.T) {
	incoming := []Contact{contact("uuid-1", "Ana Putri", "Finance")}

	plan := diffContacts(incoming, incoming)

	if !plan.Empty() {
		t.Fatalf("an identical directory reported changes: %d adds, %d updates, %d deactivations",
			len(plan.Adds), len(plan.Updates), len(plan.Deactivations))
	}
	if plan.Unchanged != 1 {
		t.Errorf("unchanged = %d, want 1", plan.Unchanged)
	}
}

// A promotion moves title. That is an update, and it must keep the row's own
// identity — otherwise every sync inserts a new row for the same human being
// and the PIC dropdown fills with duplicates of themselves.
func TestAPromotionUpdatesInPlaceAndKeepsTheRowIdentity(t *testing.T) {
	existing := []Contact{contact("uuid-1", "Ana Putri", "Finance")}
	existing[0].ID = "row-42"
	existing[0].FirstSeenAt = time.Now().Add(-30 * 24 * time.Hour).UTC()

	promoted := contact("uuid-1", "Ana Putri", "Finance")
	promoted.Title = "Finance Manager"

	plan := diffContacts(existing, []Contact{promoted})

	if len(plan.Updates) != 1 {
		t.Fatalf("updates = %d, want 1 (%+v)", len(plan.Updates), plan)
	}
	if len(plan.Adds) != 0 {
		t.Errorf("a changed person was also reported as an add (%d)", len(plan.Adds))
	}
	if plan.Updates[0].ID != "row-42" {
		t.Errorf("update carries ID %q, want the existing row-42 — a new id inserts a duplicate", plan.Updates[0].ID)
	}
	if !plan.Updates[0].FirstSeenAt.Equal(existing[0].FirstSeenAt) {
		t.Errorf("update reset first_seen_at; every sync would look like a new hire")
	}
	if plan.Updates[0].Title != "Finance Manager" {
		t.Errorf("title = %q, want the new one", plan.Updates[0].Title)
	}
}

// Somebody who leaves disappears from the directory. The right action is
// deactivating, NOT deleting: hardware_assets.assigned_user stores a display
// name, and deleting the row would leave a machine whose owner reads as blank
// in every future report.
func TestSomebodyWhoLeftIsDeactivatedNotDeleted(t *testing.T) {
	plan := diffContacts([]Contact{contact("uuid-1", "Ana Putri", "Finance")}, nil)

	if len(plan.Deactivations) != 1 {
		t.Fatalf("deactivations = %d, want 1", len(plan.Deactivations))
	}
	if plan.Deactivations[0].ExternalID != "uuid-1" {
		t.Errorf("deactivated the wrong contact: %q", plan.Deactivations[0].ExternalID)
	}
	if len(plan.Adds) != 0 {
		t.Errorf("a departure was reported as an add")
	}
}

// Subtle, and invisible on screen: a contact deactivated by an earlier sync is
// still absent from the directory, so re-reporting it every run would leave
// Settings claiming a change is pending forever, and every Apply rewriting a
// row that is already correct.
func TestAnAlreadyDeactivatedContactIsNotReportedAgain(t *testing.T) {
	gone := contact("uuid-1", "Ana Putri", "Finance")
	gone.IsActive = false

	plan := diffContacts([]Contact{gone}, nil)

	if len(plan.Deactivations) != 0 {
		t.Errorf("deactivations = %d, want 0 — an inactive contact is already in that state",
			len(plan.Deactivations))
	}
}

// Somebody who left and came back has identical directory fields. A
// fields-only comparison calls them unchanged, and they stay permanently out of
// the PIC dropdown with nothing in the UI explaining why.
func TestAReactivatedPersonIsAnUpdate(t *testing.T) {
	gone := contact("uuid-1", "Ana Putri", "Finance")
	gone.ID = "row-7"
	gone.IsActive = false

	plan := diffContacts([]Contact{gone}, []Contact{contact("uuid-1", "Ana Putri", "Finance")})

	if len(plan.Updates) != 1 {
		t.Fatalf("updates = %d, want 1 — a returning employee needs an update, not 'unchanged'", len(plan.Updates))
	}
	if plan.Updates[0].ID != "row-7" {
		t.Errorf("update carries ID %q, want row-7", plan.Updates[0].ID)
	}
	if !plan.Updates[0].IsActive {
		t.Error("the update does not reactivate the contact")
	}
}

// A directory that returns the same entry twice — paging across a reorg, two
// matching search bases — must not try to insert one UNIQUE external_id twice,
// which would abort the whole transaction and lose the entire sync.
func TestADuplicateEntryFromTheDirectoryIsCollapsed(t *testing.T) {
	dupe := contact("uuid-1", "Ana Putri", "Finance")

	plan := diffContacts(nil, []Contact{dupe, dupe})

	if len(plan.Adds) != 1 {
		t.Fatalf("adds = %d, want 1 — the duplicate would violate the UNIQUE index", len(plan.Adds))
	}
}

// The point of preview-then-apply: preview writes nothing. Driven through the
// real handler and repository, because a diff-only test would not catch a
// handler that writes on the way to computing the plan.
func TestPreviewWritesNothingAndApplyDoes(t *testing.T) {
	d, r := directoryFixture(t, []Contact{
		contact("uuid-1", "Ana Putri", "Finance"),
		contact("uuid-2", "Budi Santoso", "IT"),
	})
	saveConfig(t, r)

	if rec := doJSON(t, r, "POST", "/api/directory/sync/preview", ""); rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("preview wrote %d contacts — it is supposed to only show the plan", n)
	}

	if rec := doJSON(t, r, "POST", "/api/directory/sync/apply", ""); rec.Code != 200 {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts WHERE is_active = 1`); err != nil {
		t.Fatalf("count after apply: %v", err)
	}
	if n != 2 {
		t.Fatalf("apply stored %d contacts, want 2", n)
	}

	// Applying again must converge. A sync that keeps reporting the same rows
	// as new work every time is a sync nobody can trust.
	if rec := doJSON(t, r, "POST", "/api/directory/sync/apply", ""); rec.Code != 200 {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body.String())
	}
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts`); err != nil {
		t.Fatalf("final count: %v", err)
	}
	if n != 2 {
		t.Fatalf("a second apply changed the row count to %d — it is not converging", n)
	}
}

// A departure has to survive the write path too, not just the diff: the plan
// says deactivate, and the repository must not turn that into a delete.
func TestADepartedContactStaysInTheDatabaseAfterApply(t *testing.T) {
	d, r, client := directoryFixtureWithClient(t, []Contact{
		contact("uuid-1", "Ana Putri", "Finance"),
		contact("uuid-2", "Budi Santoso", "IT"),
	})
	saveConfig(t, r)

	if rec := doJSON(t, r, "POST", "/api/directory/sync/apply", ""); rec.Code != 200 {
		t.Fatalf("first apply: %d %s", rec.Code, rec.Body.String())
	}

	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts WHERE is_active = 0`); err != nil {
		t.Fatalf("count inactive: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d inactive after a first sync, want 0", n)
	}

	// Ana leaves the company: the directory now returns only Budi.
	client.contacts = []Contact{contact("uuid-2", "Budi Santoso", "IT")}
	if rec := doJSON(t, r, "POST", "/api/directory/sync/apply", ""); rec.Code != 200 {
		t.Fatalf("second apply: %d %s", rec.Code, rec.Body.String())
	}

	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("contacts after a departure = %d, want 2 — a departed person must be kept, not deleted", n)
	}
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts WHERE is_active = 0`); err != nil {
		t.Fatalf("count inactive: %v", err)
	}
	if n != 1 {
		t.Fatalf("inactive after a departure = %d, want 1", n)
	}

	// And she drops out of the PIC dropdown, because a machine should not be
	// handed to somebody who has left.
	rec := doJSON(t, r, "GET", "/api/directory/contacts", "")
	if rec.Code != 200 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Ana Putri") {
		t.Errorf("a departed person is still offered as a PIC: %s", rec.Body.String())
	}
}

// The bind password is an environment value. Nothing the console sends or the
// server stores may bring it back, and Settings still has to be able to say
// whether one is set.
func TestTheBindPasswordIsNeverEchoedBack(t *testing.T) {
	d, r := directoryFixture(t, nil)

	body := `{"host":"ldap.example.com","port":636,"use_tls":true,` +
		`"base_dn":"DC=example,DC=com","bind_dn":"CN=svc,DC=example,DC=com",` +
		`"bind_password":"hunter2-should-never-be-stored"}`
	rec := doJSON(t, r, "PUT", "/api/directory/config", body)
	if rec.Code != 200 {
		t.Fatalf("save config: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("the response echoed the submitted password: %s", rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/directory/config", "")
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("reading the config echoed a password: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"bind_password_configured":true`) {
		t.Errorf("config did not report the env password as configured: %s", rec.Body.String())
	}

	// And there is no column that could hold one in the first place.
	var cols []string
	if err := d.Select(&cols, `SELECT name FROM pragma_table_info('directory_sync_config')`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	for _, c := range cols {
		if strings.Contains(c, "password") {
			t.Errorf("directory_sync_config has column %q, which could hold a secret", c)
		}
	}
}

// A technician registering an asset needs the PIC dropdown, so contacts are
// readable by a viewer. The directory's configuration is not, and neither is
// the right to run a sync.
func TestOnlyContactsAreReadableWithoutAdmin(t *testing.T) {
	_, r := directoryFixture(t, nil)

	if rec := doJSONAs(t, r, "GET", "/api/directory/contacts", "", rbac.RoleViewer); rec.Code != 200 {
		t.Errorf("viewer reading contacts: %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := doJSONAs(t, r, "GET", "/api/directory/config", "", rbac.RoleViewer); rec.Code == 200 {
		t.Error("viewer read the directory config; its credentials are not viewer data")
	}
	if rec := doJSONAs(t, r, "POST", "/api/directory/sync/apply", "", rbac.RoleViewer); rec.Code == 200 {
		t.Error("viewer applied a sync")
	}
	if rec := doJSONAs(t, r, "POST", "/api/directory/test", "", rbac.RoleTechnician); rec.Code == 200 {
		t.Error("a technician tested the directory connection; only an admin should")
	}
}

// Without a saved config, sync has to say so rather than quietly returning an
// empty plan: "0 adds" reads like a directory that is simply up to date.
func TestSyncWithoutConfigSaysItIsNotConfigured(t *testing.T) {
	_, r := directoryFixture(t, nil)

	rec := doJSON(t, r, "POST", "/api/directory/sync/preview", "")
	if rec.Code != 400 {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not configured") {
		t.Errorf("response = %s, want it to say the directory is not configured", rec.Body.String())
	}
}

// A refused bind is an error, not an empty directory. Reporting it as success
// would show the operator "0 changes" when in fact nothing was read at all —
// the most dangerous way this feature can fail, because it looks like good
// news and somebody acts on it.
func TestADirectoryThatCannotBeReadIsNotAnEmptyDirectory(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	client := &fakeClient{err: &appError{"bind as CN=svc: invalid credentials"}}
	r := chi.NewRouter()
	NewHandler(NewRepository(d), nopAuditor{}, client, "", func(next http.Handler) http.Handler { return next }).Register(r)
	saveConfig(t, r)

	for _, path := range []string{"/api/directory/sync/preview", "/api/directory/sync/apply"} {
		rec := doJSON(t, r, "POST", path, "")
		if rec.Code == 200 {
			t.Errorf("%s returned 200 on a failed bind: %s", path, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "invalid credentials") {
			t.Errorf("%s did not pass the reason through: %s", path, rec.Body.String())
		}
	}

	// Nothing was written, because nothing was read.
	var n int
	if err := d.Get(&n, `SELECT COUNT(*) FROM directory_contacts`); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a failed bind still wrote %d contacts", n)
	}

	// And the connection test must not report a green tick for it.
	//
	// The status is 200 by design: a failed bind is a valid answer to "can I
	// reach the directory?", not a server fault. The verdict lives in ok:false,
	// and carrying it in the body means the console reads one shape whether the
	// bind worked or not — its request() throws on any non-2xx, so a 502 made
	// this verdict unreachable through the normal path.
	rec := doJSON(t, r, "POST", "/api/directory/test", "")
	if rec.Code != 200 {
		t.Fatalf("test connection returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var res struct {
		OK          bool `json:"ok"`
		Message     string
		EntriesSeen int `json:"entries_seen"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode test response: %v", err)
	}
	if res.OK {
		t.Errorf("test connection reported ok for a rejected password: %s", rec.Body.String())
	}
	if !strings.Contains(res.Message, "invalid credentials") {
		t.Errorf("test connection did not pass the reason through: %s", rec.Body.String())
	}
	if res.EntriesSeen != 0 {
		t.Errorf("failed bind reported %d entries seen, want 0", res.EntriesSeen)
	}
}

// A config that cannot be saved should be refused with the field named, since
// the whole form is the operator's only guide to what a good value looks like.
func TestAConfigMissingItsRequiredFieldsIsRefusedByName(t *testing.T) {
	_, r := directoryFixture(t, nil)

	cases := []struct{ body, want string }{
		{`{"port":636,"base_dn":"DC=x","bind_dn":"CN=s"}`, "host is required"},
		{`{"host":"h","port":0,"base_dn":"DC=x","bind_dn":"CN=s"}`, "port must be between"},
		{`{"host":"h","port":636,"base_dn":"DC=x","bind_dn":""}`, "bind_dn is required"},
	}
	for _, c := range cases {
		rec := doJSON(t, r, "PUT", "/api/directory/config", c.body)
		if rec.Code != 400 {
			t.Errorf("%s → status %d, want 400", c.body, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s → %s, want it to mention %q", c.body, rec.Body.String(), c.want)
		}
	}
}

// The list route must never answer with a JSON null. Every other list endpoint
// in this server initializes its slice for exactly that reason, and the console
// dropdown does `list || []` only because it has been burned before.
func TestTheContactListIsNeverNull(t *testing.T) {
	_, r := directoryFixture(t, nil)

	rec := doJSON(t, r, "GET", "/api/directory/contacts", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("empty directory returned %s, want []", got)
	}
	var out []Contact
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
