ALTER TABLE users
  ADD COLUMN login_failure_window_started_at timestamptz,
  ADD COLUMN failed_mfa integer NOT NULL DEFAULT 0 CHECK (failed_mfa >= 0),
  ADD COLUMN mfa_failure_window_started_at timestamptz,
  ADD COLUMN mfa_locked_until timestamptz;

-- Application processes must never be able to rewrite migration history.
REVOKE ALL ON schema_migrations FROM hedvi_api, hedvi_worker;
