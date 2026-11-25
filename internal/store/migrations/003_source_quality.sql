CREATE TABLE IF NOT EXISTS source_quality (
 tenant text NOT NULL, source text NOT NULL,
 state text NOT NULL, last_attempt_at timestamptz NOT NULL DEFAULT now(),
 last_success_at timestamptz, error_code text NOT NULL DEFAULT '',
 expected_set jsonb NOT NULL DEFAULT '[]', observed_set jsonb NOT NULL DEFAULT '[]',
 PRIMARY KEY(tenant,source)
);
ALTER TABLE source_quality ENABLE ROW LEVEL SECURITY;
ALTER TABLE source_quality FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_scope ON source_quality USING (tenant=current_setting('app.tenant',true)) WITH CHECK (tenant=current_setting('app.tenant',true));
