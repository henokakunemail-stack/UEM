-- server/core/db/migrations/0013_asset_license_management.sql
-- Phase 14: Asset & License Management

CREATE TABLE IF NOT EXISTS hardware_assets (
    id TEXT PRIMARY KEY,
    asset_tag TEXT NOT NULL UNIQUE,
    device_id TEXT REFERENCES devices(id) ON DELETE SET NULL,
    model_name TEXT NOT NULL,
    serial_number TEXT NOT NULL DEFAULT '',
    vendor TEXT NOT NULL DEFAULT '',
    site TEXT NOT NULL DEFAULT '',
    department TEXT NOT NULL DEFAULT '',
    assigned_user TEXT NOT NULL DEFAULT '',
    purchase_date DATETIME,
    purchase_cost REAL NOT NULL DEFAULT 0.0,
    warranty_expires_at DATETIME,
    status TEXT NOT NULL DEFAULT 'in_use', -- 'in_use', 'in_stock', 'in_repair', 'retired', 'disposed'
    notes TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS software_licenses (
    id TEXT PRIMARY KEY,
    software_name TEXT NOT NULL,
    publisher TEXT NOT NULL DEFAULT '',
    license_key TEXT NOT NULL DEFAULT '',
    license_type TEXT NOT NULL DEFAULT 'per_device', -- 'per_device', 'per_user', 'site_license', 'subscription'
    total_seats INTEGER NOT NULL DEFAULT 1,
    cost REAL NOT NULL DEFAULT 0.0,
    purchased_at DATETIME,
    expires_at DATETIME,
    notes TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS license_allocations (
    id TEXT PRIMARY KEY,
    license_id TEXT NOT NULL REFERENCES software_licenses(id) ON DELETE CASCADE,
    device_id TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    allocated_at DATETIME NOT NULL,
    allocated_by TEXT NOT NULL,
    UNIQUE(license_id, device_id)
);

CREATE INDEX IF NOT EXISTS idx_hardware_assets_dev ON hardware_assets(device_id);
CREATE INDEX IF NOT EXISTS idx_hardware_assets_status ON hardware_assets(status);
CREATE INDEX IF NOT EXISTS idx_software_licenses_name ON software_licenses(software_name);
