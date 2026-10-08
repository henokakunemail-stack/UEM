-- server/core/db/migrations/0021_agent_update_signing.sql
-- P0.4: signed update manifests.
--
-- A release used to be authenticated by its SHA-256 alone. That is an
-- integrity check, not an authenticity one: it proves the bytes on disk are
-- the bytes the uploader recorded, and nothing more. Anybody who can reach
-- the upload route (an admin, or whoever compromised one) can ship a binary
-- of their choosing to every agent in the fleet, and the SHA-256 would
-- faithfully confirm that the file they shipped is the file they shipped.
--
-- Ed25519 makes the manifest authentic. The release row now carries a
-- signature over a canonical manifest (version/os/arch/sha256/size/url/
-- published_at/minimum_supported_version), produced by a private key that
-- lives only on the signing host, never on the server. The agent verifies
-- with the public key before it swaps its own executable.
--
-- minimum_supported_version is the downgrade floor: an agent below it refuses
-- the install, so a rollback on a device that has already migrated forward
-- cannot silently re-introduce a version the fleet no longer supports.
--
-- All four columns are nullable/blank-safe because releases uploaded before
-- this migration have none of them, and an agent that receives a blank
-- signature treats it as unsigned rather than failing the update outright --
-- see the agent's verify path for why that is deliberate and temporary.

ALTER TABLE agent_releases ADD COLUMN published_at DATETIME;
ALTER TABLE agent_releases ADD COLUMN minimum_supported_version TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_releases ADD COLUMN ed25519_signature TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_releases ADD COLUMN download_url TEXT NOT NULL DEFAULT '';

-- Backfill: a release that predates signing was published when it was
-- uploaded, and downloads from the server-relative route the handler always
-- built. A blank signature keeps it installable by agents that have not yet
-- been upgraded to verify.
UPDATE agent_releases SET published_at = created_at WHERE published_at IS NULL;
