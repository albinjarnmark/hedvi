-- SES identity tags bind provider resources to application rows. They do not
-- prove that this claim's customer currently controls DNS: selectors may be
-- reused when an identity is recreated. Require a per-claim TXT challenge too.
-- Drain old API/workers before this migration so old verification code cannot
-- restore eligibility without the challenge.
UPDATE domains SET status = 'pending', verified_at = NULL, last_checked_at = NULL,
  next_check_at = now(), check_started_at = now(), check_attempts = 0,
  last_error = 'Publish the current _hedvi-verification TXT record to confirm domain ownership.'
WHERE deleted_at IS NULL AND provider_state <> 'deleting';
