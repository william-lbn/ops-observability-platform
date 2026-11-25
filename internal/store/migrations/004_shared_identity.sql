ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS auth_method text NOT NULL DEFAULT 'oidc';
ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS credential_hash text NOT NULL DEFAULT '';
ALTER TABLE auth_sessions ADD CONSTRAINT auth_sessions_method CHECK(auth_method IN ('oidc','personal_credential'));
CREATE TABLE auth_login_states (
 hash text PRIMARY KEY CHECK(length(hash)=64),
 nonce text NOT NULL, verifier text NOT NULL,
 expires_at timestamptz NOT NULL
);
CREATE INDEX auth_login_states_expiry ON auth_login_states(expires_at);
