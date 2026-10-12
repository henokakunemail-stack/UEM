-- Migration 0023: Network Filter Mode (Blacklist vs Whitelist)
-- Adds mode column to filter_policies ('blacklist' | 'whitelist').
-- Default is 'blacklist' preserving legacy behavior.

ALTER TABLE filter_policies ADD COLUMN mode TEXT NOT NULL DEFAULT 'blacklist';
