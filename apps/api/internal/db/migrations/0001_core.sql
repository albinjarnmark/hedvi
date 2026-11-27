-- Hedvi core schema.
--
-- Invariants enforced here (see docs/architecture/acceptance.md):
--  * An email is accepted only when its row, recipients, quota reservation,
--    outbox job and idempotency record commit in one transaction.
--  * Test-mode emails can never get a send outbox job or a send attempt.
--  * Every tenant table carries org_id and is protected by row level security
--    for the API role. Workers get explicit, separate policies.

-- Login roles are created by the bootstrap runbook. They are created here as
-- NOLOGIN placeholders when missing so that migrations are self-contained.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'hedvi_api') THEN
    CREATE ROLE hedvi_api NOLOGIN;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'hedvi_worker') THEN
    CREATE ROLE hedvi_worker NOLOGIN;
  END IF;
END
$$;

CREATE FUNCTION hedvi_org() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT nullif(current_setting('hedvi.org_id', true), '')::uuid
$$;

CREATE FUNCTION hedvi_user() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT nullif(current_setting('hedvi.user_id', true), '')::uuid
$$;

CREATE FUNCTION touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END
$$;

-- ---------------------------------------------------------------------------
-- Accounts
-- ---------------------------------------------------------------------------

CREATE TABLE users (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  email            text NOT NULL UNIQUE CHECK (email = lower(email)),
  name             text NOT NULL DEFAULT '',
  password_hash    text NOT NULL,
  totp_secret_enc  bytea,
  totp_enabled_at  timestamptz,
  totp_last_step   bigint NOT NULL DEFAULT 0,
  failed_logins    int NOT NULL DEFAULT 0,
  locked_until     timestamptz,
  is_staff         boolean NOT NULL DEFAULT false,
  locale           text NOT NULL DEFAULT 'en' CHECK (locale IN ('en', 'sv')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER users_touch BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE sessions (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash       bytea NOT NULL UNIQUE,
  mfa_verified_at  timestamptz,
  ip               inet,
  user_agent       text NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now(),
  last_seen_at     timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  revoked_at       timestamptz
);
CREATE INDEX sessions_user_idx ON sessions (user_id);

CREATE TABLE organizations (
  id                       uuid PRIMARY KEY DEFAULT uuidv7(),
  name                     text NOT NULL,
  plan                     text NOT NULL DEFAULT 'free'
                           CHECK (plan IN ('free', 'flex', 'pro_50k', 'pro_100k', 'custom')),
  currency                 text NOT NULL DEFAULT 'USD' CHECK (currency IN ('USD', 'SEK')),
  risk_status              text NOT NULL DEFAULT 'new'
                           CHECK (risk_status IN ('new', 'approved', 'restricted', 'paused', 'blocked')),
  -- Retention. NULL means plan default (see internal/plans).
  metadata_retention_days  int CHECK (metadata_retention_days BETWEEN 1 AND 90),
  content_retention_days   int CHECK (content_retention_days BETWEEN 0 AND 7),
  -- Cost control. Everything that can cost more is off by default.
  use_credits_for_overage  boolean NOT NULL DEFAULT false,
  overage_enabled          boolean NOT NULL DEFAULT false,
  overage_cap_recipients   bigint NOT NULL DEFAULT 0 CHECK (overage_cap_recipients >= 0),
  created_at               timestamptz NOT NULL DEFAULT now(),
  updated_at               timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER organizations_touch BEFORE UPDATE ON organizations FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE memberships (
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role        text NOT NULL CHECK (role IN ('owner', 'admin', 'developer', 'billing')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships (user_id);

CREATE TABLE projects (
  id                        uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id                    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name                      text NOT NULL,
  ses_tenant_name           text NOT NULL UNIQUE,
  sending_status            text NOT NULL DEFAULT 'test_only'
                            CHECK (sending_status IN ('test_only', 'pending_review', 'approved', 'paused')),
  review_request            jsonb,
  status_reason             text NOT NULL DEFAULT '',
  status_changed_at         timestamptz NOT NULL DEFAULT now(),
  hourly_limit              int NOT NULL DEFAULT 25 CHECK (hourly_limit >= 0),
  daily_limit               int NOT NULL DEFAULT 100 CHECK (daily_limit >= 0),
  test_daily_limit          int NOT NULL DEFAULT 500 CHECK (test_daily_limit >= 0),
  test_storage_limit_bytes  bigint NOT NULL DEFAULT 52428800 CHECK (test_storage_limit_bytes >= 0),
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id)
);
CREATE INDEX projects_org_idx ON projects (org_id);
CREATE TRIGGER projects_touch BEFORE UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE domains (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id               uuid NOT NULL,
  project_id           uuid NOT NULL,
  name                 text NOT NULL CHECK (name = lower(name)),
  region               text NOT NULL DEFAULT 'eu-north-1',
  status               text NOT NULL DEFAULT 'not_started'
                       CHECK (status IN ('not_started', 'pending', 'verified', 'partially_verified', 'failed', 'temporary_failure')),
  mail_from_subdomain  text NOT NULL DEFAULT 'send',
  dkim_tokens          text[] NOT NULL DEFAULT '{}',
  records              jsonb NOT NULL DEFAULT '[]',
  analysis             jsonb NOT NULL DEFAULT '{}',
  ns_provider          text NOT NULL DEFAULT '',
  provider_identity    text NOT NULL DEFAULT '',
  check_attempts       int NOT NULL DEFAULT 0,
  check_started_at     timestamptz NOT NULL DEFAULT now(),
  last_checked_at      timestamptz,
  next_check_at        timestamptz,
  last_error           text NOT NULL DEFAULT '',
  verified_at          timestamptz,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  deleted_at           timestamptz,
  FOREIGN KEY (org_id, project_id) REFERENCES projects (org_id, id) ON DELETE CASCADE,
  UNIQUE (org_id, id)
);
-- One active claim per domain name across the platform. See docs/architecture/domains.md.
CREATE UNIQUE INDEX domains_active_name_idx ON domains (name) WHERE deleted_at IS NULL;
CREATE INDEX domains_project_idx ON domains (project_id) WHERE deleted_at IS NULL;
CREATE INDEX domains_check_idx ON domains (next_check_at) WHERE deleted_at IS NULL AND next_check_at IS NOT NULL;
CREATE TRIGGER domains_touch BEFORE UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

CREATE TABLE api_keys (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id        uuid NOT NULL,
  project_id    uuid NOT NULL,
  name          text NOT NULL,
  mode          text NOT NULL CHECK (mode IN ('test', 'live')),
  permission    text NOT NULL DEFAULT 'full_access' CHECK (permission IN ('full_access', 'sending_access')),
  domain_id     uuid REFERENCES domains(id) ON DELETE CASCADE,
  lookup_id     text NOT NULL UNIQUE,
  secret_hash   bytea NOT NULL,
  hint          text NOT NULL,
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  last_used_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz,
  FOREIGN KEY (org_id, project_id) REFERENCES projects (org_id, id) ON DELETE CASCADE
);
CREATE INDEX api_keys_project_idx ON api_keys (project_id);

-- ---------------------------------------------------------------------------
-- Quota, credits and ledger
-- ---------------------------------------------------------------------------

CREATE TABLE usage_periods (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id              uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  period_start        timestamptz NOT NULL,
  period_end          timestamptz NOT NULL,
  plan                text NOT NULL,
  included            bigint NOT NULL CHECK (included >= 0),
  reserved            bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  consumed            bigint NOT NULL DEFAULT 0 CHECK (consumed >= 0),
  overage_cap         bigint NOT NULL DEFAULT 0 CHECK (overage_cap >= 0),
  overage_reserved    bigint NOT NULL DEFAULT 0 CHECK (overage_reserved >= 0),
  overage_consumed    bigint NOT NULL DEFAULT 0 CHECK (overage_consumed >= 0),
  data_included       bigint NOT NULL CHECK (data_included >= 0),
  data_reserved       bigint NOT NULL DEFAULT 0 CHECK (data_reserved >= 0),
  data_consumed       bigint NOT NULL DEFAULT 0 CHECK (data_consumed >= 0),
  warned_80_at        timestamptz,
  warned_95_at        timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, period_start),
  CHECK (period_end > period_start),
  CHECK (reserved + consumed <= included),
  CHECK (overage_reserved + overage_consumed <= overage_cap),
  CHECK (data_reserved + data_consumed <= data_included)
);

CREATE TABLE credit_lots (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  source         text NOT NULL CHECK (source IN ('purchase', 'grant', 'migration')),
  source_ref     text NOT NULL DEFAULT '',
  granted        bigint NOT NULL CHECK (granted > 0),
  reserved       bigint NOT NULL DEFAULT 0 CHECK (reserved >= 0),
  consumed       bigint NOT NULL DEFAULT 0 CHECK (consumed >= 0),
  data_granted   bigint NOT NULL DEFAULT 0 CHECK (data_granted >= 0),
  data_reserved  bigint NOT NULL DEFAULT 0 CHECK (data_reserved >= 0),
  data_consumed  bigint NOT NULL DEFAULT 0 CHECK (data_consumed >= 0),
  purchased_at   timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz NOT NULL,
  expired_at     timestamptz,
  expired_amount bigint NOT NULL DEFAULT 0 CHECK (expired_amount >= 0),
  CHECK (reserved + consumed + expired_amount <= granted),
  CHECK (data_reserved + data_consumed <= data_granted)
);
CREATE INDEX credit_lots_org_idx ON credit_lots (org_id, expires_at);
CREATE UNIQUE INDEX credit_lots_source_ref_idx ON credit_lots (source, source_ref) WHERE source_ref <> '';

-- ---------------------------------------------------------------------------
-- Emails
-- ---------------------------------------------------------------------------

CREATE TABLE emails (
  id                   uuid PRIMARY KEY,
  org_id               uuid NOT NULL,
  project_id           uuid NOT NULL,
  api_key_id           uuid REFERENCES api_keys(id) ON DELETE SET NULL,
  domain_id            uuid REFERENCES domains(id) ON DELETE SET NULL,
  mode                 text NOT NULL CHECK (mode IN ('test', 'live')),
  from_address         text NOT NULL,
  from_domain          text NOT NULL,
  subject              text,
  to_addresses         text[] NOT NULL DEFAULT '{}',
  cc_addresses         text[] NOT NULL DEFAULT '{}',
  bcc_addresses        text[] NOT NULL DEFAULT '{}',
  reply_to             text[] NOT NULL DEFAULT '{}',
  tags                 jsonb NOT NULL DEFAULT '[]',
  payload_ref          text,
  payload_sha256       bytea NOT NULL,
  payload_bytes        bigint NOT NULL,
  attachment_count     int NOT NULL DEFAULT 0,
  attachment_bytes     bigint NOT NULL DEFAULT 0,
  idempotency_key      text,
  resend_of            uuid REFERENCES emails(id) ON DELETE SET NULL,
  -- Aggregate status shown as Resend-compatible last_event.
  last_event           text NOT NULL,
  last_event_at        timestamptz NOT NULL DEFAULT now(),
  -- Send state machine. See internal/sender.
  send_state           text NOT NULL
                       CHECK (send_state IN ('captured', 'queued', 'sending', 'retry_wait', 'held',
                                             'sent', 'failed', 'skipped', 'unknown', 'canceled')),
  next_attempt_at      timestamptz NOT NULL DEFAULT now(),
  lease_owner          text,
  lease_expires_at     timestamptz,
  attempt_count        int NOT NULL DEFAULT 0,
  provider_message_id  text,
  created_at           timestamptz NOT NULL DEFAULT now(),
  queue_deadline_at    timestamptz NOT NULL,
  content_expires_at   timestamptz NOT NULL,
  content_deleted_at   timestamptz,
  metadata_expires_at  timestamptz NOT NULL,
  FOREIGN KEY (org_id, project_id) REFERENCES projects (org_id, id) ON DELETE CASCADE,
  UNIQUE (org_id, id),
  CHECK ((mode = 'test') = (send_state = 'captured'))
);
CREATE INDEX emails_project_created_idx ON emails (project_id, created_at DESC, id DESC);
CREATE INDEX emails_project_mode_idx ON emails (project_id, mode, created_at DESC);
CREATE INDEX emails_pending_idx ON emails (next_attempt_at) WHERE send_state IN ('queued', 'retry_wait');
CREATE INDEX emails_lease_idx ON emails (lease_expires_at) WHERE send_state = 'sending';
CREATE INDEX emails_held_idx ON emails (project_id) WHERE send_state = 'held';
CREATE INDEX emails_content_exp_idx ON emails (content_expires_at) WHERE content_deleted_at IS NULL;
CREATE INDEX emails_metadata_exp_idx ON emails (metadata_expires_at);
CREATE UNIQUE INDEX emails_provider_msg_idx ON emails (provider_message_id) WHERE provider_message_id IS NOT NULL;
CREATE INDEX emails_payload_ref_idx ON emails (payload_ref) WHERE payload_ref IS NOT NULL;

CREATE TABLE quota_reservations (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id           uuid NOT NULL,
  email_id         uuid NOT NULL,
  source           text NOT NULL CHECK (source IN ('period', 'credit', 'overage')),
  usage_period_id  uuid REFERENCES usage_periods(id),
  credit_lot_id    uuid REFERENCES credit_lots(id),
  quantity         int NOT NULL CHECK (quantity > 0),
  consumed         int NOT NULL DEFAULT 0 CHECK (consumed >= 0),
  released         int NOT NULL DEFAULT 0 CHECK (released >= 0),
  data_per_unit    bigint NOT NULL DEFAULT 0 CHECK (data_per_unit >= 0),
  created_at       timestamptz NOT NULL DEFAULT now(),
  settled_at       timestamptz,
  FOREIGN KEY (org_id, email_id) REFERENCES emails (org_id, id) ON DELETE CASCADE,
  CHECK (consumed + released <= quantity),
  CHECK ((source = 'credit') = (credit_lot_id IS NOT NULL)),
  CHECK ((source <> 'credit') = (usage_period_id IS NOT NULL))
);
CREATE INDEX quota_reservations_email_idx ON quota_reservations (email_id);

CREATE TABLE recipients (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id          uuid NOT NULL,
  email_id        uuid NOT NULL,
  address         text NOT NULL,
  normalized      text NOT NULL,
  field           text NOT NULL CHECK (field IN ('to', 'cc', 'bcc')),
  status          text NOT NULL
                  CHECK (status IN ('test_captured', 'queued', 'suppressed', 'sent', 'delivery_delayed',
                                    'delivered', 'bounced', 'complained', 'failed', 'canceled', 'delivery_unknown')),
  status_detail   text NOT NULL DEFAULT '',
  status_at       timestamptz NOT NULL DEFAULT now(),
  billing_state   text NOT NULL DEFAULT 'none' CHECK (billing_state IN ('none', 'reserved', 'consumed', 'released')),
  reservation_id  uuid REFERENCES quota_reservations(id),
  FOREIGN KEY (org_id, email_id) REFERENCES emails (org_id, id) ON DELETE CASCADE,
  UNIQUE (email_id, normalized),
  CHECK ((billing_state IN ('reserved', 'consumed', 'released')) = (reservation_id IS NOT NULL))
);
CREATE INDEX recipients_org_normalized_idx ON recipients (org_id, normalized);

CREATE TABLE send_attempts (
  id                   uuid PRIMARY KEY,
  org_id               uuid NOT NULL,
  email_id             uuid NOT NULL,
  worker_id            text NOT NULL,
  state                text NOT NULL
                       CHECK (state IN ('started', 'accepted', 'rejected', 'retryable', 'unknown',
                                        'resolved_accepted', 'resolved_not_sent')),
  provider_message_id  text,
  error_code           text NOT NULL DEFAULT '',
  error_message        text NOT NULL DEFAULT '',
  resolution_note      text NOT NULL DEFAULT '',
  resolved_by          uuid REFERENCES users(id) ON DELETE SET NULL,
  started_at           timestamptz NOT NULL DEFAULT now(),
  finished_at          timestamptz,
  FOREIGN KEY (org_id, email_id) REFERENCES emails (org_id, id) ON DELETE CASCADE
);
CREATE INDEX send_attempts_email_idx ON send_attempts (email_id);
CREATE INDEX send_attempts_unknown_idx ON send_attempts (started_at) WHERE state IN ('started', 'unknown');

CREATE TABLE credit_ledger (
  id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  usage_period_id  uuid REFERENCES usage_periods(id),
  credit_lot_id    uuid REFERENCES credit_lots(id),
  reservation_id   uuid,
  email_id         uuid,
  kind             text NOT NULL CHECK (kind IN ('grant', 'reserve', 'consume', 'release', 'expire', 'adjust')),
  source           text NOT NULL CHECK (source IN ('period', 'credit', 'overage')),
  quantity         bigint NOT NULL,
  data_bytes       bigint NOT NULL DEFAULT 0,
  note             text NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX credit_ledger_org_idx ON credit_ledger (org_id, created_at);

CREATE TABLE idempotency_records (
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id       uuid NOT NULL,
  operation        text NOT NULL,
  key              text NOT NULL,
  request_sha256   bytea NOT NULL,
  response_status  int NOT NULL,
  response_body    jsonb NOT NULL,
  resource_id      uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  PRIMARY KEY (org_id, project_id, operation, key)
);
CREATE INDEX idempotency_expiry_idx ON idempotency_records (expires_at);

-- Rate windows per project (live recipients per hour/day, test messages per day).
CREATE TABLE send_counters (
  org_id        uuid NOT NULL,
  project_id    uuid NOT NULL,
  window_kind   text NOT NULL CHECK (window_kind IN ('live_hour', 'live_day', 'test_day')),
  window_start  timestamptz NOT NULL,
  count         int NOT NULL CHECK (count >= 0),
  PRIMARY KEY (project_id, window_kind, window_start),
  FOREIGN KEY (org_id, project_id) REFERENCES projects (org_id, id) ON DELETE CASCADE
);

-- ---------------------------------------------------------------------------
-- Durable outbox, events and suppression
-- ---------------------------------------------------------------------------

CREATE TABLE outbox (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id            uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  topic             text NOT NULL CHECK (topic IN ('send', 'webhook')),
  message           jsonb NOT NULL,
  available_at      timestamptz NOT NULL DEFAULT now(),
  published_at      timestamptz,
  publish_attempts  int NOT NULL DEFAULT 0,
  last_error        text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX outbox_pending_idx ON outbox (available_at) WHERE published_at IS NULL;
CREATE INDEX outbox_published_idx ON outbox (published_at) WHERE published_at IS NOT NULL;

-- Defence in depth: a send job may only reference a live email.
CREATE FUNCTION outbox_send_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.topic = 'send' AND NOT EXISTS (
    SELECT 1 FROM emails
    WHERE id = (NEW.message->>'email_id')::uuid AND org_id = NEW.org_id AND mode = 'live'
  ) THEN
    RAISE EXCEPTION 'send outbox job must reference a live email of the same organization'
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER outbox_send_guard BEFORE INSERT OR UPDATE ON outbox
  FOR EACH ROW EXECUTE FUNCTION outbox_send_guard();

CREATE FUNCTION send_attempt_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM emails WHERE id = NEW.email_id AND org_id = NEW.org_id AND mode = 'live') THEN
    RAISE EXCEPTION 'send attempts are only allowed for live emails' USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER send_attempt_guard BEFORE INSERT ON send_attempts
  FOR EACH ROW EXECUTE FUNCTION send_attempt_guard();

CREATE TABLE email_events (
  id                   uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id               uuid NOT NULL,
  email_id             uuid NOT NULL,
  recipient_id         uuid REFERENCES recipients(id) ON DELETE CASCADE,
  type                 text NOT NULL,
  provider_message_id  text NOT NULL DEFAULT '',
  dedupe_key           text NOT NULL UNIQUE,
  occurred_at          timestamptz NOT NULL,
  received_at          timestamptz NOT NULL DEFAULT now(),
  applied              boolean NOT NULL DEFAULT true,
  detail               jsonb NOT NULL DEFAULT '{}',
  FOREIGN KEY (org_id, email_id) REFERENCES emails (org_id, id) ON DELETE CASCADE
);
CREATE INDEX email_events_email_idx ON email_events (email_id, occurred_at);

CREATE TABLE suppressions (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  normalized   text NOT NULL,
  reason       text NOT NULL CHECK (reason IN ('hard_bounce', 'complaint', 'manual', 'import')),
  source       text NOT NULL CHECK (source IN ('provider_event', 'api', 'dashboard', 'import', 'staff')),
  source_ref   text NOT NULL DEFAULT '',
  note         text NOT NULL DEFAULT '',
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz,
  removed_at   timestamptz,
  removed_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  remove_note  text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX suppressions_active_idx ON suppressions (org_id, normalized) WHERE removed_at IS NULL;

-- ---------------------------------------------------------------------------
-- Webhooks
-- ---------------------------------------------------------------------------

CREATE TABLE webhooks (
  id          uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id      uuid NOT NULL,
  project_id  uuid NOT NULL,
  url         text NOT NULL,
  events      text[] NOT NULL,
  secret_enc  bytea NOT NULL,
  status      text NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled', 'disabled')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id, project_id) REFERENCES projects (org_id, id) ON DELETE CASCADE,
  UNIQUE (org_id, id)
);
CREATE INDEX webhooks_project_idx ON webhooks (project_id);
CREATE TRIGGER webhooks_touch BEFORE UPDATE ON webhooks FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- A webhook event has a stable id across retries and manual resends.
CREATE TABLE webhook_events (
  id          text PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  project_id  uuid NOT NULL,
  type        text NOT NULL,
  email_id    uuid,
  payload     jsonb NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_events_project_idx ON webhook_events (project_id, created_at DESC);

CREATE TABLE webhook_deliveries (
  id                uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id            uuid NOT NULL,
  webhook_id        uuid NOT NULL,
  event_id          text NOT NULL REFERENCES webhook_events(id) ON DELETE CASCADE,
  state             text NOT NULL DEFAULT 'pending'
                    CHECK (state IN ('pending', 'delivering', 'succeeded', 'failed')),
  attempt_count     int NOT NULL DEFAULT 0,
  next_attempt_at   timestamptz NOT NULL DEFAULT now(),
  lease_expires_at  timestamptz,
  last_attempt_at   timestamptz,
  last_status       int,
  last_response     text NOT NULL DEFAULT '',
  last_error        text NOT NULL DEFAULT '',
  manual_resends    int NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id, webhook_id) REFERENCES webhooks (org_id, id) ON DELETE CASCADE,
  UNIQUE (webhook_id, event_id)
);
CREATE INDEX webhook_deliveries_due_idx ON webhook_deliveries (next_attempt_at) WHERE state IN ('pending', 'delivering');

-- ---------------------------------------------------------------------------
-- Operations
-- ---------------------------------------------------------------------------

CREATE TABLE audit_log (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id            uuid REFERENCES organizations(id) ON DELETE CASCADE,
  actor_kind        text NOT NULL CHECK (actor_kind IN ('user', 'api_key', 'system', 'staff')),
  actor_user_id     uuid REFERENCES users(id) ON DELETE SET NULL,
  actor_api_key_id  uuid REFERENCES api_keys(id) ON DELETE SET NULL,
  action            text NOT NULL,
  target_type       text NOT NULL DEFAULT '',
  target_id         text NOT NULL DEFAULT '',
  metadata          jsonb NOT NULL DEFAULT '{}',
  ip                inet,
  created_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_org_idx ON audit_log (org_id, created_at DESC);

CREATE TABLE alerts (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id       uuid REFERENCES organizations(id) ON DELETE CASCADE,
  project_id   uuid,
  kind         text NOT NULL,
  severity     text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  dedupe_key   text NOT NULL,
  message      text NOT NULL,
  data         jsonb NOT NULL DEFAULT '{}',
  created_at   timestamptz NOT NULL DEFAULT now(),
  notified_at  timestamptz,
  resolved_at  timestamptz
);
CREATE UNIQUE INDEX alerts_open_dedupe_idx ON alerts (dedupe_key) WHERE resolved_at IS NULL;
CREATE INDEX alerts_org_idx ON alerts (org_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Security definer helpers for lookups that happen before the tenant is known.
-- ---------------------------------------------------------------------------

CREATE FUNCTION hedvi_lookup_api_key(p_lookup_id text)
RETURNS TABLE (id uuid, org_id uuid, project_id uuid, mode text, permission text,
               domain_id uuid, secret_hash bytea, revoked_at timestamptz)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT k.id, k.org_id, k.project_id, k.mode, k.permission, k.domain_id, k.secret_hash, k.revoked_at
  FROM api_keys k WHERE k.lookup_id = p_lookup_id
$$;

-- Returns the caller's role in the project's organization, or no row.
CREATE FUNCTION hedvi_project_access(p_project_id uuid, p_user_id uuid)
RETURNS TABLE (org_id uuid, role text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT p.org_id, m.role FROM projects p
  JOIN memberships m ON m.org_id = p.org_id AND m.user_id = p_user_id
  WHERE p.id = p_project_id
$$;

-- Reports whether a domain name is actively claimed by another organization
-- without revealing which one.
CREATE FUNCTION hedvi_domain_claim(p_name text, p_org_id uuid)
RETURNS TABLE (claimed_elsewhere boolean, verified_elsewhere boolean, stale_claim_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT
    bool_or(d.org_id <> p_org_id),
    bool_or(d.org_id <> p_org_id AND d.status = 'verified'),
    (array_agg(d.id) FILTER (WHERE d.org_id <> p_org_id AND d.status <> 'verified'
                               AND d.created_at < now() - interval '72 hours'))[1]
  FROM domains d WHERE d.name = p_name AND d.deleted_at IS NULL
$$;

-- Releases an unverified claim older than 72 hours so the real owner can add the domain.
CREATE FUNCTION hedvi_release_stale_domain_claim(p_domain_id uuid)
RETURNS boolean
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  WITH released AS (
    UPDATE domains SET deleted_at = now(), last_error = 'released: unverified claim older than 72 hours'
    WHERE id = p_domain_id AND status <> 'verified' AND deleted_at IS NULL
      AND created_at < now() - interval '72 hours'
    RETURNING id
  )
  SELECT EXISTS (SELECT 1 FROM released)
$$;

REVOKE ALL ON FUNCTION hedvi_lookup_api_key(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION hedvi_project_access(uuid, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION hedvi_domain_claim(text, uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION hedvi_release_stale_domain_claim(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_lookup_api_key(text) TO hedvi_api;
GRANT EXECUTE ON FUNCTION hedvi_project_access(uuid, uuid) TO hedvi_api;
GRANT EXECUTE ON FUNCTION hedvi_domain_claim(text, uuid) TO hedvi_api;
GRANT EXECUTE ON FUNCTION hedvi_release_stale_domain_claim(uuid) TO hedvi_api;

-- ---------------------------------------------------------------------------
-- Privileges and row level security
-- ---------------------------------------------------------------------------

GRANT USAGE ON SCHEMA public TO hedvi_api, hedvi_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO hedvi_api, hedvi_worker;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO hedvi_api, hedvi_worker;
-- The ledger and audit log are append-only for application roles.
REVOKE UPDATE, DELETE ON credit_ledger, audit_log FROM hedvi_api, hedvi_worker;
-- Only retention may delete audit rows; it runs as the worker with an explicit grant.
GRANT DELETE ON audit_log TO hedvi_worker;

DO $$
DECLARE
  t text;
BEGIN
  -- Tables isolated by org_id.
  FOREACH t IN ARRAY ARRAY[
    'projects', 'domains', 'api_keys', 'usage_periods', 'credit_lots', 'emails',
    'quota_reservations', 'recipients', 'send_attempts', 'credit_ledger',
    'idempotency_records', 'send_counters', 'outbox', 'email_events', 'suppressions',
    'webhooks', 'webhook_events', 'webhook_deliveries', 'audit_log', 'alerts'
  ] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY tenant_isolation ON %I TO hedvi_api USING (org_id = hedvi_org()) WITH CHECK (org_id = hedvi_org())', t);
    EXECUTE format('CREATE POLICY worker_access ON %I TO hedvi_worker USING (true) WITH CHECK (true)', t);
  END LOOP;
END
$$;

ALTER TABLE organizations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON organizations TO hedvi_api
  USING (id = hedvi_org() OR id IN (SELECT m.org_id FROM memberships m WHERE m.user_id = hedvi_user()))
  WITH CHECK (id = hedvi_org());
CREATE POLICY worker_access ON organizations TO hedvi_worker USING (true) WITH CHECK (true);

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON memberships TO hedvi_api
  USING (org_id = hedvi_org() OR user_id = hedvi_user())
  WITH CHECK (org_id = hedvi_org());
CREATE POLICY worker_access ON memberships TO hedvi_worker USING (true) WITH CHECK (true);
