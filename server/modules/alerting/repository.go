package alerting

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type AlertRule struct {
	ID           string    `json:"id" db:"id"`
	Name         string    `json:"name" db:"name"`
	RuleType     string    `json:"rule_type" db:"rule_type"`
	ThresholdVal float64   `json:"threshold_val" db:"threshold_val"`
	Severity     string    `json:"severity" db:"severity"`
	WebhookURL   string    `json:"webhook_url" db:"webhook_url"`
	IsEnabled    bool      `json:"is_enabled" db:"is_enabled"`
	CreatedBy    string    `json:"created_by" db:"created_by"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type AlertIncident struct {
	ID               string     `json:"id" db:"id"`
	RuleID           string     `json:"rule_id" db:"rule_id"`
	DeviceID         string     `json:"device_id" db:"device_id"`
	Severity         string     `json:"severity" db:"severity"`
	Title            string     `json:"title" db:"title"`
	Message          string     `json:"message" db:"message"`
	Status           string     `json:"status" db:"status"` // 'open', 'acknowledged', 'resolved'
	TriggerCount     int        `json:"trigger_count" db:"trigger_count"`
	AcknowledgedBy   *string    `json:"acknowledged_by" db:"acknowledged_by"`
	AcknowledgedAt   *time.Time `json:"acknowledged_at" db:"acknowledged_at"`
	ResolvedBy       *string    `json:"resolved_by" db:"resolved_by"`
	ResolvedAt       *time.Time `json:"resolved_at" db:"resolved_at"`
	FirstTriggeredAt time.Time  `json:"first_triggered_at" db:"first_triggered_at"`
	LastTriggeredAt  time.Time  `json:"last_triggered_at" db:"last_triggered_at"`

	// Joined fields
	Hostname string `json:"hostname,omitempty" db:"hostname"`
	Site     string `json:"site,omitempty" db:"site"`
	RuleName string `json:"rule_name,omitempty" db:"rule_name"`
}

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func NewID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (r *Repository) ListRules(ctx context.Context) ([]AlertRule, error) {
	rules := []AlertRule{}
	err := r.db.SelectContext(ctx, &rules, `
		SELECT id, name, rule_type, threshold_val, severity, webhook_url, is_enabled, created_by, created_at, updated_at
		FROM alert_rules
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	return rules, nil
}

func (r *Repository) GetRuleByID(ctx context.Context, id string) (*AlertRule, error) {
	var rule AlertRule
	err := r.db.GetContext(ctx, &rule, `
		SELECT id, name, rule_type, threshold_val, severity, webhook_url, is_enabled, created_by, created_at, updated_at
		FROM alert_rules
		WHERE id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get alert rule: %w", err)
	}
	return &rule, nil
}

func (r *Repository) CreateRule(ctx context.Context, rule *AlertRule) error {
	now := time.Now().UTC()
	if rule.ID == "" {
		rule.ID = NewID()
	}
	rule.CreatedAt = now
	rule.UpdatedAt = now

	_, err := r.db.ExecContext(ctx, `
		INSERT INTO alert_rules (
			id, name, rule_type, threshold_val, severity, webhook_url, is_enabled, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rule.ID, rule.Name, rule.RuleType, rule.ThresholdVal, rule.Severity, rule.WebhookURL,
		rule.IsEnabled, rule.CreatedBy, rule.CreatedAt, rule.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create alert rule: %w", err)
	}
	return nil
}

func (r *Repository) UpdateRule(ctx context.Context, rule *AlertRule) error {
	rule.UpdatedAt = time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE alert_rules SET
			name = ?, rule_type = ?, threshold_val = ?, severity = ?,
			webhook_url = ?, is_enabled = ?, updated_at = ?
		WHERE id = ?
	`, rule.Name, rule.RuleType, rule.ThresholdVal, rule.Severity,
		rule.WebhookURL, rule.IsEnabled, rule.UpdatedAt, rule.ID)
	if err != nil {
		return fmt.Errorf("update alert rule: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("alert rule not found")
	}
	return nil
}

func (r *Repository) DeleteRule(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("alert rule not found")
	}
	return nil
}

func (r *Repository) ListIncidents(ctx context.Context, status, severity string, limit int) ([]AlertIncident, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `
		SELECT
			i.id, i.rule_id, i.device_id, i.severity, i.title, i.message, i.status,
			i.trigger_count, i.acknowledged_by, i.acknowledged_at, i.resolved_by, i.resolved_at,
			i.first_triggered_at, i.last_triggered_at,
			COALESCE(d.hostname, '') as hostname,
			COALESCE(d.site, '') as site,
			COALESCE(r.name, '') as rule_name
		FROM alert_incidents i
		LEFT JOIN devices d ON d.id = i.device_id
		LEFT JOIN alert_rules r ON r.id = i.rule_id
		WHERE 1=1
	`
	var args []any
	if status != "" {
		query += " AND i.status = ?"
		args = append(args, status)
	}
	if severity != "" {
		query += " AND i.severity = ?"
		args = append(args, severity)
	}
	query += " ORDER BY i.last_triggered_at DESC LIMIT ?"
	args = append(args, limit)

	incidents := []AlertIncident{}
	if err := r.db.SelectContext(ctx, &incidents, query, args...); err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	return incidents, nil
}

func (r *Repository) GetIncidentByID(ctx context.Context, id string) (*AlertIncident, error) {
	var incident AlertIncident
	err := r.db.GetContext(ctx, &incident, `
		SELECT
			i.id, i.rule_id, i.device_id, i.severity, i.title, i.message, i.status,
			i.trigger_count, i.acknowledged_by, i.acknowledged_at, i.resolved_by, i.resolved_at,
			i.first_triggered_at, i.last_triggered_at,
			COALESCE(d.hostname, '') as hostname,
			COALESCE(d.site, '') as site,
			COALESCE(r.name, '') as rule_name
		FROM alert_incidents i
		LEFT JOIN devices d ON d.id = i.device_id
		LEFT JOIN alert_rules r ON r.id = i.rule_id
		WHERE i.id = ?
	`, id)
	if err != nil {
		return nil, fmt.Errorf("get incident: %w", err)
	}
	return &incident, nil
}

func (r *Repository) UpsertIncident(ctx context.Context, ruleID, deviceID, severity, title, message string) (*AlertIncident, bool, error) {
	now := time.Now().UTC()

	// Check if active incident already exists for (rule_id, device_id)
	var existing AlertIncident
	err := r.db.GetContext(ctx, &existing, `
		SELECT id, trigger_count, status
		FROM alert_incidents
		WHERE rule_id = ? AND device_id = ? AND status != 'resolved'
		LIMIT 1
	`, ruleID, deviceID)

	if err == nil {
		// Active incident exists -> Increment trigger_count, update message and last_triggered_at (dedup)
		_, updateErr := r.db.ExecContext(ctx, `
			UPDATE alert_incidents SET
				trigger_count = trigger_count + 1,
				message = ?,
				last_triggered_at = ?
			WHERE id = ?
		`, message, now, existing.ID)
		if updateErr != nil {
			return nil, false, fmt.Errorf("update incident trigger: %w", updateErr)
		}
		inc, _ := r.GetIncidentByID(ctx, existing.ID)
		return inc, false, nil // false = not newly created
	}

	// Create new incident
	newID := NewID()
	_, insertErr := r.db.ExecContext(ctx, `
		INSERT INTO alert_incidents (
			id, rule_id, device_id, severity, title, message, status,
			trigger_count, first_triggered_at, last_triggered_at
		) VALUES (?, ?, ?, ?, ?, ?, 'open', 1, ?, ?)
	`, newID, ruleID, deviceID, severity, title, message, now, now)
	if insertErr != nil {
		return nil, false, fmt.Errorf("insert incident: %w", insertErr)
	}

	inc, _ := r.GetIncidentByID(ctx, newID)
	return inc, true, nil // true = newly created
}

func (r *Repository) AcknowledgeIncident(ctx context.Context, id, userID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE alert_incidents SET
			status = 'acknowledged',
			acknowledged_by = ?,
			acknowledged_at = ?
		WHERE id = ? AND status = 'open'
	`, userID, now, id)
	if err != nil {
		return fmt.Errorf("acknowledge incident: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("incident not found or not in open status")
	}
	return nil
}

func (r *Repository) ResolveIncident(ctx context.Context, id, userID string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE alert_incidents SET
			status = 'resolved',
			resolved_by = ?,
			resolved_at = ?
		WHERE id = ? AND status != 'resolved'
	`, userID, now, id)
	if err != nil {
		return fmt.Errorf("resolve incident: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("incident not found or already resolved")
	}
	return nil
}
