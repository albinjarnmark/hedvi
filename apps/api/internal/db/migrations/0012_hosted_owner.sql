-- A migration never appoints an owner. The operator verifies one exact account
-- through the CLI before the hosted console can be unlocked.
CREATE UNIQUE INDEX users_single_instance_owner ON users(is_staff) WHERE is_staff;
REVOKE INSERT, UPDATE, DELETE ON users FROM hedvi_api;
GRANT INSERT(id,email,name,password_hash,locale,email_verified_at) ON users TO hedvi_api;
GRANT UPDATE(name,locale,password_hash,email_verified_at,credential_version,totp_secret_enc,
 totp_enabled_at,totp_last_step,failed_logins,locked_until,login_failure_window_started_at,
 failed_mfa,mfa_failure_window_started_at,mfa_locked_until) ON users TO hedvi_api;

CREATE TABLE instance_owner (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton), user_id uuid NOT NULL UNIQUE REFERENCES users(id),
 email text NOT NULL, enrollment_hash bytea NOT NULL CHECK(octet_length(enrollment_hash)=32),
 owner_secret_enc bytea NOT NULL CHECK(octet_length(owner_secret_enc)>32),
 last_step bigint NOT NULL DEFAULT 0, failures integer NOT NULL DEFAULT 0,
 window_at timestamptz, locked_until timestamptz
);
CREATE TABLE owner_sessions (
 token_hash bytea PRIMARY KEY, session_id uuid NOT NULL UNIQUE REFERENCES sessions(id) ON DELETE CASCADE,
 user_id uuid NOT NULL REFERENCES instance_owner(user_id) ON DELETE CASCADE,
 expires_at timestamptz NOT NULL, credential_version bigint NOT NULL
);
CREATE TABLE complimentary_memberships (
 id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id),
 plan text NOT NULL CHECK(plan IN ('flex','pro_50k','pro_100k')),
 granted_by uuid NOT NULL REFERENCES users(id), reason text NOT NULL CHECK(length(reason) BETWEEN 10 AND 500),
 starts_at timestamptz NOT NULL DEFAULT now(), ends_at timestamptz NOT NULL,
 revoked_at timestamptz, revoke_reason text, CHECK(ends_at>starts_at), UNIQUE(org_id,id)
);
CREATE INDEX complimentary_memberships_org ON complimentary_memberships(org_id,starts_at DESC);
GRANT SELECT,INSERT,UPDATE,DELETE ON instance_owner,owner_sessions TO hedvi_worker;
GRANT SELECT,INSERT,UPDATE ON complimentary_memberships TO hedvi_worker;
REVOKE ALL ON instance_owner,owner_sessions,complimentary_memberships FROM PUBLIC,hedvi_api;
ALTER TABLE usage_periods ADD COLUMN complimentary_grant uuid UNIQUE,
 ADD FOREIGN KEY(org_id,complimentary_grant) REFERENCES complimentary_memberships(org_id,id),
 ADD CHECK(complimentary_grant IS NULL OR stripe_invoice IS NULL);
ALTER TABLE credit_lots ADD COLUMN complimentary_grant uuid UNIQUE,
 ADD FOREIGN KEY(org_id,complimentary_grant) REFERENCES complimentary_memberships(org_id,id),
 ADD CHECK(complimentary_grant IS NULL OR (source='grant' AND stripe_invoice IS NULL));
DROP INDEX usage_calendar_per_plan;
CREATE UNIQUE INDEX usage_calendar_per_plan ON usage_periods(org_id,period_start,plan)
 WHERE stripe_invoice IS NULL AND complimentary_grant IS NULL;

CREATE POLICY api_audit_actor ON audit_log AS RESTRICTIVE FOR INSERT TO hedvi_api
 WITH CHECK (actor_kind='user' AND action NOT IN ('membership.granted','membership.revoked','membership.superseded','project.approved','project.paused','user.staff_granted','user.staff_revoked','owner.unlocked'));

CREATE OR REPLACE FUNCTION hedvi_calendar_period(oid uuid, at_time timestamptz) RETURNS uuid
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
 ON CONFLICT(org_id,period_start,plan) WHERE stripe_invoice IS NULL AND complimentary_grant IS NULL DO NOTHING;
 SELECT id INTO STRICT result FROM public.usage_periods WHERE org_id=oid AND period_start=start_at AND plan=baseline AND stripe_invoice IS NULL AND complimentary_grant IS NULL;
 RETURN result;
END $$;

CREATE OR REPLACE FUNCTION hedvi_effective_plan(oid uuid, at_time timestamptz) RETURNS text
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,public AS $$
 SELECT COALESCE((SELECT p.plan FROM usage_periods p WHERE p.org_id=o.id AND (p.stripe_invoice IS NOT NULL OR p.complimentary_grant IS NOT NULL)
    AND p.billing_usable AND p.period_start<=at_time AND at_time<p.period_end ORDER BY (p.stripe_invoice IS NOT NULL) DESC,p.period_start DESC LIMIT 1),o.plan)
 FROM organizations o WHERE o.id=oid AND (o.id=public.hedvi_org() OR EXISTS (SELECT 1 FROM public.memberships m WHERE m.org_id=oid AND m.user_id=public.hedvi_user()) OR pg_has_role(session_user,'hedvi_worker','USAGE'))
$$;

-- A canonical paid Pro invoice permanently supersedes an overlapping gift Pro
-- period. Both writers hold the organization lock. Refunds never reopen gifts.
CREATE FUNCTION hedvi_paid_supersedes_pro_gift() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE gift record;
BEGIN
 FOR gift IN SELECT id FROM public.complimentary_memberships
  WHERE org_id=NEW.org_id AND plan IN ('pro_50k','pro_100k') AND revoked_at IS NULL
    AND NEW.period_end>now() AND starts_at<NEW.period_end AND ends_at>NEW.period_start AND ends_at>now() FOR UPDATE LOOP
  -- A prepaid future cycle takes over at its start. Shorten the gift now so a
  -- later payment reversal cannot reopen the overlapping interval.
  IF NEW.period_start>now() THEN
   UPDATE public.complimentary_memberships SET ends_at=NEW.period_start WHERE id=gift.id;
   UPDATE public.usage_periods SET period_end=NEW.period_start WHERE complimentary_grant=gift.id;
  ELSE
   UPDATE public.complimentary_memberships SET revoked_at=now(),revoke_reason='paid_subscription' WHERE id=gift.id;
   UPDATE public.usage_periods SET billing_usable=false WHERE complimentary_grant=gift.id;
  END IF;
  INSERT INTO public.audit_log(org_id,actor_kind,action,target_type,target_id,metadata)
    VALUES(NEW.org_id,'system','membership.superseded','complimentary_membership',gift.id::text,
      jsonb_build_object('reason','paid_subscription','effective_at',greatest(now(),NEW.period_start)));
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER paid_supersedes_pro_gift AFTER INSERT ON usage_periods
 FOR EACH ROW WHEN (NEW.stripe_invoice IS NOT NULL) EXECUTE FUNCTION hedvi_paid_supersedes_pro_gift();

-- The API may request live review. Decisions and delivery ceilings are operator-owned.
CREATE FUNCTION hedvi_customer_project_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF pg_has_role(current_user,'hedvi_api','USAGE') AND NOT pg_has_role(current_user,'hedvi_worker','USAGE') THEN
  IF TG_OP='INSERT' THEN
   IF NEW.sending_status<>'test_only' OR NEW.hourly_limit<>25 OR NEW.daily_limit<>100 THEN RAISE insufficient_privilege; END IF;
  ELSIF (OLD.hourly_limit,OLD.daily_limit,OLD.ses_tenant_name) IS DISTINCT FROM
        (NEW.hourly_limit,NEW.daily_limit,NEW.ses_tenant_name) OR
        (NEW.sending_status IS DISTINCT FROM OLD.sending_status AND
         (OLD.sending_status<>'test_only' OR NEW.sending_status<>'pending_review')) THEN
   RAISE insufficient_privilege;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER customer_project_transition BEFORE INSERT OR UPDATE OF sending_status,hourly_limit,daily_limit,ses_tenant_name
 ON projects FOR EACH ROW EXECUTE FUNCTION hedvi_customer_project_transition();

CREATE FUNCTION hedvi_drop_owner_session() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='sessions' THEN
  DELETE FROM public.owner_sessions WHERE session_id=NEW.id;
 ELSE
  DELETE FROM public.owner_sessions WHERE user_id=NEW.id;
  IF EXISTS(SELECT 1 FROM public.instance_owner WHERE user_id=NEW.id) THEN
   UPDATE public.sessions SET revoked_at=clock_timestamp() WHERE user_id=NEW.id AND revoked_at IS NULL;
  END IF;
 END IF;
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION hedvi_drop_owner_session() FROM PUBLIC,hedvi_api;
CREATE TRIGGER revoke_owner_session AFTER UPDATE OF revoked_at,token_hash,user_id,mfa_verified_at ON sessions
 FOR EACH ROW WHEN (NEW.revoked_at IS NOT NULL OR
 (NEW.token_hash,NEW.user_id,NEW.mfa_verified_at) IS DISTINCT FROM (OLD.token_hash,OLD.user_id,OLD.mfa_verified_at))
 EXECUTE FUNCTION hedvi_drop_owner_session();
CREATE TRIGGER revoke_owner_credentials AFTER UPDATE OF password_hash,totp_secret_enc,credential_version,is_staff,email,email_verified_at,totp_enabled_at ON users
 FOR EACH ROW WHEN ((NEW.password_hash,NEW.totp_secret_enc,NEW.credential_version,NEW.is_staff,NEW.email,NEW.email_verified_at,NEW.totp_enabled_at) IS DISTINCT FROM
  (OLD.password_hash,OLD.totp_secret_enc,OLD.credential_version,OLD.is_staff,OLD.email,OLD.email_verified_at,OLD.totp_enabled_at))
 EXECUTE FUNCTION hedvi_drop_owner_session();
CREATE INDEX owner_recent_users ON users(created_at DESC,id DESC);
CREATE INDEX owner_recent_grants ON complimentary_memberships(starts_at DESC,id);
CREATE INDEX owner_open_alerts ON alerts(created_at DESC) WHERE resolved_at IS NULL;
CREATE INDEX owner_recent_send_errors ON send_attempts(started_at DESC) WHERE state IN('rejected','retryable','unknown');
CREATE INDEX owner_recent_auth_errors ON auth_mail_outbox(created_at DESC) WHERE state IN('unknown','failed');
CREATE INDEX owner_recent_webhook_errors ON webhook_deliveries(created_at DESC) WHERE state='failed';
CREATE INDEX owner_project_review_order ON projects((sending_status='pending_review') DESC,status_changed_at DESC,id)
 WHERE sending_status IN('pending_review','paused','approved');

CREATE INDEX owner_search_account ON users((lower(email) COLLATE "C"),id);
CREATE INDEX owner_search_organization ON organizations((lower(name) COLLATE "C"),id);
CREATE INDEX owner_search_memberships ON memberships(user_id,org_id) WHERE role='owner';
