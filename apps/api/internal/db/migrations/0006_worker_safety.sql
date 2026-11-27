-- Fences late HTTP responses after a delivery lease was reclaimed.
ALTER TABLE webhook_deliveries ADD COLUMN lease_token uuid;
CREATE INDEX webhook_deliveries_org_inflight ON webhook_deliveries (org_id, webhook_id, lease_expires_at) WHERE state = 'delivering';
CREATE INDEX emails_org_inflight ON emails (org_id, lease_expires_at) WHERE send_state = 'sending';

-- Serialize reactivation after expiry and changes in suppression strength.
-- Complaint > permanent manual/import > time-limited hard bounce.
CREATE FUNCTION hedvi_suppress(p_org uuid, p_normalized text, p_reason text, p_source text,
  p_ref text, p_expires timestamptz, p_note text, p_actor uuid) RETURNS void
LANGUAGE plpgsql SECURITY INVOKER SET search_path = pg_catalog, public AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(p_org::text, 613));
  UPDATE suppressions SET removed_at=expires_at, remove_note='Expired'
    WHERE org_id=p_org AND normalized=p_normalized AND removed_at IS NULL AND expires_at<=now();
  INSERT INTO suppressions(org_id, normalized, reason, source, source_ref, expires_at, note, created_by)
    VALUES(p_org, p_normalized, p_reason, p_source, p_ref, p_expires, p_note, p_actor)
    ON CONFLICT (org_id,normalized) WHERE removed_at IS NULL DO UPDATE SET
      reason=excluded.reason, source=excluded.source, source_ref=excluded.source_ref,
      expires_at=excluded.expires_at, note=excluded.note, created_by=excluded.created_by
    WHERE (excluded.reason='complaint' AND suppressions.reason <> 'complaint')
       OR (suppressions.reason='hard_bounce' AND (excluded.expires_at IS NULL OR excluded.expires_at> suppressions.expires_at));
END
$$;
REVOKE ALL ON FUNCTION hedvi_suppress(uuid,text,text,text,text,timestamptz,text,uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_suppress(uuid,text,text,text,text,timestamptz,text,uuid) TO hedvi_api,hedvi_worker;

-- Recover complaints that the previous insert-on-conflict path could have
-- discarded, where retained evidence exists and a later removal was not recorded.
SELECT hedvi_suppress(r.org_id,r.normalized,'complaint','provider_event',r.email_id::text,NULL,'',NULL)
FROM (SELECT DISTINCT ON (org_id,normalized) org_id, normalized, email_id, status_at FROM recipients
      WHERE status='complained' ORDER BY org_id,normalized,status_at DESC) r
WHERE NOT EXISTS(SELECT 1 FROM suppressions s WHERE s.org_id=r.org_id AND s.normalized=r.normalized
  AND ((s.removed_at IS NULL AND s.reason='complaint') OR s.removed_at>=r.status_at));

-- Historical staff resolutions synthesized a placeholder without knowing SES's
-- message ID. Make that absence explicit, so a later fully bound SES event
-- can record the real ID and every subsequent event must match it.
WITH manual AS (
  UPDATE send_attempts SET provider_message_id=NULL WHERE state='resolved_accepted'
    AND provider_message_id='manual-' || id::text RETURNING id,email_id
) UPDATE emails e SET provider_message_id=NULL FROM manual m
  WHERE e.id=m.email_id AND e.provider_message_id='manual-' || m.id::text;

-- Older network and SES errors may still be visible in retained customer rows.
UPDATE webhook_deliveries SET last_error='request failed' WHERE last_error LIKE 'request failed:%';
UPDATE recipients SET status_detail='provider_rejected: The sending provider rejected the email.'
  WHERE status='failed' AND (status_detail LIKE '%: The sending provider rejected the email:%'
    OR status_detail LIKE 'Rejected by provider after acceptance:%');
UPDATE webhook_events SET payload=jsonb_set(payload,'{data,failed,reason}',to_jsonb('The sending provider rejected the email.'::text))
  WHERE type='email.failed' AND payload #>> '{data,failed,reason}' LIKE 'The sending provider rejected the email:%';
