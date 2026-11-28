-- The API can only enqueue the newly created organization's notification.
-- The worker owns delivery and the recipient is configured outside customer data.
CREATE TABLE signup_notifications (
  org_id uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','sending','accepted','delivered','failed','unknown')),
  is_test boolean NOT NULL DEFAULT false,
  attempts integer NOT NULL DEFAULT 0,
  available_at timestamptz NOT NULL DEFAULT now(),
  attempt_id uuid,
  provider_message_id text
);
CREATE INDEX signup_notifications_pending ON signup_notifications (available_at) WHERE state IN ('queued','sending');
ALTER TABLE signup_notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE signup_notifications FORCE ROW LEVEL SECURITY;
REVOKE ALL ON signup_notifications FROM PUBLIC,hedvi_api;
GRANT INSERT(org_id) ON signup_notifications TO hedvi_api;
CREATE POLICY signup_enqueue ON signup_notifications FOR INSERT TO hedvi_api WITH CHECK (org_id = hedvi_org());
GRANT SELECT,UPDATE,DELETE ON signup_notifications TO hedvi_worker;
CREATE POLICY worker_access ON signup_notifications TO hedvi_worker USING (true) WITH CHECK (true);
