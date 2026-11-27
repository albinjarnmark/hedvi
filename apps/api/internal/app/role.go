package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckAPIRole fails startup if a misconfigured API login can bypass tenant
// policies by ownership, privileged attributes, role membership or disabled RLS.
func CheckAPIRole(ctx context.Context, pool *pgxpool.Pool) error {
	var safe bool
	err := pool.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM pg_roles r WHERE pg_has_role(current_user,r.oid,'MEMBER') AND (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolname='hedvi_worker'))
 AND pg_has_role(current_user,'hedvi_api','USAGE')
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND pg_has_role(current_user,c.relowner,'MEMBER'))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname IN ('organizations','memberships','projects','domains','api_keys','emails','recipients','webhooks','webhook_events','webhook_deliveries','suppressions','audit_log','usage_periods','credit_lots','credit_ledger','quota_reservations','send_attempts','idempotency_records','send_counters','outbox','email_events','alerts','billing_customers','billing_orders','billing_invoices','billing_adjustments') AND NOT c.relrowsecurity)
 AND current_setting('row_security')='on'
 AND NOT has_table_privilege(current_user,'public.schema_migrations','INSERT,UPDATE,DELETE')`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("API database role must enforce tenant RLS and have no worker, owner, superuser or BYPASSRLS privileges")
	}
	return nil
}
