-- Admin console changes to tables outside the admin_* family. This file runs last in the migration because it alters the community and dictionary tables created by the files before it. Every statement is additive and idempotent.

-- Community moderation is post-moderation: new uploads are public at once with moderation='pending', and only 'removed' rows are hidden from the public endpoints. Rows that existed before moderation are approved. previous_moderation keeps the state a removal replaced, so restoring (or unbanning the owner) can put it back.
ALTER TABLE community_skins ADD COLUMN IF NOT EXISTS moderation text NOT NULL DEFAULT 'approved' CHECK(moderation IN ('pending','approved','removed'));
ALTER TABLE community_skins ADD COLUMN IF NOT EXISTS previous_moderation text CHECK(previous_moderation IN ('pending','approved'));
ALTER TABLE community_skins ADD COLUMN IF NOT EXISTS moderation_reason text CHECK(length(moderation_reason)<=500);
ALTER TABLE community_skins ADD COLUMN IF NOT EXISTS moderated_by text;
ALTER TABLE community_skins ADD COLUMN IF NOT EXISTS moderated_at timestamptz;
CREATE INDEX IF NOT EXISTS community_skins_pending ON community_skins(created_at) WHERE moderation='pending';

ALTER TABLE community_resources ADD COLUMN IF NOT EXISTS moderation text NOT NULL DEFAULT 'approved' CHECK(moderation IN ('pending','approved','removed'));
ALTER TABLE community_resources ADD COLUMN IF NOT EXISTS previous_moderation text CHECK(previous_moderation IN ('pending','approved'));
ALTER TABLE community_resources ADD COLUMN IF NOT EXISTS moderation_reason text CHECK(length(moderation_reason)<=500);
ALTER TABLE community_resources ADD COLUMN IF NOT EXISTS moderated_by text;
ALTER TABLE community_resources ADD COLUMN IF NOT EXISTS moderated_at timestamptz;
CREATE INDEX IF NOT EXISTS community_resources_pending ON community_resources(kind,created_at) WHERE moderation='pending';

ALTER TABLE community_candidate_skins ADD COLUMN IF NOT EXISTS moderation text NOT NULL DEFAULT 'approved' CHECK(moderation IN ('pending','approved','removed'));
ALTER TABLE community_candidate_skins ADD COLUMN IF NOT EXISTS previous_moderation text CHECK(previous_moderation IN ('pending','approved'));
ALTER TABLE community_candidate_skins ADD COLUMN IF NOT EXISTS moderation_reason text CHECK(length(moderation_reason)<=500);
ALTER TABLE community_candidate_skins ADD COLUMN IF NOT EXISTS moderated_by text;
ALTER TABLE community_candidate_skins ADD COLUMN IF NOT EXISTS moderated_at timestamptz;
CREATE INDEX IF NOT EXISTS community_candidate_skins_pending ON community_candidate_skins(created_at) WHERE moderation='pending';

ALTER TABLE community_plugins ADD COLUMN IF NOT EXISTS moderation text NOT NULL DEFAULT 'approved' CHECK(moderation IN ('pending','approved','removed'));
ALTER TABLE community_plugins ADD COLUMN IF NOT EXISTS previous_moderation text CHECK(previous_moderation IN ('pending','approved'));
ALTER TABLE community_plugins ADD COLUMN IF NOT EXISTS moderation_reason text CHECK(length(moderation_reason)<=500);
ALTER TABLE community_plugins ADD COLUMN IF NOT EXISTS moderated_by text;
ALTER TABLE community_plugins ADD COLUMN IF NOT EXISTS moderated_at timestamptz;
CREATE INDEX IF NOT EXISTS community_plugins_pending ON community_plugins(created_at) WHERE moderation='pending';

-- User reports on community content. kind uses the admin section names; item_id has no foreign key because it points into one of five tables.
CREATE TABLE IF NOT EXISTS community_reports (
 id bigserial PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('skins','candidate-skins','plugins','dictionaries','replies')),
 item_id text NOT NULL CHECK(length(item_id) BETWEEN 1 AND 128),
 reporter_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 64),
 detail text NOT NULL DEFAULT '' CHECK(length(detail)<=1000),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(kind,item_id,reporter_id)
);
CREATE INDEX IF NOT EXISTS community_reports_item ON community_reports(kind,item_id);

-- Account bans. A banned account cannot log in, refresh or use a session.
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS banned_at timestamptz;
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS ban_reason text CHECK(length(ban_reason)<=500);
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS banned_by text;
CREATE INDEX IF NOT EXISTS auth_users_banned ON auth_users(banned_at) WHERE banned_at IS NOT NULL;
-- The User-Agent a session was created with, truncated, so the admin can tell a user's devices apart.
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS user_agent text NOT NULL DEFAULT '' CHECK(length(user_agent)<=256);

-- Website dictionary submissions, recorded after the GitHub write succeeded, so the review page can show each submission's note and time next to the rolling pull request.
CREATE TABLE IF NOT EXISTS word_submissions (
 id bigserial PRIMARY KEY,
 pr_number integer NOT NULL CHECK(pr_number>0),
 kind text NOT NULL CHECK(kind IN ('words','english','translations')),
 entries jsonb NOT NULL,
 note text NOT NULL DEFAULT '' CHECK(length(note)<=1000),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS word_submissions_pr ON word_submissions(pr_number,created_at);
