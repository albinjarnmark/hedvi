-- Raw hourly_limit/daily_limit hold the operator's exception. An ordinary
-- approval historically submitted 25/100 even if the form was left untouched.
ALTER TABLE projects ADD COLUMN sending_limits_manual boolean NOT NULL DEFAULT false;
UPDATE projects p SET sending_limits_manual=true
WHERE (p.hourly_limit,p.daily_limit)<>(25,100) OR EXISTS (
 SELECT 1 FROM audit_log a WHERE a.org_id=p.org_id AND a.target_type='project'
 AND a.target_id=p.id::text AND a.action='project.approved' AND a.actor_kind IN ('staff','system')
 AND (coalesce(a.metadata->>'hourly_limit',a.metadata->>'hourly')<>'25'
   OR coalesce(a.metadata->>'daily_limit',a.metadata->>'daily')<>'100')
);

CREATE FUNCTION hedvi_guard_sending_limit_mode() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF pg_has_role(current_user,'hedvi_api','USAGE') AND NOT pg_has_role(current_user,'hedvi_worker','USAGE')
 AND (CASE WHEN TG_OP='INSERT' THEN NEW.sending_limits_manual
           ELSE NEW.sending_limits_manual IS DISTINCT FROM OLD.sending_limits_manual END)
 THEN RAISE insufficient_privilege; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_sending_limit_mode BEFORE INSERT OR UPDATE OF sending_limits_manual ON projects
 FOR EACH ROW EXECUTE FUNCTION hedvi_guard_sending_limit_mode();

-- Invoker RLS applies even to the limits read. Entitlements may become usable
-- or be held between sends; no checkout redirect or stored plan label grants
-- paid throughput. Operator exceptions are scoped to the named project.
CREATE FUNCTION hedvi_live_limits(pid uuid, at_time timestamptz)
RETURNS TABLE(hourly int, daily int, org_hourly int, org_daily int, manual boolean)
LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,public AS $$
 WITH tier AS (
   SELECT p.hourly_limit,p.daily_limit,p.sending_limits_manual,
    coalesce((SELECT u.plan FROM public.usage_periods u WHERE u.org_id=p.org_id
      AND (u.stripe_invoice IS NOT NULL OR u.complimentary_grant IS NOT NULL)
      AND u.billing_usable AND u.period_start<=at_time AND at_time<u.period_end
      AND u.plan IN ('pro_50k','pro_100k')
      ORDER BY (u.stripe_invoice IS NOT NULL) DESC,u.period_start DESC LIMIT 1),
     CASE WHEN EXISTS (SELECT 1 FROM public.credit_lots l WHERE l.org_id=p.org_id AND l.billing_usable
       AND l.purchased_at<=at_time AND at_time<l.expires_at AND l.expired_at IS NULL
       AND l.granted>l.reserved+l.consumed+l.expired_amount) THEN 'flex' ELSE 'free' END) AS plan
   FROM public.projects p WHERE p.id=pid
 )
 SELECT CASE WHEN t.sending_limits_manual THEN t.hourly_limit ELSE n.h END,
        CASE WHEN t.sending_limits_manual THEN t.daily_limit ELSE n.d END,
        CASE WHEN t.sending_limits_manual THEN greatest(n.h,t.hourly_limit) ELSE n.h END,
        CASE WHEN t.sending_limits_manual THEN greatest(n.d,t.daily_limit) ELSE n.d END,t.sending_limits_manual
 FROM tier t JOIN (VALUES ('free',50,200),('flex',100,1000),('pro_50k',250,2500),('pro_100k',500,5000)) n(plan,h,d)
 ON n.plan=t.plan
$$;
REVOKE ALL ON FUNCTION hedvi_live_limits(uuid,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_live_limits(uuid,timestamptz) TO hedvi_api,hedvi_worker;
CREATE INDEX send_counters_org_window ON send_counters(org_id,window_kind,window_start);

-- Acceptance increments existing windows. Only retention discards them.
REVOKE UPDATE,DELETE ON send_counters FROM hedvi_api;
GRANT UPDATE(count) ON send_counters TO hedvi_api;
CREATE FUNCTION hedvi_guard_send_counter() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.count < OLD.count AND pg_has_role(current_user,'hedvi_api','USAGE') AND NOT pg_has_role(current_user,'hedvi_worker','USAGE')
 THEN RAISE insufficient_privilege; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER prevent_api_counter_reset BEFORE UPDATE OF count ON send_counters
 FOR EACH ROW EXECUTE FUNCTION hedvi_guard_send_counter();
