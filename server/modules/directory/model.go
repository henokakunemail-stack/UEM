package directory

import "time"

// Contact is one person in the corporate directory, as this server stores them.
//
// It is NOT a console user. There is no password_hash and no role field, and
// login.go already refuses a user row with an empty hash — so a synced contact
// cannot log in even by accident, and adding this type changed nothing about
// authentication. Its whole purpose is to be the list a hardware asset's PIC is
// picked from.
type Contact struct {
	ID                string    `db:"id" json:"id"`
	ExternalID        string    `db:"external_id" json:"external_id"`
	DistinguishedName string    `db:"distinguished_name" json:"distinguished_name"`
	DisplayName       string    `db:"display_name" json:"display_name"`
	Email             string    `db:"email" json:"email"`
	Department        string    `db:"department" json:"department"`
	Title             string    `db:"title" json:"title"`
	Username          string    `db:"username" json:"username"`
	Source            string    `db:"source" json:"source"`
	IsActive          bool      `db:"is_active" json:"is_active"`
	FirstSeenAt       time.Time `db:"first_seen_at" json:"first_seen_at"`
	LastSyncedAt      time.Time `db:"last_synced_at" json:"last_synced_at"`
	UpdatedAt         time.Time `db:"updated_at" json:"updated_at"`
}

// Config is everything about the directory connection that is safe to store.
//
// There is no password field, and that is the design rather than an oversight:
// LDAPBindPassword is read from the environment and passed in separately, so
// the secret has exactly one home and that home is not the database.
type Config struct {
	ID           string    `db:"id" json:"-"`
	Host         string    `db:"host" json:"host"`
	Port         int       `db:"port" json:"port"`
	UseTLS       bool      `db:"use_tls" json:"use_tls"`
	BaseDN       string    `db:"base_dn" json:"base_dn"`
	BindDN       string    `db:"bind_dn" json:"bind_dn"`
	SearchFilter string    `db:"search_filter" json:"search_filter"`
	Source       string    `db:"source" json:"source"`
	UpdatedBy    string    `db:"updated_by" json:"-"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
	UpdatedAt    time.Time `db:"updated_at" json:"updated_at"`

	// BindPassword is env-sourced and never persisted. It is a field rather
	// than a parameter so the LDAP client can read config the way it reads
	// everything else, and it is tagged json:"-" so no accidental marshal can
	// put it in a response.
	BindPassword string `json:"-"`
}

// ConfigView is what the console receives. It answers "is a password set?"
// and never carries the value, the same way redactLicenseKey keeps a product
// key out of a viewer's hands.
type ConfigView struct {
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	UseTLS          bool      `json:"use_tls"`
	BaseDN          string    `json:"base_dn"`
	BindDN          string    `json:"bind_dn"`
	SearchFilter    string    `json:"search_filter"`
	Source          string    `json:"source"`
	BindPasswordSet bool      `json:"bind_password_configured"`
	UpdatedBy       string    `json:"updated_by"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// SyncPlan is what a diff produced. Preview returns it and writes nothing;
// apply recomputes it and writes. It is never stored, because a stored plan is
// a plan computed against a database that has since moved on.
type SyncPlan struct {
	Adds          []Contact `json:"adds"`
	Updates       []Contact `json:"updates"`
	Deactivations []Contact `json:"deactivations"`
	Unchanged     int       `json:"unchanged"`
	// TotalInDirectory is the number of entries the search returned, which is
	// not the same as adds+updates+unchanged: the search filter decides what a
	// "person" is, and entries it excludes belong to nobody on this list.
	TotalInDirectory int `json:"total_in_directory"`
}

// Empty reports whether applying this plan would change nothing. The console
// uses it to say "no changes" instead of showing three empty tables.
func (p SyncPlan) Empty() bool {
	return len(p.Adds) == 0 && len(p.Updates) == 0 && len(p.Deactivations) == 0
}
