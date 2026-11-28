-- Read only: the public API cannot decide live access or change delivery limits.
-- Legal receipt contents are pinned by the running server, never supplied by a client.
CREATE FUNCTION hedvi_org_has_live_consent(oid uuid, version text, documents jsonb, adopted boolean)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT $1=public.hedvi_org() AND EXISTS (
  SELECT 1 FROM public.memberships m JOIN public.users u ON u.id=m.user_id
  JOIN public.legal_receipts r ON r.user_id=u.id
  WHERE m.org_id=$1 AND m.role IN ('owner','admin') AND u.email_verified_at IS NOT NULL
    AND r.manifest_version=$2 AND r.documents=$3 AND r.manifest_adopted=$4
 )
$$;
REVOKE ALL ON FUNCTION hedvi_org_has_live_consent(uuid,text,jsonb,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_org_has_live_consent(uuid,text,jsonb,boolean) TO hedvi_api,hedvi_worker;
GRANT SELECT ON billing_order_status TO hedvi_worker;

-- A claim tag must remain attached to the same resource for its entire life.
-- Customer deletion is a soft delete; recreating a claim always gets a new UUID.
REVOKE INSERT,DELETE ON domains FROM hedvi_api;
GRANT INSERT(org_id,project_id,name,region,mail_from_subdomain,status,provider_state,next_check_at) ON domains TO hedvi_api;
REVOKE DELETE ON projects FROM hedvi_api;
CREATE FUNCTION hedvi_immutable_customer_claim() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF pg_has_role(current_user,'hedvi_api','USAGE') AND NOT pg_has_role(current_user,'hedvi_worker','USAGE') THEN
  IF TG_TABLE_NAME='domains' THEN
   IF (NEW.id,NEW.org_id,NEW.project_id,NEW.name,NEW.region,NEW.mail_from_subdomain)
      IS DISTINCT FROM (OLD.id,OLD.org_id,OLD.project_id,OLD.name,OLD.region,OLD.mail_from_subdomain) THEN
    RAISE insufficient_privilege;
   END IF;
  ELSIF (NEW.id,NEW.org_id) IS DISTINCT FROM (OLD.id,OLD.org_id) THEN RAISE insufficient_privilege;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER immutable_customer_claim BEFORE UPDATE ON domains FOR EACH ROW EXECUTE FUNCTION hedvi_immutable_customer_claim();
CREATE TRIGGER immutable_customer_project BEFORE UPDATE OF id,org_id ON projects FOR EACH ROW EXECUTE FUNCTION hedvi_immutable_customer_claim();
CREATE POLICY api_no_auto_activation_audit ON audit_log AS RESTRICTIVE FOR INSERT TO hedvi_api
 WITH CHECK (action<>'project.live_auto_activated');
