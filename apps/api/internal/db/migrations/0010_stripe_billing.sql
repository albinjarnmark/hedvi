-- Use one database per Stripe account and mode. Previously accepted reservations
-- retain their usage_period_id / credit_lot_id across this migration.
CREATE TABLE billing_installation (
  singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
  account text NOT NULL, live boolean NOT NULL
);
CREATE TABLE billing_customers (
  org_id uuid PRIMARY KEY REFERENCES organizations(id),
  id uuid NOT NULL UNIQUE DEFAULT uuidv7(),
  currency text NOT NULL CHECK (currency IN ('usd','sek')),
  customer_id text UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE billing_orders (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  org_id uuid NOT NULL REFERENCES billing_customers(org_id),
  request_id uuid NOT NULL,
  sku text NOT NULL CHECK (sku IN ('flex','pro_50k','pro_100k')),
  currency text NOT NULL CHECK (currency IN ('usd','sek')),
  price_id text NOT NULL,
  amount bigint NOT NULL CHECK (amount>0),
  session_id text UNIQUE, subscription_id text UNIQUE, subscription_item_id text UNIQUE,
  invoice_cursor timestamptz, subscription_status text, cancel_at timestamptz,
  checkout_consent_observed_at timestamptz,
  state text NOT NULL DEFAULT 'creating' CHECK (state IN ('creating','open','paid','active','ended','expired','review')),
  created_at timestamptz NOT NULL DEFAULT now(),
  checked_at timestamptz, next_check_at timestamptz NOT NULL DEFAULT now(),
  lease uuid, lease_until timestamptz, check_failures int NOT NULL DEFAULT 0,
  UNIQUE(org_id,request_id), UNIQUE(org_id,id)
);
CREATE UNIQUE INDEX billing_pending_order ON billing_orders(org_id,sku)
 WHERE state IN ('creating','open','review');
CREATE UNIQUE INDEX billing_pro_contract ON billing_orders(org_id)
 WHERE sku<>'flex' AND state NOT IN ('expired','ended');
CREATE INDEX billing_order_due ON billing_orders(next_check_at);
CREATE TABLE billing_invoices (
  id text PRIMARY KEY, org_id uuid NOT NULL, order_id uuid NOT NULL,
  payment_id text UNIQUE, charge_id text UNIQUE, line_id text UNIQUE,
  stripe_created_at timestamptz,
  state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','granted','held','revoked','review')),
  reason text NOT NULL DEFAULT '', paid_at timestamptz, amount_total bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  checked_at timestamptz, next_check_at timestamptz NOT NULL DEFAULT now(),
  lease uuid, lease_until timestamptz, check_failures int NOT NULL DEFAULT 0,
  FOREIGN KEY(org_id,order_id) REFERENCES billing_orders(org_id,id), UNIQUE(org_id,id)
);
CREATE INDEX billing_invoice_due ON billing_invoices(next_check_at);
CREATE TABLE billing_events (
  id text PRIMARY KEY, kind text NOT NULL, object_id text NOT NULL, customer_id text NOT NULL,
  charge_id text NOT NULL, invoice_id text NOT NULL, subscription_id text NOT NULL, order_ref text NOT NULL,
  received_at timestamptz NOT NULL DEFAULT clock_timestamp(), processed_at timestamptz
);
CREATE INDEX billing_event_due ON billing_events(received_at) WHERE processed_at IS NULL;
CREATE INDEX billing_event_invoice ON billing_events(invoice_id,received_at) WHERE invoice_id<>'';
CREATE INDEX billing_event_charge ON billing_events(charge_id,received_at) WHERE charge_id<>'';
CREATE TABLE billing_adjustments (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, org_id uuid NOT NULL,
  invoice_id text NOT NULL, prior_state text NOT NULL, new_state text NOT NULL, reason text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(org_id,invoice_id) REFERENCES billing_invoices(org_id,id)
);
ALTER TABLE usage_periods DROP CONSTRAINT usage_periods_org_id_period_start_key,
 ADD COLUMN stripe_invoice text UNIQUE,
 ADD COLUMN billing_usable boolean NOT NULL DEFAULT true,
 ADD FOREIGN KEY(org_id,stripe_invoice) REFERENCES billing_invoices(org_id,id);
CREATE UNIQUE INDEX usage_calendar_per_plan ON usage_periods(org_id,period_start,plan) WHERE stripe_invoice IS NULL;
CREATE UNIQUE INDEX usage_stripe_cycle ON usage_periods(org_id,period_start) WHERE stripe_invoice IS NOT NULL AND billing_usable;
ALTER TABLE credit_lots ADD COLUMN stripe_invoice text UNIQUE,
 ADD COLUMN billing_usable boolean NOT NULL DEFAULT true,
 ADD FOREIGN KEY(org_id,stripe_invoice) REFERENCES billing_invoices(org_id,id);

-- Existing operator-assigned plans remain the baseline. Stripe only sets a
-- Free baseline to Flex on purchase; Pro is effective only in a paid window.
CREATE FUNCTION hedvi_effective_plan(oid uuid, at_time timestamptz) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((SELECT p.plan FROM usage_periods p WHERE p.org_id=o.id AND p.stripe_invoice IS NOT NULL
    AND p.billing_usable AND p.period_start<=at_time AND at_time<p.period_end ORDER BY p.period_start DESC LIMIT 1),o.plan)
 FROM organizations o WHERE o.id=oid AND (o.id=public.hedvi_org() OR EXISTS (SELECT 1 FROM public.memberships m WHERE m.org_id=oid AND m.user_id=public.hedvi_user()) OR pg_has_role(session_user,'hedvi_worker','USAGE'))
$$;
REVOKE ALL ON FUNCTION hedvi_effective_plan(uuid,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_effective_plan(uuid,timestamptz) TO hedvi_api,hedvi_worker;
ALTER TABLE billing_customers ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_orders ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_invoices ENABLE ROW LEVEL SECURITY;
ALTER TABLE billing_adjustments ENABLE ROW LEVEL SECURITY;
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['billing_customers','billing_orders','billing_invoices','billing_adjustments'] LOOP
    EXECUTE format('CREATE POLICY tenant ON %I TO hedvi_api USING (org_id=hedvi_org()) WITH CHECK (org_id=hedvi_org())', t);
    EXECUTE format('CREATE POLICY processor ON %I TO hedvi_worker USING (true) WITH CHECK (true)', t);
  END LOOP;
END $$;
GRANT SELECT ON billing_installation,billing_customers,billing_orders,billing_invoices TO hedvi_api;
GRANT INSERT(org_id,currency),UPDATE(customer_id) ON billing_customers TO hedvi_api;
GRANT INSERT(org_id,request_id,sku,currency,price_id,amount),UPDATE(session_id) ON billing_orders TO hedvi_api;
GRANT INSERT(id,kind,object_id,customer_id,charge_id,invoice_id,subscription_id,order_ref) ON billing_events TO hedvi_api;
GRANT SELECT,INSERT ON billing_installation TO hedvi_worker;
GRANT SELECT,INSERT,UPDATE ON billing_customers,billing_orders,billing_invoices,billing_events TO hedvi_worker;
GRANT SELECT,INSERT ON billing_adjustments TO hedvi_worker;
GRANT USAGE ON SEQUENCE billing_adjustments_id_seq TO hedvi_worker;
-- Counter mutations by the request processor are necessary. A paid grant's
-- source and amount and the settlement worker's revocation flag are immutable to it.
REVOKE INSERT,UPDATE,DELETE ON credit_lots,usage_periods FROM hedvi_api;
GRANT UPDATE(reserved,data_reserved) ON credit_lots TO hedvi_api;
GRANT UPDATE(reserved,data_reserved,overage_reserved,warned_80_at,warned_95_at) ON usage_periods TO hedvi_api;
CREATE FUNCTION hedvi_calendar_period(oid uuid, at_time timestamptz) RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog, public AS $$
DECLARE start_at timestamptz; baseline text; cap bigint; data_cap bigint; result uuid;
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR oid IS NULL THEN RAISE EXCEPTION 'organization mismatch'; END IF;
 SELECT plan INTO STRICT baseline FROM public.organizations WHERE id=oid;
 start_at := date_trunc('month',at_time,'UTC');
 cap := CASE baseline WHEN 'free' THEN 5000 WHEN 'pro_50k' THEN 50000 WHEN 'pro_100k' THEN 100000 ELSE 0 END;
 data_cap := CASE baseline WHEN 'free' THEN 104857600 WHEN 'pro_50k' THEN 1073741824 WHEN 'pro_100k' THEN 2147483648 ELSE 0 END;
 INSERT INTO public.usage_periods(org_id,period_start,period_end,plan,included,data_included)
 VALUES(oid,start_at,(start_at AT TIME ZONE 'UTC' + interval '1 month') AT TIME ZONE 'UTC',baseline,cap,data_cap)
 ON CONFLICT(org_id,period_start,plan) WHERE stripe_invoice IS NULL DO NOTHING;
 SELECT id INTO STRICT result FROM public.usage_periods WHERE org_id=oid AND period_start=start_at AND plan=baseline AND stripe_invoice IS NULL;
 RETURN result;
END $$;
REVOKE ALL ON FUNCTION hedvi_calendar_period(uuid,timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_calendar_period(uuid,timestamptz) TO hedvi_api,hedvi_worker;
UPDATE organizations SET overage_enabled=false,overage_cap_recipients=0 WHERE overage_enabled OR overage_cap_recipients<>0;
UPDATE usage_periods SET overage_cap=overage_reserved+overage_consumed WHERE overage_cap<>overage_reserved+overage_consumed;

REVOKE INSERT,UPDATE,DELETE ON organizations FROM hedvi_api;
GRANT INSERT(id,name,currency),UPDATE(name,content_retention_days,metadata_retention_days,use_credits_for_overage) ON organizations TO hedvi_api;
ALTER TABLE billing_orders ADD COLUMN country text NOT NULL CHECK(country ~ '^[A-Z]{2}$');
GRANT INSERT(country) ON billing_orders TO hedvi_api;
ALTER TABLE billing_orders ADD COLUMN return_project_id uuid NOT NULL,
 ADD FOREIGN KEY(org_id,return_project_id) REFERENCES projects(org_id,id);
GRANT INSERT(return_project_id) ON billing_orders TO hedvi_api;
CREATE FUNCTION hedvi_billing_binding_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='billing_customers' THEN
   IF OLD.customer_id IS NOT NULL AND OLD.customer_id IS DISTINCT FROM NEW.customer_id THEN RAISE EXCEPTION 'immutable billing customer'; END IF;
 ELSE
   IF OLD.session_id IS NOT NULL AND OLD.session_id IS DISTINCT FROM NEW.session_id THEN RAISE EXCEPTION 'immutable checkout session'; END IF;
   IF OLD.subscription_item_id IS NOT NULL AND OLD.subscription_item_id IS DISTINCT FROM NEW.subscription_item_id THEN RAISE EXCEPTION 'immutable billing item'; END IF;
   IF OLD.subscription_id IS NOT NULL AND OLD.subscription_id IS DISTINCT FROM NEW.subscription_id THEN RAISE EXCEPTION 'immutable billing subscription'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER billing_customer_immutable BEFORE UPDATE ON billing_customers FOR EACH ROW EXECUTE FUNCTION hedvi_billing_binding_immutable();
CREATE TRIGGER billing_order_immutable BEFORE UPDATE ON billing_orders FOR EACH ROW EXECUTE FUNCTION hedvi_billing_binding_immutable();

-- The first canonical grant may be concurrent with a refund whose charge has
-- not yet been bound locally. Serialize both the invoice and charge references
-- in the same order before taking the organization's quota lock.
CREATE FUNCTION hedvi_bill_lock_refs(invoice_ref text, charge_ref text) RETURNS void
LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE ref text;
BEGIN
 FOR ref IN SELECT DISTINCT value FROM unnest(ARRAY[invoice_ref,charge_ref]) value
            WHERE value<>'' ORDER BY value LOOP
   PERFORM pg_advisory_xact_lock(hashtextextended('hedvi-billing:' || ref,0));
 END LOOP;
END $$;
REVOKE ALL ON FUNCTION hedvi_bill_lock_refs(text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_bill_lock_refs(text,text) TO hedvi_worker;

CREATE FUNCTION hedvi_hold_billing_source() RETURNS trigger LANGUAGE plpgsql
SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE org uuid; invoice text; previous text;
BEGIN
 IF NEW.kind NOT IN ('charge.refunded','refund.created','refund.updated','charge.dispute.created','charge.dispute.updated','charge.dispute.closed','credit_note.created','credit_note.updated') THEN RETURN NEW; END IF;
 PERFORM public.hedvi_bill_lock_refs(NEW.invoice_id,NEW.charge_id);
 SELECT i.org_id,i.id INTO org,invoice FROM public.billing_invoices i
 WHERE ((NEW.invoice_id<>'' AND i.id=NEW.invoice_id) OR (NEW.charge_id<>'' AND i.charge_id=NEW.charge_id))
 AND (NEW.invoice_id='' OR i.id=NEW.invoice_id)
 AND (NEW.charge_id='' OR i.charge_id IS NULL OR i.charge_id=NEW.charge_id);
 IF invoice IS NULL THEN RETURN NEW; END IF;
 PERFORM 1 FROM public.organizations WHERE id=org FOR UPDATE;
 SELECT state INTO previous FROM public.billing_invoices WHERE id=invoice FOR UPDATE;
 IF previous='granted' THEN
   UPDATE public.credit_lots SET billing_usable=false WHERE stripe_invoice=invoice;
   UPDATE public.usage_periods SET billing_usable=false WHERE stripe_invoice=invoice;
   INSERT INTO public.billing_adjustments(org_id,invoice_id,prior_state,new_state,reason)
   VALUES(org,invoice,'granted','held','payment_change_pending');
 END IF;
 UPDATE public.billing_invoices SET state=CASE WHEN state='granted' THEN 'held' ELSE state END,
   reason=CASE WHEN state='granted' THEN 'payment_change_pending' ELSE reason END,
   next_check_at=clock_timestamp(),lease=NULL,lease_until=NULL WHERE id=invoice;
 RETURN NEW;
END $$;
REVOKE ALL ON FUNCTION hedvi_hold_billing_source() FROM PUBLIC;
CREATE TRIGGER billing_hold_on_signal AFTER INSERT ON billing_events FOR EACH ROW EXECUTE FUNCTION hedvi_hold_billing_source();

-- The only public baseline mutation permits pre-existing Free or Flex accounts.
-- Paid cycles and preserved operator plans remain worker-controlled.
CREATE FUNCTION hedvi_select_billing_baseline(oid uuid, selection text) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR oid IS NULL OR selection NOT IN ('free','flex') OR selection IS NULL THEN RETURN false; END IF;
 IF NOT EXISTS(SELECT 1 FROM public.memberships WHERE org_id=oid AND user_id=public.hedvi_user() AND role IN ('owner','admin','billing')) THEN RETURN false; END IF;
 PERFORM 1 FROM public.organizations WHERE id=oid AND plan IN ('free','flex') FOR UPDATE;
 IF NOT FOUND THEN RETURN false; END IF;
 IF selection='flex' AND NOT EXISTS(SELECT 1 FROM public.credit_lots WHERE org_id=oid AND billing_usable AND expires_at>clock_timestamp() AND expired_at IS NULL AND granted>reserved+consumed+expired_amount) THEN RETURN false; END IF;
 UPDATE public.organizations SET plan=selection WHERE id=oid;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION hedvi_select_billing_baseline(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_select_billing_baseline(uuid,text) TO hedvi_api;

CREATE POLICY billing_api_reserve_movement ON credit_ledger AS RESTRICTIVE FOR INSERT TO hedvi_api
 WITH CHECK(kind='reserve' AND quantity>0 AND data_bytes>=0 AND note='' AND EXISTS(
   SELECT 1 FROM quota_reservations q
   WHERE q.org_id=credit_ledger.org_id AND q.id=credit_ledger.reservation_id
     AND q.email_id=credit_ledger.email_id AND q.source=credit_ledger.source
     AND q.credit_lot_id IS NOT DISTINCT FROM credit_ledger.credit_lot_id
     AND q.usage_period_id IS NOT DISTINCT FROM credit_ledger.usage_period_id
     AND q.quantity=credit_ledger.quantity AND q.quantity*q.data_per_unit=credit_ledger.data_bytes
 ));
CREATE UNIQUE INDEX billing_reserve_movement ON credit_ledger(reservation_id) WHERE kind='reserve';

-- Fixed-size durable windows bound provider work across replicas and restarts.
-- Each organization has two rows, and the installation owns one shared row.
CREATE TABLE billing_action_limits(scope text PRIMARY KEY, window_start timestamptz NOT NULL, used int NOT NULL CHECK(used>0));
CREATE FUNCTION hedvi_admit_billing_action(oid uuid) RETURNS boolean LANGUAGE plpgsql
SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE rule record; ts timestamptz:=clock_timestamp(); bucket timestamptz; current_count int;
BEGIN
 IF oid IS DISTINCT FROM public.hedvi_org() OR NOT EXISTS(SELECT 1 FROM public.memberships WHERE org_id=oid AND user_id=public.hedvi_user() AND role IN ('owner','admin','billing')) THEN RETURN false; END IF;
 -- The outer caller rolls back all window increments on rejection.
 FOR rule IN SELECT * FROM (VALUES (oid::text || ':10m', interval '10 minutes', 8),
   (oid::text || ':day',interval '1 day',40),('installation', interval '1 minute',30)) AS r(scope,span,cap) LOOP
   bucket:=date_bin(rule.span,ts,timestamptz '2000-01-01 00:00 UTC');
   INSERT INTO public.billing_action_limits AS b(scope,window_start,used) VALUES(rule.scope,bucket,1)
    ON CONFLICT(scope) DO UPDATE SET window_start=excluded.window_start,
    used=CASE WHEN b.window_start=excluded.window_start THEN b.used+1 ELSE 1 END
    WHERE b.window_start<excluded.window_start OR (b.window_start=excluded.window_start AND b.used<rule.cap) RETURNING used INTO current_count;
   IF NOT FOUND THEN RETURN false; END IF;
 END LOOP;
 RETURN true;
END $$;
REVOKE ALL ON FUNCTION hedvi_admit_billing_action(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hedvi_admit_billing_action(uuid) TO hedvi_api;

CREATE VIEW billing_order_status WITH (security_invoker=true) AS
 SELECT o.org_id,o.id,o.sku,o.currency,o.created_at,o.cancel_at,
 (SELECT max(p.period_end) FROM usage_periods p JOIN billing_invoices n ON n.id=p.stripe_invoice
   WHERE n.order_id=o.id AND p.org_id=o.org_id AND p.billing_usable) AS paid_through,
 CASE WHEN i.state='revoked' THEN 'reversed' WHEN i.state IN ('held','review') THEN 'payment_review'
      WHEN o.state IN ('ended','expired') THEN o.state
      WHEN o.subscription_status IN ('past_due','unpaid','paused') THEN 'past_due'
      WHEN i.state='pending' THEN 'payment_pending' ELSE o.state END AS state
 FROM billing_orders o LEFT JOIN LATERAL (SELECT b.state FROM billing_invoices b
   WHERE b.org_id=o.org_id AND b.order_id=o.id
   ORDER BY COALESCE(b.stripe_created_at,b.created_at) DESC,b.id DESC LIMIT 1) i ON true;
GRANT SELECT ON billing_order_status TO hedvi_api;
REVOKE SELECT ON billing_invoices FROM hedvi_api;
GRANT SELECT(id,org_id,order_id,paid_at,state,stripe_created_at,created_at) ON billing_invoices TO hedvi_api;
