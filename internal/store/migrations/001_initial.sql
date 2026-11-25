CREATE TABLE IF NOT EXISTS schema_migrations(version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS events (
 tenant text NOT NULL, source text NOT NULL, id uuid NOT NULL,
 digest text NOT NULL CHECK(length(digest)=64), raw bytea NOT NULL,
 observed_at timestamptz NOT NULL, received_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,source,id)
);
CREATE INDEX IF NOT EXISTS events_timeline ON events(tenant,received_at DESC,id);
CREATE TABLE IF NOT EXISTS attachments (
 tenant text NOT NULL, digest text NOT NULL, bytes bytea NOT NULL,
 content_type text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,digest), CHECK(octet_length(bytes)<=1048576)
);
CREATE TABLE IF NOT EXISTS audits (
 sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant text NOT NULL, actor text NOT NULL, action text NOT NULL,
 object_id text NOT NULL, request_id text NOT NULL,
 detail jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audits_timeline ON audits(tenant,sequence DESC);
CREATE TABLE IF NOT EXISTS outbox (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 tenant text NOT NULL, audit_sequence bigint NOT NULL REFERENCES audits(sequence),
 payload jsonb NOT NULL, delivered_at timestamptz, attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS incidents (
 tenant text NOT NULL, fingerprint text NOT NULL, title text NOT NULL,
 state text NOT NULL CHECK(state IN ('firing','acknowledged','resolved')),
 labels jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,fingerprint)
);
CREATE TABLE IF NOT EXISTS projections (
 tenant text NOT NULL, audit_sequence bigint NOT NULL UNIQUE,
 payload jsonb NOT NULL, projected_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS catalog (
 tenant text NOT NULL, source text NOT NULL, uid text NOT NULL,
 namespace text NOT NULL, name text NOT NULL, kind text NOT NULL,
 snapshot jsonb NOT NULL, observed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant,source,uid)
);
DO $$ DECLARE t text; BEGIN
 FOR t IN SELECT unnest(ARRAY['events','attachments','audits','outbox','incidents','projections','catalog']) LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',t);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',t);
  IF NOT EXISTS(SELECT 1 FROM pg_policies WHERE schemaname=current_schema() AND tablename=t AND policyname='tenant_scope') THEN
   EXECUTE format('CREATE POLICY tenant_scope ON %I USING (tenant=current_setting(''app.tenant'',true)) WITH CHECK (tenant=current_setting(''app.tenant'',true))',t);
  END IF;
 END LOOP;
END $$;
INSERT INTO schema_migrations(version) VALUES(1) ON CONFLICT DO NOTHING;
