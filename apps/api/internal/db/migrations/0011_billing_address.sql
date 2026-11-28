-- An invoice address is bound before Stripe can create a payable session.
-- Legacy customers remain unbound until operator reconciliation.
ALTER TABLE billing_customers ADD COLUMN billing_profile jsonb,
 ADD COLUMN profile_rejected boolean NOT NULL DEFAULT false,
 ADD COLUMN creation_uncertain boolean NOT NULL DEFAULT false,
 ADD COLUMN creation_lease uuid, ADD COLUMN creation_lease_until timestamptz;
ALTER TABLE billing_customers ADD CONSTRAINT billing_profile_shape CHECK (
  billing_profile IS NULL OR (jsonb_typeof(billing_profile)='object'
    AND billing_profile ?& ARRAY['name','line1','line2','city','postal_code','state','country']
    AND jsonb_typeof(billing_profile->'country')='string'
    AND billing_profile->>'country' ~ '^[A-Z]{2}$'));
GRANT INSERT(billing_profile) ON billing_customers TO hedvi_api;

CREATE FUNCTION hedvi_billing_profile_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE retry boolean;
BEGIN
 IF TG_TABLE_NAME='billing_customers' THEN
   IF TG_OP='INSERT' AND NEW.billing_profile IS NULL THEN RAISE EXCEPTION 'billing profile required'; END IF;
   IF TG_OP='UPDATE' THEN
     retry := OLD.profile_rejected AND NOT OLD.creation_uncertain AND OLD.customer_id IS NULL
       AND NEW.customer_id IS NULL AND NEW.id<>OLD.id AND NOT NEW.profile_rejected
       AND NEW.billing_profile IS NOT NULL
       AND NOT EXISTS(SELECT 1 FROM public.billing_orders WHERE org_id=OLD.org_id);
     IF (OLD.billing_profile IS DISTINCT FROM NEW.billing_profile OR OLD.id<>NEW.id
         OR OLD.currency<>NEW.currency OR OLD.created_at<>NEW.created_at) AND NOT retry THEN
       RAISE EXCEPTION 'immutable billing profile';
     END IF;
     IF OLD.profile_rejected AND NEW.customer_id IS NOT NULL THEN RAISE EXCEPTION 'rejected customer attempt'; END IF;
   END IF;
 ELSE
   IF NEW.country IS DISTINCT FROM (SELECT c.billing_profile->>'country' FROM public.billing_customers c WHERE c.org_id=NEW.org_id) THEN
     RAISE EXCEPTION 'billing order country differs from invoice address';
   END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER billing_profile_immutable BEFORE INSERT OR UPDATE ON billing_customers
 FOR EACH ROW EXECUTE FUNCTION hedvi_billing_profile_immutable();
CREATE TRIGGER billing_order_country BEFORE INSERT OR UPDATE OF country ON billing_orders
 FOR EACH ROW EXECUTE FUNCTION hedvi_billing_profile_immutable();

-- API callers cannot directly edit profiles, mark outcomes or rotate keys.
-- Only a first, exclusive, definitively rejected Customer creation is editable.
CREATE FUNCTION hedvi_retry_billing_profile(oid uuid, proposed jsonb, curr text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR oid IS NULL THEN RAISE EXCEPTION 'organization mismatch'; END IF;
 UPDATE public.billing_customers SET billing_profile=proposed,currency=curr,id=uuidv7(),
   created_at=clock_timestamp(),profile_rejected=false,creation_lease=NULL,creation_lease_until=NULL
 WHERE org_id=oid AND profile_rejected AND NOT creation_uncertain AND customer_id IS NULL
   AND NOT EXISTS(SELECT 1 FROM public.billing_orders WHERE org_id=oid);
END $$;
CREATE FUNCTION hedvi_claim_billing_customer(oid uuid, attempt uuid, claim uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR oid IS NULL OR claim IS NULL THEN RAISE EXCEPTION 'organization mismatch'; END IF;
 UPDATE public.billing_customers SET creation_uncertain=creation_uncertain OR creation_lease IS NOT NULL,
   creation_lease=claim,creation_lease_until=clock_timestamp()+interval '45 seconds'
 WHERE org_id=oid AND id=attempt AND customer_id IS NULL AND NOT profile_rejected
   AND (creation_lease IS NULL OR creation_lease_until<=clock_timestamp());
 RETURN FOUND;
END $$;
CREATE FUNCTION hedvi_fail_billing_customer(oid uuid, attempt uuid, claim uuid, rejected boolean) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE editable boolean := false; at_time timestamptz := clock_timestamp();
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR oid IS NULL THEN RAISE EXCEPTION 'organization mismatch'; END IF;
 UPDATE public.billing_customers SET
   profile_rejected=rejected AND NOT creation_uncertain AND creation_lease_until>at_time
     AND NOT EXISTS(SELECT 1 FROM public.billing_orders WHERE org_id=oid),
   creation_uncertain=creation_uncertain OR NOT rejected OR creation_lease_until<=at_time,
   creation_lease=NULL,creation_lease_until=NULL
 WHERE org_id=oid AND id=attempt AND creation_lease=claim AND customer_id IS NULL
 RETURNING profile_rejected INTO editable;
 RETURN COALESCE(editable,false);
END $$;
REVOKE ALL ON FUNCTION hedvi_retry_billing_profile(uuid,jsonb,text),hedvi_claim_billing_customer(uuid,uuid,uuid),hedvi_fail_billing_customer(uuid,uuid,uuid,boolean) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_retry_billing_profile(uuid,jsonb,text),hedvi_claim_billing_customer(uuid,uuid,uuid),hedvi_fail_billing_customer(uuid,uuid,uuid,boolean) TO hedvi_api;
