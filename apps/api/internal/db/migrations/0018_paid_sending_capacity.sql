-- Zero means no plan-level hourly/daily admission cap. Commercial recipient
-- and attachment reservations, Free limits, abuse holds and explicit operator
-- restrictions remain authoritative. Keep v1 intact for rolling deployment.
CREATE FUNCTION hedvi_live_limits_v2(pid uuid, at_time timestamptz)
RETURNS TABLE(hourly int, daily int, org_hourly int, org_daily int, manual boolean)
LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,public AS $$
 WITH tier AS (
   SELECT p.hourly_limit,p.daily_limit,p.sending_limits_manual,
    coalesce((SELECT u.plan FROM public.usage_periods u WHERE u.org_id=p.org_id
      AND (u.stripe_invoice IS NOT NULL OR u.complimentary_grant IS NOT NULL)
      AND u.billing_usable AND u.period_start<=at_time AND at_time<u.period_end
      AND u.plan IN ('pro_50k','pro_100k')
      ORDER BY (u.stripe_invoice IS NOT NULL) DESC,u.period_start DESC LIMIT 1),
     CASE WHEN public.hedvi_effective_plan(p.org_id,at_time)='flex' AND EXISTS (SELECT 1 FROM public.credit_lots l WHERE l.org_id=p.org_id AND l.billing_usable
       AND l.purchased_at<=at_time AND at_time<l.expires_at AND l.expired_at IS NULL
       AND l.granted>l.reserved+l.consumed+l.expired_amount) THEN 'flex' ELSE 'free' END) AS plan
   FROM public.projects p WHERE p.id=pid
 )
 SELECT CASE WHEN t.sending_limits_manual THEN coalesce(nullif(t.hourly_limit,0),-1) ELSE n.h END,
        CASE WHEN t.sending_limits_manual THEN coalesce(nullif(t.daily_limit,0),-1) ELSE n.d END,
        CASE WHEN t.sending_limits_manual AND n.h>0 THEN greatest(n.h,t.hourly_limit) ELSE n.h END,
        CASE WHEN t.sending_limits_manual AND n.d>0 THEN greatest(n.d,t.daily_limit) ELSE n.d END,t.sending_limits_manual
 FROM tier t JOIN (VALUES ('free',50,200),('flex',0,0),('pro_50k',0,0),('pro_100k',0,0)) n(plan,h,d)
 ON n.plan=t.plan
$$;
REVOKE ALL ON FUNCTION hedvi_live_limits_v2(uuid,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_live_limits_v2(uuid,timestamptz) TO hedvi_api,hedvi_worker;

-- Shared across worker replicas in this environment. Only the worker can pace
-- provider submissions; the API cannot reset this clock or read other traffic.
CREATE TABLE provider_send_pacing (
  name text PRIMARY KEY,
  next_at timestamptz NOT NULL
);
REVOKE ALL ON provider_send_pacing FROM PUBLIC, hedvi_api;
GRANT SELECT,INSERT,UPDATE ON provider_send_pacing TO hedvi_worker;

-- Bound free suppression-event work separately from paid sending capacity.
-- Late suppressions count too, so repeatedly releasing paid credits cannot
-- bypass this protection. A worker may finish accepted work above the ceiling;
-- new live admission then waits until the next UTC day.
CREATE TABLE suppression_work_counters (
  org_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  window_start timestamptz NOT NULL,
  count bigint NOT NULL CHECK(count>=0),
  PRIMARY KEY(org_id,window_start)
);
ALTER TABLE suppression_work_counters ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON suppression_work_counters TO hedvi_api
 USING(org_id=hedvi_org()) WITH CHECK(org_id=hedvi_org());
CREATE POLICY worker_access ON suppression_work_counters TO hedvi_worker USING(true) WITH CHECK(true);
REVOKE ALL ON suppression_work_counters FROM PUBLIC,hedvi_api,hedvi_worker;
GRANT SELECT,INSERT ON suppression_work_counters TO hedvi_api,hedvi_worker;
GRANT UPDATE(count) ON suppression_work_counters TO hedvi_api,hedvi_worker;
GRANT DELETE ON suppression_work_counters TO hedvi_worker;
CREATE TRIGGER prevent_api_suppression_counter_reset BEFORE UPDATE OF count ON suppression_work_counters
 FOR EACH ROW EXECUTE FUNCTION hedvi_guard_send_counter();
