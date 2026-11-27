-- Drain acceptance and retention before applying this migration. Existing
-- projects above the default keep their data and may drain normally.
ALTER TABLE projects
  ADD COLUMN live_storage_limit_bytes bigint NOT NULL DEFAULT 1073741824 CHECK (live_storage_limit_bytes >= 0),
  ADD COLUMN live_stored_payload_bytes bigint NOT NULL DEFAULT 0 CHECK (live_stored_payload_bytes >= 0);

-- ALTER has locked projects. Fail cleanly if an old email writer still owns
-- the child table so the migrator can retry after that writer finishes.
LOCK TABLE emails IN SHARE ROW EXCLUSIVE MODE NOWAIT;

UPDATE projects p SET live_stored_payload_bytes = stored.total
FROM (
  SELECT project_id, sum(payload_bytes)::bigint AS total FROM emails
  WHERE mode = 'live' AND payload_ref IS NOT NULL AND content_deleted_at IS NULL
  GROUP BY project_id
) stored WHERE p.id = stored.project_id;

-- Ownership moves, metadata deletion and ordinary content retention all
-- account for the actual presence of the live payload in the same transaction.
CREATE FUNCTION hedvi_account_live_payload() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP <> 'INSERT' AND OLD.mode = 'live' AND OLD.payload_ref IS NOT NULL AND OLD.content_deleted_at IS NULL THEN
    UPDATE projects SET live_stored_payload_bytes = live_stored_payload_bytes - OLD.payload_bytes
    WHERE id = OLD.project_id;
  END IF;
  IF TG_OP <> 'DELETE' AND NEW.mode = 'live' AND NEW.payload_ref IS NOT NULL AND NEW.content_deleted_at IS NULL THEN
    UPDATE projects SET live_stored_payload_bytes = live_stored_payload_bytes + NEW.payload_bytes
    WHERE id = NEW.project_id AND live_storage_limit_bytes - live_stored_payload_bytes >= NEW.payload_bytes;
    IF NOT FOUND THEN
      RAISE EXCEPTION 'Project live payload storage limit reached'
        USING ERRCODE = '23514', CONSTRAINT = 'projects_live_payload_capacity';
    END IF;
  END IF;
  RETURN NULL;
END
$$;

CREATE TRIGGER emails_live_payload_storage
AFTER INSERT OR DELETE OR UPDATE OF project_id, mode, payload_ref, payload_bytes, content_deleted_at ON emails
FOR EACH ROW EXECUTE FUNCTION hedvi_account_live_payload();

-- The previous operational counters excluded recipients suppressed at acceptance.
-- Include all recipient rows already accepted in the current UTC windows.
WITH hourly AS (
  SELECT e.org_id, e.project_id, date_trunc('hour', e.created_at, 'UTC') AS hour_start, count(*) AS total
  FROM emails e JOIN recipients r ON r.email_id = e.id
  WHERE e.mode = 'live' AND e.created_at >= date_trunc('day', now(), 'UTC') AND e.created_at <= now()
  GROUP BY e.org_id, e.project_id, hour_start
), windows AS (
  SELECT org_id, project_id, 'live_hour' AS kind, hour_start AS starts, total
  FROM hourly WHERE hour_start = date_trunc('hour', now(), 'UTC')
  UNION ALL
  SELECT org_id, project_id, 'live_day', date_trunc('day', now(), 'UTC'), sum(total)
  FROM hourly GROUP BY org_id, project_id
)
INSERT INTO send_counters (org_id, project_id, window_kind, window_start, count)
SELECT org_id, project_id, kind, starts, total::integer FROM windows
ON CONFLICT (project_id, window_kind, window_start) DO UPDATE
SET count = greatest(send_counters.count, EXCLUDED.count);
