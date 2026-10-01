-- 用户身份仅按提供方及其稳定标识关联，禁止按同名邮箱自动合并。
CREATE TABLE IF NOT EXISTS auth_users (
 id text PRIMARY KEY, display_name text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS auth_identities (
 provider text NOT NULL, subject text NOT NULL, user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(provider, subject)
);
CREATE INDEX IF NOT EXISTS auth_identities_user ON auth_identities(user_id);
CREATE TABLE IF NOT EXISTS auth_challenges (
 id_hash text PRIMARY KEY, provider text NOT NULL, subject text NOT NULL DEFAULT '', nonce text NOT NULL DEFAULT '',
 code_hash text NOT NULL DEFAULT '', link_user text REFERENCES auth_users(id) ON DELETE CASCADE,
 attempts integer NOT NULL DEFAULT 0, expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_challenges_expiry ON auth_challenges(expires_at);
CREATE TABLE IF NOT EXISTS auth_sessions (
 id text PRIMARY KEY, user_id text NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
 access_hash text NOT NULL UNIQUE, refresh_hash text NOT NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(), access_expires timestamptz NOT NULL,
 expires_at timestamptz NOT NULL, revoked boolean NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS auth_sessions_user ON auth_sessions(user_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);
CREATE TABLE IF NOT EXISTS auth_used_refresh (
 hash text PRIMARY KEY, session_id text NOT NULL REFERENCES auth_sessions(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS auth_rates (
 key text PRIMARY KEY, count integer NOT NULL, expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_rates_expiry ON auth_rates(expires_at);
-- Google profile claims and the server-exchange flow (additive: old binaries ignore these columns and the defaults keep their inserts valid).
ALTER TABLE auth_identities ADD COLUMN IF NOT EXISTS email text NOT NULL DEFAULT '';
ALTER TABLE auth_identities ADD COLUMN IF NOT EXISTS email_verified boolean NOT NULL DEFAULT false;
ALTER TABLE auth_identities ADD COLUMN IF NOT EXISTS name text NOT NULL DEFAULT '';
ALTER TABLE auth_identities ADD COLUMN IF NOT EXISTS picture text NOT NULL DEFAULT '';
ALTER TABLE auth_identities ADD COLUMN IF NOT EXISTS updated_at timestamptz;
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS code_verifier text NOT NULL DEFAULT '';
-- Object key of the user's uploaded avatar in the avatar bucket; '' when they have none.
ALTER TABLE auth_users ADD COLUMN IF NOT EXISTS avatar_key text NOT NULL DEFAULT '';
ALTER TABLE auth_challenges ADD COLUMN IF NOT EXISTS redirect_uri text NOT NULL DEFAULT '';
-- refresh_token is AES-256-GCM nonce||ciphertext with AAD provider:subject; the key never reaches the database.
CREATE TABLE IF NOT EXISTS auth_provider_tokens (
 provider text NOT NULL, subject text NOT NULL, refresh_token bytea NOT NULL, scope text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(provider, subject),
 FOREIGN KEY(provider, subject) REFERENCES auth_identities(provider, subject) ON DELETE CASCADE
);
