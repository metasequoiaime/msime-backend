-- User-published plugin packs (plugin.toml plus audio files and notices, as a zip archive). id is the client's publication UUID. plugin_id is the manifest id and the install folder on clients. The archive is stored exactly as uploaded after validation and is never decoded or executed by the server; size and sha256 are generated, so they cannot disagree with the bytes download serves.
CREATE TABLE IF NOT EXISTS community_plugins (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
 owner_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 kind text NOT NULL CONSTRAINT community_plugins_kind_known CHECK(kind IN ('sound','music','command_table','effect')),
 plugin_id text NOT NULL CHECK(plugin_id ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
 name text NOT NULL,
 description text NOT NULL DEFAULT '',
 version text NOT NULL CHECK(length(version) BETWEEN 1 AND 32),
 license text NOT NULL CHECK(length(license) BETWEEN 1 AND 64),
 manifest bytea NOT NULL CHECK(octet_length(manifest) BETWEEN 1 AND 262144),
 archive bytea NOT NULL CHECK(octet_length(archive) BETWEEN 1 AND 8388608),
 size integer GENERATED ALWAYS AS (octet_length(archive)) STORED,
 sha256 text GENERATED ALWAYS AS (encode(sha256(archive),'hex')) STORED,
 -- sha256 over the canonical upload (name, description, kind, plugin_id, version, then the archive digest), so an identical retry is recognised.
 request_sha256 text NOT NULL CHECK(request_sha256 ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT now()
);
-- Tables created before effect packs carry the column's auto-named check, which lacks 'effect'; replace it with the named one.
ALTER TABLE community_plugins DROP CONSTRAINT IF EXISTS community_plugins_kind_check;
DO $$
BEGIN
 IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='community_plugins'::regclass AND conname='community_plugins_kind_known') THEN
  ALTER TABLE community_plugins ADD CONSTRAINT community_plugins_kind_known CHECK(kind IN ('sound','music','command_table','effect'));
 END IF;
END $$;
CREATE INDEX IF NOT EXISTS community_plugins_newest ON community_plugins(created_at DESC,id);
CREATE INDEX IF NOT EXISTS community_plugins_kind_newest ON community_plugins(kind,created_at DESC,id);
CREATE INDEX IF NOT EXISTS community_plugins_owner ON community_plugins(owner_id,created_at DESC);
CREATE TABLE IF NOT EXISTS community_plugin_downloads (
 pack_id text NOT NULL REFERENCES community_plugins(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 PRIMARY KEY(pack_id,user_id)
);
CREATE TABLE IF NOT EXISTS community_plugin_ratings (
 pack_id text NOT NULL REFERENCES community_plugins(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 stars integer NOT NULL CHECK(stars BETWEEN 1 AND 5),
 PRIMARY KEY(pack_id,user_id)
);
