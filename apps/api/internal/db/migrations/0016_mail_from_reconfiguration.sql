-- The existing trigger still rejects direct edits to claim coordinates by the API.
-- This transition keeps the claim and its exclusive reservation, and invalidates
-- verification before any provider write for the new bounce address.
ALTER TABLE domains ADD COLUMN mail_from_revision bigint NOT NULL DEFAULT 0 CHECK(mail_from_revision>=0),
                    ADD COLUMN mail_from_reconfiguring boolean NOT NULL DEFAULT false;

-- Only the transition below advances the revision or starts a change. The
-- API and worker may clear the marker in the same verified-domain update.
CREATE FUNCTION hedvi_guard_mail_from_revision() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF pg_has_role(current_user,'hedvi_api','USAGE') AND NOT pg_has_role(current_user,'hedvi_worker','USAGE') THEN
   IF NEW.mail_from_revision<>OLD.mail_from_revision OR
      (NEW.mail_from_reconfiguring<>OLD.mail_from_reconfiguring AND
       (NEW.mail_from_reconfiguring OR NEW.status<>'verified' OR NEW.provider_state<>'active')) THEN
     RAISE insufficient_privilege;
   END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER guard_mail_from_revision BEFORE UPDATE OF mail_from_revision,mail_from_reconfiguring
 ON domains FOR EACH ROW EXECUTE FUNCTION hedvi_guard_mail_from_revision();

CREATE FUNCTION hedvi_change_mail_from(project uuid, claim uuid, previous text, previous_revision bigint, replacement text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF replacement IS NULL OR replacement COLLATE "C" !~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$' OR previous=replacement THEN
   RETURN false;
 END IF;
 IF NOT EXISTS (SELECT 1 FROM public.domains WHERE id=claim AND org_id=public.hedvi_org() AND project_id=project
               AND deleted_at IS NULL AND provider_state<>'deleting' AND mail_from_subdomain=previous AND mail_from_revision=previous_revision
               AND length(name)+1+length(replacement)<=253) THEN RETURN false; END IF;
 IF NOT pg_try_advisory_xact_lock(hashtextextended(claim::text,43369)) THEN RETURN false; END IF;
 UPDATE public.domains SET mail_from_subdomain=replacement, mail_from_revision=mail_from_revision+1,
    mail_from_reconfiguring=true, status='pending', verified_at=NULL,
    records='[]',analysis='{}',last_error='',check_attempts=0,check_started_at=clock_timestamp(),last_checked_at=clock_timestamp(),
    next_check_at=CASE WHEN provider_state='awaiting_dns' THEN created_at+interval '72 hours' END,
    provider_state=CASE WHEN provider_state='awaiting_dns' THEN 'awaiting_dns' ELSE 'provisioning' END
 WHERE id=claim AND org_id=public.hedvi_org() AND project_id=project AND mail_from_subdomain=previous AND mail_from_revision=previous_revision
   AND provider_state<>'deleting' AND deleted_at IS NULL;
 RETURN FOUND;
END $$;
REVOKE ALL ON FUNCTION hedvi_change_mail_from(uuid,uuid,text,bigint,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_change_mail_from(uuid,uuid,text,bigint,text) TO hedvi_api;
