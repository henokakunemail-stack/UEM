package networkfilter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type FilterPolicy struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Description string    `json:"description" db:"description"`
	TargetType  string    `json:"target_type" db:"target_type"` // 'all', 'group', 'device'
	TargetID    string    `json:"target_id" db:"target_id"`
	IsEnabled   bool      `json:"is_enabled" db:"is_enabled"`
	Priority    int       `json:"priority" db:"priority"`
	CreatedBy   string    `json:"created_by" db:"created_by"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`

	RulesCount int `json:"rules_count" db:"rules_count"`
}

type FilterRule struct {
	ID        string    `json:"id" db:"id"`
	PolicyID  string    `json:"policy_id" db:"policy_id"`
	RuleType  string    `json:"rule_type" db:"rule_type"` // 'domain', 'ip_port'
	Pattern   string    `json:"pattern" db:"pattern"`
	Action    string    `json:"action" db:"action"` // 'block', 'allow'
	Category  string    `json:"category" db:"category"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
}

type DeviceFilterState struct {
	DeviceID      string    `json:"device_id" db:"device_id"`
	PolicyVersion string    `json:"policy_version" db:"policy_version"`
	Status        string    `json:"status" db:"status"` // 'synced', 'pending', 'tampered', 'failed'
	RulesApplied  int       `json:"rules_applied" db:"rules_applied"`
	LastAppliedAt time.Time `json:"last_applied_at" db:"last_applied_at"`
	ErrorMessage  string    `json:"error_message" db:"error_message"`
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

func (r *Repository) CreatePolicy(ctx context.Context, p *FilterPolicy) error {
	now := time.Now().UTC()
	if p.ID == "" {
		p.ID = NewID()
	}
	p.CreatedAt = now
	p.UpdatedAt = now

	query := `
		INSERT INTO filter_policies (
			id, name, description, target_type, target_id,
			is_enabled, priority, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		p.ID, p.Name, p.Description, p.TargetType, p.TargetID,
		p.IsEnabled, p.Priority, p.CreatedBy, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create filter policy: %w", err)
	}
	return nil
}

func (r *Repository) GetPolicyByID(ctx context.Context, id string) (*FilterPolicy, error) {
	var p FilterPolicy
	query := `
		SELECT
			p.id, p.name, p.description, p.target_type, p.target_id,
			p.is_enabled, p.priority, p.created_by, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM filter_rules r WHERE r.policy_id = p.id) as rules_count
		FROM filter_policies p
		WHERE p.id = ?
	`
	if err := r.db.GetContext(ctx, &p, query, id); err != nil {
		return nil, fmt.Errorf("get filter policy: %w", err)
	}
	return &p, nil
}

func (r *Repository) ListPolicies(ctx context.Context) ([]FilterPolicy, error) {
	policies := []FilterPolicy{}
	query := `
		SELECT
			p.id, p.name, p.description, p.target_type, p.target_id,
			p.is_enabled, p.priority, p.created_by, p.created_at, p.updated_at,
			(SELECT COUNT(*) FROM filter_rules r WHERE r.policy_id = p.id) as rules_count
		FROM filter_policies p
		ORDER BY p.priority ASC, p.created_at DESC
	`
	if err := r.db.SelectContext(ctx, &policies, query); err != nil {
		return nil, fmt.Errorf("list filter policies: %w", err)
	}
	return policies, nil
}

func (r *Repository) UpdatePolicy(ctx context.Context, p *FilterPolicy) error {
	p.UpdatedAt = time.Now().UTC()
	query := `
		UPDATE filter_policies SET
			name = ?, description = ?, target_type = ?, target_id = ?,
			is_enabled = ?, priority = ?, updated_at = ?
		WHERE id = ?
	`
	res, err := r.db.ExecContext(ctx, query,
		p.Name, p.Description, p.TargetType, p.TargetID,
		p.IsEnabled, p.Priority, p.UpdatedAt, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update filter policy: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("policy not found")
	}
	return nil
}

func (r *Repository) DeletePolicy(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM filter_policies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete filter policy: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("policy not found")
	}
	return nil
}

func (r *Repository) AddRule(ctx context.Context, rule *FilterRule) error {
	now := time.Now().UTC()
	if rule.ID == "" {
		rule.ID = NewID()
	}
	rule.CreatedAt = now
	if rule.Action == "" {
		rule.Action = "block"
	}
	if rule.RuleType == "" {
		rule.RuleType = "domain"
	}
	if rule.Category == "" {
		rule.Category = "custom"
	}

	query := `
		INSERT INTO filter_rules (id, policy_id, rule_type, pattern, action, category, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		rule.ID, rule.PolicyID, rule.RuleType, rule.Pattern, rule.Action, rule.Category, rule.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("add filter rule: %w", err)
	}
	return nil
}

func (r *Repository) ListRulesByPolicy(ctx context.Context, policyID string) ([]FilterRule, error) {
	rules := []FilterRule{}
	query := `
		SELECT id, policy_id, rule_type, pattern, action, category, created_at
		FROM filter_rules
		WHERE policy_id = ?
		ORDER BY created_at ASC
	`
	if err := r.db.SelectContext(ctx, &rules, query, policyID); err != nil {
		return nil, fmt.Errorf("list filter rules: %w", err)
	}
	return rules, nil
}

func (r *Repository) DeleteRule(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM filter_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete filter rule: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("rule not found")
	}
	return nil
}

// CompileEffectiveRules resolves all active policies targeting the device (device,
// group, or all) and returns unique blocked domain patterns.
//
// Every group the device belongs to is collected, not just the first one. A device
// can sit in several groups at once (the membership table is keyed by
// (group_id, device_id), not device_id), and taking an arbitrary single group meant
// a device in three groups silently ignored two of the policies aimed at it -- with
// no error anywhere, because the query succeeded and returned a short list.
func (r *Repository) CompileEffectiveRules(ctx context.Context, deviceID string) ([]string, string, error) {
	var groupIDs []string
	// Not _ =: a failure here would silently degrade the device to "no groups" and
	// drop every group policy aimed at it, which reads as a working system rather
	// than a failed query.
	if err := r.db.SelectContext(ctx, &groupIDs,
		`SELECT group_id FROM device_group_members WHERE device_id = ?`, deviceID); err != nil {
		return nil, "", fmt.Errorf("resolve device groups: %w", err)
	}

	// The pattern set has to be a stable, deterministic union, so the caller and the
	// device independently compute the same version hash from the same ordering.
	// Priority is the tiebreak an operator already sets in the UI, so it keeps
	// deciding the order.
	patterns := []string{}
	query := `
		SELECT r.pattern
		FROM filter_rules r
		JOIN filter_policies p ON p.id = r.policy_id
		WHERE p.is_enabled = 1
		  AND r.action = 'block'
		  AND (
		      p.target_type = 'all'
		      OR (p.target_type = 'device' AND p.target_id = ?)
		  )
		ORDER BY p.priority ASC, r.created_at ASC
	`
	if err := r.db.SelectContext(ctx, &patterns, query, deviceID); err != nil {
		return nil, "", fmt.Errorf("compile rules: %w", err)
	}

	// Group policies are a second query rather than an IN clause because sqlx.In
	// cannot bind an empty slice -- which is the common case, since most devices
	// are in no group at all.
	if len(groupIDs) > 0 {
		groupQuery, args, err := sqlx.In(`
			SELECT r.pattern
			FROM filter_rules r
			JOIN filter_policies p ON p.id = r.policy_id
			WHERE p.is_enabled = 1
			  AND r.action = 'block'
			  AND p.target_type = 'group' AND p.target_id IN (?)
			ORDER BY p.priority ASC, r.created_at ASC
		`, groupIDs)
		if err != nil {
			return nil, "", fmt.Errorf("build group query: %w", err)
		}
		var groupPatterns []string
		if err := r.db.SelectContext(ctx, &groupPatterns, groupQuery, args...); err != nil {
			return nil, "", fmt.Errorf("compile group rules: %w", err)
		}
		patterns = append(patterns, groupPatterns...)
	}

	// Deduplicate preserving order
	seen := make(map[string]bool)
	unique := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if !seen[p] {
			seen[p] = true
			unique = append(unique, p)
		}
	}

	// Compute a version hash of the effective rules
	h := sha256.New()
	for _, u := range unique {
		h.Write([]byte(u))
		h.Write([]byte{'\n'})
	}
	version := hex.EncodeToString(h.Sum(nil))[:16]

	return unique, version, nil
}

func (r *Repository) RecordDeviceFilterState(ctx context.Context, s *DeviceFilterState) error {
	now := time.Now().UTC()
	s.LastAppliedAt = now

	query := `
		INSERT INTO device_filter_states (
			device_id, policy_version, status, rules_applied, last_applied_at, error_message
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
			policy_version = excluded.policy_version,
			status = excluded.status,
			rules_applied = excluded.rules_applied,
			last_applied_at = excluded.last_applied_at,
			error_message = excluded.error_message
	`
	_, err := r.db.ExecContext(ctx, query,
		s.DeviceID, s.PolicyVersion, s.Status, s.RulesApplied, s.LastAppliedAt, s.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("record filter state: %w", err)
	}
	return nil
}

func (r *Repository) GetDeviceFilterState(ctx context.Context, deviceID string) (*DeviceFilterState, error) {
	var s DeviceFilterState
	query := `
		SELECT device_id, policy_version, status, rules_applied, last_applied_at, error_message
		FROM device_filter_states
		WHERE device_id = ?
	`
	if err := r.db.GetContext(ctx, &s, query, deviceID); err != nil {
		return nil, fmt.Errorf("get filter state: %w", err)
	}
	return &s, nil
}
