package devicemanagement

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func seedAgentVersions(t *testing.T) *Repository {
	t.Helper()
	d := newTestDB(t)
	now := time.Now().UTC()
	for _, row := range []struct {
		id      string
		version any
		retired any
	}{
		{"a", "1.5.6", nil},
		{"b", "1.5.6", nil},
		{"c", "1.5.10", nil},
		{"d", "1.5.9", nil},
		{"pending", nil, nil},
		{"empty", "", nil},
		{"gone", "1.4.0", now},
	} {
		d.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, device_secret_hash,
			enrolled_at, created_at, updated_at, retired_at) VALUES (?, ?, 'windows', '11', ?, 'online', 'h', ?, ?, ?, ?)`,
			row.id, row.id, row.version, now, now, now, row.retired)
	}
	return NewRepository(d)
}

// The Devices page could filter by status and site but not by agent version,
// so finding the machines still on an old agent meant paging through the
// whole fleet. The filter is applied in SQL, so it holds across pages.
func TestDevicesCanBeFilteredByAgentVersion(t *testing.T) {
	repo := seedAgentVersions(t)
	ctx := context.Background()

	ids := func(version string) []string {
		devs, total, err := repo.ListPaged(ctx, "", "", version, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, d := range devs {
			out = append(out, d.ID)
		}
		if total != len(out) {
			t.Errorf("version %q: total %d but %d rows", version, total, len(out))
		}
		return out
	}
	if got := ids("1.5.6"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("1.5.6 = %v, want [a b]", got)
	}
	if got := ids(UnknownAgentVersion); !reflect.DeepEqual(got, []string{"empty", "pending"}) {
		t.Errorf("unknown = %v, want the devices with no version", got)
	}
	if got := ids("1.4.0"); len(got) != 0 {
		t.Errorf("a retired device's version matched: %v", got)
	}
	if got := ids(""); len(got) != 6 {
		t.Errorf("no agent filter returned %d devices, want 6", len(got))
	}
}

// The filter's choices: every version the active fleet runs, newest first by
// number (1.5.10 above 1.5.9), with the unversioned devices last.
func TestAgentVersionsAreListedNewestFirstWithCounts(t *testing.T) {
	repo := seedAgentVersions(t)
	got, err := repo.AgentVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []AgentVersionCount{{"1.5.10", 1}, {"1.5.9", 1}, {"1.5.6", 2}, {"", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AgentVersions = %v, want %v", got, want)
	}
}
