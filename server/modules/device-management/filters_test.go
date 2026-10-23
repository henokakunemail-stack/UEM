package devicemanagement

import (
	"context"
	"testing"
	"time"
)

// The group view dropped the status and site filters and listed retired
// members; an empty site matched no filter at all.
func TestGroupAndSiteFiltersAgree(t *testing.T) {
	d := newTestDB(t)
	now := time.Now().UTC()
	for _, row := range []struct {
		id, status string
		site       any
		retired    any
	}{
		{"on-hq", "online", "hq", nil},
		{"off-hq", "offline", "hq", nil},
		{"on-none", "online", nil, nil},
		{"on-empty", "online", "", nil},
		{"old", "offline", "hq", now},
	} {
		d.MustExec(`INSERT INTO devices (id, hostname, os_name, os_version, agent_version, status, device_secret_hash,
			enrolled_at, created_at, updated_at, site, retired_at) VALUES (?, ?, 'windows','11','1','`+row.status+`','h',?,?,?,?,?)`,
			row.id, row.id, now, now, now, row.site, row.retired)
	}
	d.MustExec(`INSERT INTO device_groups (id, name, description, created_at, updated_at) VALUES ('g','G','',?,?)`, now, now)
	for _, id := range []string{"on-hq", "off-hq", "on-none", "old"} {
		d.MustExec(`INSERT INTO device_group_members (group_id, device_id, added_at) VALUES ('g', ?, ?)`, id, now)
	}
	inv := newInventoryRepository(d)
	ctx := context.Background()

	n, err := inv.countGroupDevices(ctx, "g", "online", "", "")
	if err != nil || n != 2 {
		t.Fatalf("online members = %d (%v), want 2", n, err)
	}
	n, _ = inv.countGroupDevices(ctx, "g", "", "hq", "")
	if n != 2 {
		t.Fatalf("hq members = %d, want 2 (the retired one is not a member)", n)
	}
	groups, err := inv.listGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].MemberCount != 3 {
		t.Fatalf("member_count = %+v (%v), want 3", groups, err)
	}

	repo := NewRepository(d)
	_, total, err := repo.ListPaged(ctx, "", UnassignedSite, "", 50, 0)
	if err != nil || total != 2 {
		t.Fatalf("unassigned devices = %d (%v), want 2 (NULL and empty site)", total, err)
	}
}
