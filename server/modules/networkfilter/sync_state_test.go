package networkfilter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// syncHandler is the fixture from reconnect_test.go, which already holds one
// online device with one blocking policy -- exactly the state an operator is in
// when they press Sync.
func syncHandler(t *testing.T) *connectFixture { return newConnectFixture(t) }

func putSync(t *testing.T, f *connectFixture, deviceID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/devices/"+deviceID+"/filter/sync", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", deviceID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.h.syncDeviceFilter(rec, req)
	return rec
}

// The dispatch computed a status and returned it in the response body, and wrote
// nothing. device_filter_states therefore kept whatever the agent last reported,
// so a device that had just been pushed a new policy still read as its previous
// status: an operator who reloaded the page was shown a state that no longer
// described the machine, and a device that had never reported anything kept
// reporting the empty "pending" row forever.
func TestSyncingADeviceRecordsWhereThatDispatchLeftIt(t *testing.T) {
	f := syncHandler(t)

	rec := putSync(t, f, "dev-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	state, err := f.repo.GetDeviceFilterState(context.Background(), "dev-1")
	if err != nil {
		t.Fatalf("the dispatch wrote no row, so a reload still shows the old state: %v", err)
	}
	if state.PolicyVersion != f.version {
		t.Errorf("policy_version = %q, want %q", state.PolicyVersion, f.version)
	}
	if state.Status != statusPending {
		t.Errorf("status = %q, want %q: the device has not answered yet, and %q is the "+
			"value the reconnect hook and the state endpoint both use",
			state.Status, statusPending, state.Status)
	}
}

// rules_applied is what the agent last confirmed. This dispatch has not been
// confirmed, so writing the freshly compiled rule count here would claim the
// device is enforcing N rules the moment the bytes leave the server -- the same
// unbacked claim the state row is supposed to avoid.
func TestSyncingDoesNotClaimRulesTheDeviceHasNotConfirmed(t *testing.T) {
	ctx := context.Background()
	f := syncHandler(t)

	if err := f.repo.RecordDeviceFilterState(ctx, &DeviceFilterState{
		DeviceID: "dev-1", PolicyVersion: "old-version", Status: statusSynced, RulesApplied: 7,
	}); err != nil {
		t.Fatal(err)
	}

	if rec := putSync(t, f, "dev-1"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	state, err := f.repo.GetDeviceFilterState(ctx, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.RulesApplied != 7 {
		t.Errorf("rules_applied = %d, want 7: the agent confirmed 7 rules and has not confirmed "+
			"anything since, so this dispatch moved nothing", state.RulesApplied)
	}
	if state.PolicyVersion == "old-version" {
		t.Errorf("policy_version is still %q: the dispatch compiled a new version and did not record it",
			state.PolicyVersion)
	}
}

// 'dispatched' is the command-lifecycle vocabulary every other module uses for a
// task on the wire -- agent update, software deployment, maintenance, patch,
// remote exec. It is not a statement about enforcement, and device_filter_states.
// status documents itself as a closed set that did not contain it, so the row
// could hold a value the rest of the module cannot explain. Whether the bytes went
// out is a fact about the wire and belongs in its own field, leaving the stored
// status inside the set.
func TestTheStoredStatusStaysInsideTheDeclaredSet(t *testing.T) {
	for _, online := range []bool{true, false} {
		f := newConnectFixture(t)
		f.hub.online = online

		rec := putSync(t, f, "dev-1")
		if rec.Code != http.StatusOK {
			t.Fatalf("online=%v: status = %d, want 200 (body: %s)", online, rec.Code, rec.Body.String())
		}

		var got struct {
			Dispatched bool   `json:"dispatched"`
			Status     string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("online=%v: decode response: %v", online, err)
		}

		if got.Dispatched != online {
			t.Errorf("online=%v: response says dispatched=%v; the hub is the only thing that "+
				"knows whether the bytes went out", online, got.Dispatched)
		}
		if !validStatus[got.Status] {
			t.Errorf("online=%v: response status = %q, which is not in the closed set %v",
				online, got.Status, validStatus)
		}
	}
}
