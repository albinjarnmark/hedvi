-- Existing rows may own SES resources even if their create timed out. Retain
-- their global reservation. Only newly issued, unproven challenges can expire.
ALTER TABLE domains DROP CONSTRAINT domains_provider_state_check;
ALTER TABLE domains ADD CONSTRAINT domains_provider_state_check
  CHECK (provider_state IN ('awaiting_dns','legacy','provisioning','active','deleting'));
CREATE UNIQUE INDEX domains_project_claim_idx ON domains(project_id,name) WHERE deleted_at IS NULL;
DROP INDEX domains_active_name_idx;
CREATE UNIQUE INDEX domains_active_name_idx ON domains(name)
  WHERE deleted_at IS NULL AND provider_state <> 'awaiting_dns';
