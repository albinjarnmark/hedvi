-- Test capture and live sending are separate idempotency namespaces.
-- Backfill existing responses from the authoritative accepted email.
ALTER TABLE idempotency_records ADD COLUMN mode text CHECK (mode IN ('test', 'live', 'legacy'));
UPDATE idempotency_records i SET mode = e.mode FROM emails e
 WHERE e.id = i.resource_id AND e.org_id = i.org_id AND e.project_id = i.project_id;
DELETE FROM idempotency_records WHERE mode IS NULL AND expires_at <= now();
-- A deleted test capture may still have an unexpired retry record. Preserve
-- it as a tombstone that blocks both modes until the original TTL expires.
UPDATE idempotency_records SET mode = 'legacy' WHERE mode IS NULL;
ALTER TABLE idempotency_records ALTER COLUMN mode SET NOT NULL;
ALTER TABLE idempotency_records DROP CONSTRAINT idempotency_records_pkey;
ALTER TABLE idempotency_records ADD PRIMARY KEY (org_id, project_id, operation, mode, key);
