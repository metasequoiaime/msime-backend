CREATE TABLE IF NOT EXISTS admin_events (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK(kind IN ('download','crash')),
 platform text NOT NULL,
 version text NOT NULL,
 message text NOT NULL DEFAULT '',
 stack text NOT NULL DEFAULT '',
 resolved boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS admin_events_kind_time ON admin_events(kind,created_at DESC,id);
CREATE TABLE IF NOT EXISTS admin_audit (
 id bigserial PRIMARY KEY,
 action text NOT NULL,
 target text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS auth_users_created ON auth_users(created_at);

CREATE TABLE IF NOT EXISTS admin_login_flows (
 state_hash text PRIMARY KEY, nonce text NOT NULL, verifier text NOT NULL,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '10 minutes'
);
CREATE INDEX IF NOT EXISTS admin_login_flows_expiry ON admin_login_flows(expires_at);
CREATE TABLE IF NOT EXISTS admin_sessions (
 token_hash text PRIMARY KEY, subject text NOT NULL, email text NOT NULL,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '8 hours'
);
CREATE INDEX IF NOT EXISTS admin_sessions_expiry ON admin_sessions(expires_at);
ALTER TABLE admin_audit ADD COLUMN IF NOT EXISTS actor text NOT NULL DEFAULT 'legacy-token';

CREATE TABLE IF NOT EXISTS admin_members (
 email text PRIMARY KEY CHECK(email=lower(email)),
 enabled boolean NOT NULL DEFAULT true,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);

-- Website settings edited in the admin console and read publicly by the official site (for example the Lanzou mirror link on the download page). Clearing a setting keeps the row with an empty value so the last editor stays visible.
CREATE TABLE IF NOT EXISTS site_settings (
 key text PRIMARY KEY,
 value text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 updated_by text NOT NULL
);
