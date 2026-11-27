-- Planned synthetic-pilot credential rotation. Drain API/workers first.
-- Do not infer ownership from an existing password, session, or API key.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
ALTER TABLE users ADD COLUMN credential_version bigint NOT NULL DEFAULT 0;
UPDATE sessions SET revoked_at=now() WHERE revoked_at IS NULL;
UPDATE api_keys SET revoked_at=now() WHERE revoked_at IS NULL;

CREATE TABLE auth_mail_budgets (
  key text PRIMARY KEY,
  hour_started timestamptz NOT NULL DEFAULT now(), hour_count int NOT NULL DEFAULT 0,
  day_started timestamptz NOT NULL DEFAULT now(), day_count int NOT NULL DEFAULT 0,
  last_at timestamptz
);
INSERT INTO auth_mail_budgets(key) VALUES ('global'),('attempts');

CREATE TABLE auth_challenges (
  id uuid PRIMARY KEY,
  purpose text NOT NULL CHECK (purpose IN ('signup','reset')),
  locale text NOT NULL DEFAULT 'en' CHECK (locale IN ('en','sv')),
  token_hash bytea NOT NULL CHECK (octet_length(token_hash)=32),
  email_hash bytea NOT NULL CHECK (octet_length(email_hash)=32),
  email_enc bytea NOT NULL,
  user_id uuid REFERENCES users(id) ON DELETE CASCADE,
  credential_version bigint,
  ready_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  invalidated_at timestamptz,
  CHECK (expires_at > created_at AND expires_at <= created_at + interval '31 minutes')
);
CREATE INDEX auth_challenge_email_idx ON auth_challenges(email_hash);
CREATE INDEX auth_challenge_expiry_idx ON auth_challenges(expires_at);
CREATE INDEX auth_challenge_material_idx ON auth_challenges(expires_at) WHERE email_enc<>''::bytea;
CREATE INDEX auth_challenge_retention_idx ON auth_challenges(created_at);
CREATE INDEX auth_budget_retention_idx ON auth_mail_budgets(last_at);

CREATE TABLE auth_mail_outbox (
  id uuid PRIMARY KEY REFERENCES auth_challenges(id) ON DELETE CASCADE,
  payload_enc bytea,
  state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','sending','sent','unknown','failed','canceled')),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  attempt_id uuid,
  attempts int NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 3),
  provider_message_id text,
  result_code text,
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX auth_mail_pending_idx ON auth_mail_outbox(id) WHERE state IN ('queued','sending');
CREATE INDEX auth_mail_sending_idx ON auth_mail_outbox(lease_until) WHERE state='sending';
CREATE INDEX auth_mail_ready_idx ON auth_mail_outbox(available_at) WHERE state='queued';
CREATE TABLE auth_mail_suppressions (
  email_hash bytea PRIMARY KEY,
  reason text NOT NULL CHECK (reason IN ('bounce','complaint')),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz
);

CREATE TABLE legal_receipts (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  manifest_version text NOT NULL,
  documents jsonb NOT NULL,
  document_origin text NOT NULL,
  manifest_adopted boolean NOT NULL,
  accepted_at timestamptz NOT NULL DEFAULT now(),
  ip inet,
  user_agent text NOT NULL CHECK (length(user_agent)<=300),
  PRIMARY KEY(user_id,manifest_version)
);
ALTER TABLE legal_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY legal_user ON legal_receipts TO hedvi_api USING (user_id=hedvi_user()) WITH CHECK (user_id=hedvi_user());
CREATE POLICY legal_worker ON legal_receipts TO hedvi_worker USING (true) WITH CHECK (true);

-- Authentication state is intentionally service-wide, like users/sessions;
-- it contains no tenant mail and exposes no unauthenticated HTTP listing.
REVOKE ALL ON auth_challenges,auth_mail_outbox,auth_mail_budgets,auth_mail_suppressions,legal_receipts FROM PUBLIC;
GRANT SELECT,INSERT,UPDATE ON auth_challenges,auth_mail_budgets TO hedvi_api;
GRANT INSERT ON auth_mail_outbox TO hedvi_api;
GRANT SELECT,INSERT ON legal_receipts TO hedvi_api;
GRANT SELECT,INSERT,UPDATE,DELETE ON auth_challenges,auth_mail_outbox,auth_mail_budgets,auth_mail_suppressions TO hedvi_worker;
GRANT SELECT,INSERT ON legal_receipts TO hedvi_worker;

-- Counts only physical pending jobs, even when their proof has expired and
-- workers are down. API gets no recipient/payload/outcome read privilege.
CREATE FUNCTION hedvi_auth_pending_count() RETURNS bigint LANGUAGE sql STABLE
SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT count(*) FROM (SELECT id FROM public.auth_mail_outbox WHERE state IN ('queued','sending') LIMIT 1000) jobs
$$;
REVOKE ALL ON FUNCTION hedvi_auth_pending_count() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_auth_pending_count() TO hedvi_api,hedvi_worker;
