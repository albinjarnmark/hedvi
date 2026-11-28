-- A durable ordering watermark for each customer SES send path. It must
-- outlive email/attempt retention and resolved alerts, otherwise a delayed old
-- response could reopen an incident after a newer successful send.
CREATE TABLE provider_send_routes (
  id                       text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{64}$'),
  org_id                   uuid NOT NULL,
  project_id               uuid NOT NULL,
  domain_id                uuid NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
  from_domain              text NOT NULL,
  tenant_name              text NOT NULL,
  configuration_set        text NOT NULL,
  last_attempt_started_at  timestamptz,
  last_attempt_id          uuid,
  last_outcome             text CHECK (last_outcome IN ('accepted','failure')),
  updated_at               timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id,project_id) REFERENCES projects(org_id,id) ON DELETE CASCADE,
  UNIQUE (project_id,domain_id,from_domain,tenant_name,configuration_set),
  CHECK ((last_attempt_started_at IS NULL AND last_attempt_id IS NULL AND last_outcome IS NULL)
      OR (last_attempt_started_at IS NOT NULL AND last_attempt_id IS NOT NULL AND last_outcome IS NOT NULL))
);
ALTER TABLE provider_send_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE provider_send_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY worker_access ON provider_send_routes TO hedvi_worker USING (true) WITH CHECK (true);
REVOKE ALL ON provider_send_routes FROM PUBLIC,hedvi_api;
GRANT SELECT,INSERT,UPDATE,DELETE ON provider_send_routes TO hedvi_worker;

-- NULL means a pre-migration attempt whose provider configuration is unknown.
-- Such attempts still settle normally, but cannot claim recovery for a route.
ALTER TABLE send_attempts ADD COLUMN provider_route_id text
  REFERENCES provider_send_routes(id) ON DELETE SET NULL;
REVOKE INSERT,UPDATE,DELETE ON send_attempts FROM hedvi_api;

-- Customer-scoped provider incidents are visible through the ordinary alert
-- projection, but only the worker may write them. Preserve legitimate API
-- quota/domain-alert writes while blocking kind/key spoofing and key squatting.
CREATE POLICY api_provider_alert_insert ON alerts AS RESTRICTIVE FOR INSERT TO hedvi_api
  WITH CHECK (kind<>'provider_sending_paused' AND dedupe_key NOT LIKE 'provider-send:%');
CREATE POLICY api_provider_alert_update ON alerts AS RESTRICTIVE FOR UPDATE TO hedvi_api
  USING (kind<>'provider_sending_paused' AND dedupe_key NOT LIKE 'provider-send:%')
  WITH CHECK (kind<>'provider_sending_paused' AND dedupe_key NOT LIKE 'provider-send:%');
CREATE POLICY api_provider_alert_delete ON alerts AS RESTRICTIVE FOR DELETE TO hedvi_api
  USING (kind<>'provider_sending_paused' AND dedupe_key NOT LIKE 'provider-send:%');

-- Existing unscoped provider:<code> alerts cannot be assigned to one path
-- safely. Leave them for explicit operator review; new route alerts use an
-- independent key and are never suppressed by those legacy incidents.
