package dashboard

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jmoiron/sqlx"
)

// Repository handles analytical and aggregation queries for the dashboard.
type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

// GetSummary aggregates top-level counts and ratios.
func (r *Repository) GetSummary(ctx context.Context) (Summary, error) {
	var s Summary

	type counts struct {
		Total   int `db:"total"`
		Online  int `db:"online"`
		Offline int `db:"offline"`
		Retired int `db:"retired"`
	}
	var c counts
	queryCounts := `
		SELECT
			COALESCE(SUM(CASE WHEN retired_at IS NULL THEN 1 ELSE 0 END), 0) as total,
			COALESCE(SUM(CASE WHEN retired_at IS NULL AND status = 'online' THEN 1 ELSE 0 END), 0) as online,
			COALESCE(SUM(CASE WHEN retired_at IS NULL AND status = 'offline' THEN 1 ELSE 0 END), 0) as offline,
			COALESCE(SUM(CASE WHEN retired_at IS NOT NULL THEN 1 ELSE 0 END), 0) as retired
		FROM devices`
	if err := r.db.GetContext(ctx, &c, queryCounts); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("dashboard count devices: %w", err)
	}

	s.TotalDevices = c.Total
	s.OnlineDevices = c.Online
	s.OfflineDevices = c.Offline
	s.RetiredDevices = c.Retired
	if s.TotalDevices > 0 {
		s.OnlinePct = math.Round((float64(s.OnlineDevices)/float64(s.TotalDevices))*1000) / 10
	}

	// Low disk alerts count (< 15% free on active devices)
	queryLowDisk := `
		SELECT COUNT(*)
		FROM devices d
		JOIN device_inventory i ON d.id = i.device_id
		WHERE d.retired_at IS NULL
		  AND i.hw_disk_free_pct IS NOT NULL
		  AND i.hw_disk_free_pct < 15.0`
	if err := r.db.GetContext(ctx, &s.LowDiskAlerts, queryLowDisk); err != nil {
		s.LowDiskAlerts = 0
	}

	// Recent hardware changes in last 24h
	cutoff24h := time.Now().UTC().Add(-24 * time.Hour)
	queryHWChanges := `
		SELECT COUNT(*)
		FROM audit_logs
		WHERE action = 'inventory.hw_changed'
		  AND created_at >= ?`
	if err := r.db.GetContext(ctx, &s.RecentHWChanges24h, queryHWChanges, cutoff24h); err != nil {
		s.RecentHWChanges24h = 0
	}

	// Distinct active sites count
	querySites := `
		SELECT COUNT(DISTINCT site)
		FROM devices
		WHERE retired_at IS NULL AND site IS NOT NULL AND site != ''`
	if err := r.db.GetContext(ctx, &s.SitesCount, querySites); err != nil {
		s.SitesCount = 0
	}

	return s, nil
}

// GetSiteMetrics returns breakdown of devices per site.
func (r *Repository) GetSiteMetrics(ctx context.Context) ([]SiteMetric, error) {
	q := `
		SELECT
			COALESCE(site, 'unassigned') as site,
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'online' THEN 1 ELSE 0 END), 0) as online,
			COALESCE(SUM(CASE WHEN status = 'offline' THEN 1 ELSE 0 END), 0) as offline
		FROM devices
		WHERE retired_at IS NULL
		GROUP BY site
		ORDER BY total DESC, site ASC`

	list := []SiteMetric{}
	if err := r.db.SelectContext(ctx, &list, q); err != nil {
		return nil, fmt.Errorf("dashboard site metrics: %w", err)
	}

	for i := range list {
		if list[i].Total > 0 {
			list[i].OnlinePct = math.Round((float64(list[i].Online)/float64(list[i].Total))*1000) / 10
		}
	}
	return list, nil
}

// GetOSMetrics returns breakdown of devices across operating systems.
func (r *Repository) GetOSMetrics(ctx context.Context) ([]OSMetric, error) {
	q := `
		SELECT
			COALESCE(os_name, 'unknown') as os_name,
			COUNT(*) as count
		FROM devices
		WHERE retired_at IS NULL
		GROUP BY os_name
		ORDER BY count DESC`

	list := []OSMetric{}
	if err := r.db.SelectContext(ctx, &list, q); err != nil {
		return nil, fmt.Errorf("dashboard os metrics: %w", err)
	}

	var total int
	for _, m := range list {
		total += m.Count
	}
	if total > 0 {
		for i := range list {
			list[i].Pct = math.Round((float64(list[i].Count)/float64(total))*1000) / 10
		}
	}
	return list, nil
}

// GetAlerts returns active operational warnings: low disk space and prolonged offline devices.
func (r *Repository) GetAlerts(ctx context.Context) ([]Alert, error) {
	alerts := make([]Alert, 0, 16)

	// 1. Low disk space alerts (< 15%)
	type diskRow struct {
		ID          string    `db:"id"`
		Hostname    string    `db:"hostname"`
		Site        string    `db:"site"`
		FreePct     float64   `db:"hw_disk_free_pct"`
		CollectedAt time.Time `db:"collected_at"`
	}
	var disks []diskRow
	qDisk := `
		SELECT d.id, d.hostname, COALESCE(d.site, '') as site, i.hw_disk_free_pct, i.collected_at
		FROM devices d
		JOIN device_inventory i ON d.id = i.device_id
		WHERE d.retired_at IS NULL
		  AND i.hw_disk_free_pct IS NOT NULL
		  AND i.hw_disk_free_pct < 15.0
		ORDER BY i.hw_disk_free_pct ASC
		LIMIT 15`
	if err := r.db.SelectContext(ctx, &disks, qDisk); err == nil {
		for _, d := range disks {
			sev := "warning"
			if d.FreePct < 5.0 {
				sev = "critical"
			}
			alerts = append(alerts, Alert{
				ID:        fmt.Sprintf("disk-%s", d.ID),
				Type:      "low_disk",
				Severity:  sev,
				DeviceID:  d.ID,
				Hostname:  d.Hostname,
				Site:      d.Site,
				Message:   fmt.Sprintf("Free disk space is critically low: %.1f%% remaining", d.FreePct),
				Timestamp: d.CollectedAt,
			})
		}
	}

	// 2. Prolonged offline devices (offline for > 24h)
	cutoff24h := time.Now().UTC().Add(-24 * time.Hour)
	type offlineRow struct {
		ID         string     `db:"id"`
		Hostname   string     `db:"hostname"`
		Site       string     `db:"site"`
		LastSeenAt *time.Time `db:"last_seen_at"`
	}
	var offlines []offlineRow
	qOffline := `
		SELECT id, hostname, COALESCE(site, '') as site, last_seen_at
		FROM devices
		WHERE retired_at IS NULL
		  AND status = 'offline'
		  AND last_seen_at IS NOT NULL
		  AND last_seen_at < ?
		ORDER BY last_seen_at ASC
		LIMIT 15`
	if err := r.db.SelectContext(ctx, &offlines, qOffline, cutoff24h); err == nil {
		for _, o := range offlines {
			ts := time.Now().UTC()
			if o.LastSeenAt != nil {
				ts = *o.LastSeenAt
			}
			alerts = append(alerts, Alert{
				ID:        fmt.Sprintf("offline-%s", o.ID),
				Type:      "offline_long",
				Severity:  "warning",
				DeviceID:  o.ID,
				Hostname:  o.Hostname,
				Site:      o.Site,
				Message:   fmt.Sprintf("Device has been unreachable since %s", ts.Format("2006-01-02 15:04 UTC")),
				Timestamp: ts,
			})
		}
	}

	return alerts, nil
}

// GetRecentActivity returns the latest audit log records for the timeline.
func (r *Repository) GetRecentActivity(ctx context.Context, limit int) ([]ActivityItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	q := `
		SELECT id, actor_type, COALESCE(actor_id, '') AS actor_id, action,
		       COALESCE(target_id, '') AS target_id,
		       COALESCE(details, '{}') as details, created_at
		FROM audit_logs
		ORDER BY created_at DESC
		LIMIT ?`
	items := []ActivityItem{}
	if err := r.db.SelectContext(ctx, &items, q, limit); err != nil {
		return nil, fmt.Errorf("dashboard recent activity: %w", err)
	}
	return items, nil
}
