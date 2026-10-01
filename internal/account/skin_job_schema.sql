-- AI skin artwork jobs (POST/GET/DELETE /v1/skins/jobs). The replica that accepts a job runs the upstream call and writes its state and result here, so a poll or delete that lands on another replica sees the same job. Rows are short-lived drafts: they expire after ten minutes and Prune deletes them. owner is the authenticated principal ("user:<id>" or a configured client id), not a foreign key, because configured clients have no auth_users row. artwork is the bounded JSON body the synchronous endpoint would have returned (at most about 12 MB), kept only for succeeded jobs. cancelled is set by a DELETE while the job is still running: the row stays until the worker that owns it notices the flag on its next heartbeat, stops the upstream call and deletes the row, so the job still counts against the caps until its upstream call has really ended. heartbeat_at is refreshed by that worker while it runs; a running row whose heartbeat is older than the stale limit belongs to a replica that died and is reported as failed.
CREATE TABLE IF NOT EXISTS skin_jobs (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{48}$'),
 owner text NOT NULL CHECK(length(owner) BETWEEN 1 AND 200),
 state text NOT NULL DEFAULT 'running' CHECK(state IN ('running','succeeded','failed')),
 reason text NOT NULL DEFAULT '' CHECK(length(reason)<=200),
 artwork bytea,
 cancelled boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 heartbeat_at timestamptz NOT NULL DEFAULT now(),
 expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS skin_jobs_expires ON skin_jobs(expires_at);
