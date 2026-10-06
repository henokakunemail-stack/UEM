-- Directory (LDAP/AD) sync, Phase 16.
--
-- A contact here is NOT a console account. It exists so an operator can pick a
-- person by name instead of typing one free-text, and nothing more: synced
-- contacts get no password_hash, so login.go refuses them like any unknown
-- user. Local auth is untouched by this migration.

CREATE TABLE IF NOT EXISTS directory_contacts (
    id TEXT PRIMARY KEY,
    -- objectGUID / entryUUID, falling back to the DN only when the directory
    -- offers neither. Chosen in that order because a DN changes every time
    -- somebody is moved between OUs, and matching on it would silently
    -- re-create the same person as a second contact on the next sync.
    external_id TEXT NOT NULL UNIQUE,
    distinguished_name TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    department TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT 'ldap',
    -- Distinguishes "in the directory but disabled" from "no longer in the
    -- directory". They want different UI and different sync actions, and
    -- neither is a deletion: an asset's history still points at the person
    -- who used to hold the machine, so the row has to outlive the person.
    is_active INTEGER NOT NULL DEFAULT 1,
    first_seen_at DATETIME NOT NULL,
    last_synced_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_directory_contacts_name ON directory_contacts(display_name);
CREATE INDEX IF NOT EXISTS idx_directory_contacts_dept ON directory_contacts(department);

-- Deliberately NO password column. This repo has no encryption at rest and no
-- precedent for storing a secret: the only credential it holds is in an env
-- var. A bind password in this table would mean a stray copy of the .db is a
-- copy of the company directory, so LDAP_BIND_PASSWORD is read from the
-- environment instead and the console only ever learns whether it is set.
CREATE TABLE IF NOT EXISTS directory_sync_config (
    id TEXT PRIMARY KEY,
    host TEXT NOT NULL DEFAULT '',
    port INTEGER NOT NULL DEFAULT 636,
    -- 0 means plain LDAP on the given port; 1 means StartTLS on 389 or LDAPS on
    -- 636. Both are startTLS=false/straight-TLS apart, and the port is what
    -- tells them apart, so it is stored rather than guessed.
    use_tls INTEGER NOT NULL DEFAULT 1,
    base_dn TEXT NOT NULL DEFAULT '',
    bind_dn TEXT NOT NULL DEFAULT '',
    search_filter TEXT NOT NULL DEFAULT '(objectClass=person)',
    source TEXT NOT NULL DEFAULT 'ldap',
    updated_by TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);