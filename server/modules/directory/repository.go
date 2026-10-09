package directory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

type Repository struct {
	db *sqlx.DB
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ListContacts returns active contacts by name. Inactive ones are excluded on
// purpose: the asset form's PIC dropdown should offer people who can still be
// handed a machine, while an existing asset whose holder has left keeps
// rendering that name because assigned_user is a stored string, not a join.
func (r *Repository) ListContacts(ctx context.Context) ([]*Contact, error) {
	list := []*Contact{}
	err := r.db.SelectContext(ctx, &list, `
		SELECT id, external_id, distinguished_name, display_name, email,
		       department, title, username, source, is_active,
		       first_seen_at, last_synced_at, updated_at
		FROM directory_contacts
		WHERE is_active = 1
		ORDER BY display_name COLLATE NOCASE
	`)
	if err != nil {
		return nil, fmt.Errorf("list directory contacts: %w", err)
	}
	return list, nil
}

// AllContacts returns active and inactive alike. Only the diff uses it: a
// contact that has already been deactivated must be visible to the diff so it
// is not reported as a fresh deactivation on every single run.
func (r *Repository) AllContacts(ctx context.Context) ([]Contact, error) {
	list := []Contact{}
	err := r.db.SelectContext(ctx, &list, `
		SELECT id, external_id, distinguished_name, display_name, email,
		       department, title, username, source, is_active,
		       first_seen_at, last_synced_at, updated_at
		FROM directory_contacts
	`)
	if err != nil {
		return nil, fmt.Errorf("list all directory contacts: %w", err)
	}
	return list, nil
}

func (r *Repository) GetContact(ctx context.Context, externalID string) (*Contact, error) {
	var c Contact
	err := r.db.GetContext(ctx, &c, `
		SELECT id, external_id, distinguished_name, display_name, email,
		       department, title, username, source, is_active,
		       first_seen_at, last_synced_at, updated_at
		FROM directory_contacts
		WHERE external_id = ?
	`, externalID)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ApplyPlan writes a plan in one transaction.
//
// One transaction rather than per-row writes on purpose: a sync that inserts
// 300 contacts and fails on the 301st must not leave 300 behind for the next
// run to trip over. The upsert form is the pattern already used elsewhere in
// this repo (networkfilter/repository.go, patch-management/repository.go).
//
// is_active is bound as a literal 1 in the VALUES list above, not as a
// parameter, so it needs no separate handling in the DO UPDATE clause — which
// is the kind of thing that gets forgotten when someone adds a column later.
// Reactivation is handled upstream: diff.go classifies a contact that is
// present in the directory but inactive here as an Update and stamps
// IsActive = true, so it lands in plan.Updates.
//
// The COALESCE in the SET clause is belt-and-braces, not the mechanism. Because
// VALUES binds 1, excluded.is_active is never NULL and the first branch always
// wins; the second branch only becomes reachable if a future caller binds a
// NULL there, which is the case it is actually guarding.
func (r *Repository) ApplyPlan(ctx context.Context, plan SyncPlan) error {
	now := time.Now().UTC()

	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin directory sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	upsert := `
		INSERT INTO directory_contacts (
			id, external_id, distinguished_name, display_name, email,
			department, title, username, source, is_active,
			first_seen_at, last_synced_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
		ON CONFLICT(external_id) DO UPDATE SET
			distinguished_name = excluded.distinguished_name,
			display_name       = excluded.display_name,
			email              = excluded.email,
			department         = excluded.department,
			title              = excluded.title,
			username           = excluded.username,
			source             = excluded.source,
			is_active          = COALESCE(excluded.is_active, directory_contacts.is_active),
			last_synced_at     = excluded.last_synced_at,
			updated_at         = excluded.updated_at
	`

	for _, c := range plan.Adds {
		if c.ID == "" {
			c.ID = newID()
		}
		if _, err := tx.ExecContext(ctx, upsert,
			c.ID, c.ExternalID, c.DistinguishedName, c.DisplayName, c.Email,
			c.Department, c.Title, c.Username, c.Source,
			now, now, now,
		); err != nil {
			return fmt.Errorf("insert directory contact %s: %w", c.ExternalID, err)
		}
	}

	for _, c := range plan.Updates {
		if _, err := tx.ExecContext(ctx, upsert,
			c.ID, c.ExternalID, c.DistinguishedName, c.DisplayName, c.Email,
			c.Department, c.Title, c.Username, c.Source,
			c.FirstSeenAt, now, now,
		); err != nil {
			return fmt.Errorf("update directory contact %s: %w", c.ExternalID, err)
		}
	}

	for _, c := range plan.Deactivations {
		if _, err := tx.ExecContext(ctx, `
			UPDATE directory_contacts SET is_active = 0, last_synced_at = ?, updated_at = ?
			WHERE external_id = ?
		`, now, now, c.ExternalID); err != nil {
			return fmt.Errorf("deactivate directory contact %s: %w", c.ExternalID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit directory sync: %w", err)
	}
	return nil
}

// GetConfig reads the single config row. The second return is false when none
// has been saved, which is the normal first-run state and not an error.
func (r *Repository) GetConfig(ctx context.Context) (*Config, bool, error) {
	var c Config
	err := r.db.GetContext(ctx, &c, `
		SELECT id, host, port, use_tls, base_dn, bind_dn, search_filter,
		       source, updated_by, created_at, updated_at
		FROM directory_sync_config
		ORDER BY created_at ASC
		LIMIT 1
	`)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("get directory config: %w", err)
	}
	return &c, true, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertConfig writes the config, keeping the row's id and created_at. There is
// exactly one config by design — a second directory is a different server, not
// a second setting on this one.
func (r *Repository) UpsertConfig(ctx context.Context, c *Config) error {
	now := time.Now().UTC()
	existing, found, err := r.GetConfig(ctx)
	if err != nil {
		return err
	}

	if found {
		c.ID = existing.ID
		c.CreatedAt = existing.CreatedAt
	} else {
		c.ID = newID()
		c.CreatedAt = now
	}
	c.UpdatedAt = now

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO directory_sync_config (
			id, host, port, use_tls, base_dn, bind_dn, search_filter,
			source, updated_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			host = excluded.host, port = excluded.port,
			use_tls = excluded.use_tls, base_dn = excluded.base_dn,
			bind_dn = excluded.bind_dn, search_filter = excluded.search_filter,
			source = excluded.source, updated_by = excluded.updated_by,
			updated_at = excluded.updated_at
	`,
		c.ID, c.Host, c.Port, boolToInt(c.UseTLS), c.BaseDN, c.BindDN, c.SearchFilter,
		c.Source, c.UpdatedBy, c.CreatedAt, c.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("save directory config: %w", err)
	}
	return nil
}
