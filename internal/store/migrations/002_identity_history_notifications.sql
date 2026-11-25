CREATE TABLE IF NOT EXISTS auth_sessions (
 hash text PRIMARY KEY CHECK(length(hash)=64),
 subject text NOT NULL, expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS auth_sessions_expiry ON auth_sessions(expires_at);
CREATE TABLE IF NOT EXISTS catalog_history (
 tenant text NOT NULL, source text NOT NULL, uid text NOT NULL,
 snapshot jsonb NOT NULL, observed_at timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,source,uid,observed_at)
);
ALTER TABLE catalog_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog_history FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON catalog_history USING (tenant=current_setting('app.tenant',true)) WITH CHECK (tenant=current_setting('app.tenant',true));
CREATE TABLE IF NOT EXISTS notifications (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant text NOT NULL, fingerprint text NOT NULL, transition_at timestamptz NOT NULL,
 title text NOT NULL, body text NOT NULL, read_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant,fingerprint,transition_at)
);
ALTER TABLE notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON notifications USING (tenant=current_setting('app.tenant',true)) WITH CHECK (tenant=current_setting('app.tenant',true));
