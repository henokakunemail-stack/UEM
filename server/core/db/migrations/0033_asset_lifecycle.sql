-- Asset lifecycle: a category per hardware asset, and the admin's lifecycle
-- settings (useful life and replacement cost per category, the spec floor).
--
-- category is laptop | desktop | server | other, or empty. Empty means "take
-- it from the linked device's chassis type", which the agent reports; an
-- asset that is a monitor or a dock should be set to "other" so it never
-- borrows its computer's BIOS date as its own age.
ALTER TABLE hardware_assets ADD COLUMN category TEXT NOT NULL DEFAULT '';

-- One row, id 'default'. No row means the built-in defaults (laptop 4 years,
-- desktop 5, server 5, other 5, no replacement cost). settings_json is
-- validated by the server before it is written.
CREATE TABLE IF NOT EXISTS asset_lifecycle_settings (
    id             TEXT PRIMARY KEY,
    settings_json  TEXT NOT NULL,
    updated_by     TEXT NOT NULL DEFAULT '',
    updated_at     DATETIME NOT NULL
);
