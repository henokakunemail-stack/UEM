package devicemanagement

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// chanObserver hands every notification to the test.
type chanObserver chan []string

func (c chanObserver) DevicesRegrouped(_ context.Context, ids []string) { c <- ids }

func regroupFixture(t *testing.T) (*inventoryHandler, chanObserver) {
	t.Helper()
	d := newTestDB(t)
	seedDevice(t, d, "dev-1")
	seedDevice(t, d, "dev-2")
	now := nowUTC()
	if _, err := d.Exec(`INSERT INTO device_groups (id, name, description, created_at, updated_at)
		VALUES ('g1', 'Finance', '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	obs := make(chanObserver, 4)
	h := newInventoryHandler(newInventoryRepository(d), &recordingAudit{}, offlineHub{}).
		WithMembershipObservers(obs)
	return h, obs
}

func callGroupRoute(h http.HandlerFunc, method, path, body string, params map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func waitRegrouped(t *testing.T, obs chanObserver) []string {
	t.Helper()
	select {
	case ids := <-obs:
		return ids
	case <-time.After(3 * time.Second):
		t.Fatal("no membership notification: a device moved into or out of a group keeps " +
			"the policies of its old groups until it reconnects")
		return nil
	}
}

func TestAddMembersNotifiesObservers(t *testing.T) {
	h, obs := regroupFixture(t)
	rec := callGroupRoute(h.addMembers, http.MethodPost, "/api/groups/g1/members",
		`{"device_ids":["dev-1","dev-2"]}`, map[string]string{"id": "g1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("add members -> %d %s", rec.Code, rec.Body.String())
	}
	ids := waitRegrouped(t, obs)
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"dev-1", "dev-2"}) {
		t.Errorf("notified %v, want [dev-1 dev-2]", ids)
	}
}

func TestRemoveMemberNotifiesObservers(t *testing.T) {
	h, obs := regroupFixture(t)
	if rec := callGroupRoute(h.addMembers, http.MethodPost, "/api/groups/g1/members",
		`{"device_ids":["dev-1"]}`, map[string]string{"id": "g1"}); rec.Code != http.StatusOK {
		t.Fatalf("add -> %d", rec.Code)
	}
	waitRegrouped(t, obs)

	rec := callGroupRoute(h.removeMember, http.MethodDelete, "/api/groups/g1/members/dev-1", "",
		map[string]string{"id": "g1", "deviceId": "dev-1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("remove -> %d %s", rec.Code, rec.Body.String())
	}
	if ids := waitRegrouped(t, obs); !slices.Equal(ids, []string{"dev-1"}) {
		t.Errorf("notified %v, want [dev-1]", ids)
	}
}
