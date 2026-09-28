-- 0002_device_mgmt.sql
-- Phase 2: full inventory (hardware + software), static groups, device lifecycle.

-- Inventory snapshot. One row per device; JSON sections keep the schema stable
-- when a new fact is added, while the cached columns keep dashboard filters fast.
CREATE TABLE IF NOT EXISTS device_inventory (
  id               TEXT PRIMARY KEY,
  device_id        TEXT NOT NULL UNIQUE REFERENCES devices(id),
  -- Snapshot JSON: flexible, a new field is a struct change not an ALTER.
  hw               TEXT NOT NULL,    -- {"cpu":{...},"ram_bytes":...,"disks":[...],"nics":[...],"model":{...}}
  software         TEXT NOT NULL,    -- [{"name":...,"version":...,"publisher":...,"product_code":...}]
  os_detail        TEXT NOT NULL,    -- {"edition":...,"install_date":...,"last_boot_utc":...,"architecture":...}
  -- Cached columns for the fields the dashboard filters and sorts on most.
  hw_ram_bytes     INTEGER,
  hw_disk_free_pct REAL,
  hw_cpu_model     TEXT,
  collected_at     DATETIME NOT NULL,
  updated_at       DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_inventory_device    ON device_inventory(device_id);
CREATE INDEX IF NOT EXISTS idx_inventory_collected ON device_inventory(collected_at);

-- Static groups: deployment targets. Membership is manual in Phase 2; dynamic
-- rule-based groups are deferred until Software Deployment needs them.
CREATE TABLE IF NOT EXISTS device_groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  description TEXT,
  created_at  DATETIME NOT NULL,
  updated_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_groups_name ON device_groups(name);

CREATE TABLE IF NOT EXISTS device_group_members (
  group_id  TEXT NOT NULL REFERENCES device_groups(id),
  device_id TEXT NOT NULL REFERENCES devices(id),
  added_at  DATETIME NOT NULL,
  added_by  TEXT,
  PRIMARY KEY (group_id, device_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_device ON device_group_members(device_id);

-- Lifecycle. A retired device keeps its row so audit_logs.target_id stays
-- resolvable, but its device secret is cleared so the agent cannot reconnect
-- with the old credential.
ALTER TABLE devices ADD COLUMN retired_at   DATETIME;  -- NULL = active
ALTER TABLE devices ADD COLUMN capabilities TEXT;     -- JSON array, e.g. ["inventory.collect"]
