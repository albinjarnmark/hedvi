-- The active row and its UUID are the durable reservation for an account-wide identity.
ALTER TABLE domains ADD COLUMN provider_state text NOT NULL DEFAULT 'legacy'
  CHECK (provider_state IN ('legacy', 'provisioning', 'active', 'deleting'));
ALTER TABLE domains ALTER COLUMN provider_state SET DEFAULT 'provisioning';

-- A previous DNS or SES result cannot establish the new ownership invariant.
UPDATE domains SET status = 'pending', verified_at = NULL, next_check_at = now(),
  last_error = 'Existing identity requires ownership reconciliation.'
WHERE deleted_at IS NULL;

DROP FUNCTION hedvi_release_stale_domain_claim(uuid);
DROP FUNCTION hedvi_domain_claim(text, uuid);
