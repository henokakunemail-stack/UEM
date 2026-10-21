package dashboard

import "time"

// Summary holds high-level KPI aggregations for the central dashboard.
type Summary struct {
	TotalDevices       int     `json:"total_devices"`
	OnlineDevices      int     `json:"online_devices"`
	OfflineDevices     int     `json:"offline_devices"`
	RetiredDevices     int     `json:"retired_devices"`
	OnlinePct          float64 `json:"online_pct"`
	LowDiskAlerts      int     `json:"low_disk_alerts"`
	RecentHWChanges24h int     `json:"recent_hw_changes_24h"`
	SitesCount         int     `json:"sites_count"`
	// PendingDevices are enrollment rows that have never checked in. They are
	// not devices yet, so they are not in TotalDevices or OfflineDevices; they
	// used to be counted offline and pulled the online percentage down.
	PendingDevices int `json:"pending_devices"`
	// AlertsTotal is every health alert, not the first page the alert list
	// returns; the dashboard card used to stop at 30.
	AlertsTotal int `json:"alerts_total"`
}

// SiteMetric breaks down fleet health per office or branch site.
type SiteMetric struct {
	Site      string  `json:"site" db:"site"`
	Total     int     `json:"total" db:"total"`
	Online    int     `json:"online" db:"online"`
	Offline   int     `json:"offline" db:"offline"`
	OnlinePct float64 `json:"online_pct"`
}

// OSMetric breaks down devices across operating systems.
type OSMetric struct {
	OSName string  `json:"os_name" db:"os_name"`
	Count  int     `json:"count" db:"count"`
	Pct    float64 `json:"pct"`
}

// Alert represents an operational warning or alert across the fleet.
type Alert struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`     // low_disk, offline_long, hw_drift
	Severity  string    `json:"severity"` // warning, critical, info
	DeviceID  string    `json:"device_id"`
	Hostname  string    `json:"hostname"`
	Site      string    `json:"site"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
	// Value is the measured number behind the alert (free disk % for
	// low_disk), so the console can word it in the reader's language.
	Value *float64 `json:"value,omitempty"`
}

// ActivityItem represents a recent audit or lifecycle event for the activity stream.
type ActivityItem struct {
	ID        string    `json:"id" db:"id"`
	ActorType string    `json:"actor_type" db:"actor_type"`
	ActorID   string    `json:"actor_id" db:"actor_id"`
	Action    string    `json:"action" db:"action"`
	TargetID  string    `json:"target_id" db:"target_id"`
	Details   string    `json:"details" db:"details"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}
