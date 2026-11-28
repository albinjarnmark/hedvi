package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

func TestLiveLimitsFollowUsableSourcesAndOwnerExceptions(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	limits := func(wantH, wantD, wantOrgH, wantOrgD int, manual bool) {
		t.Helper()
		if n := e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()) WHERE hourly=$2 AND daily=$3 AND org_hourly=$4 AND org_daily=$5 AND manual=$6`, tn.ProjectID, wantH, wantD, wantOrgH, wantOrgD, manual); n != 1 {
			t.Fatal("unexpected effective limits", wantH, wantD, wantOrgH, wantOrgD, manual)
		}
	}
	// An empty Flex baseline or an open checkout does not grant throughput.
	limits(50, 200, 50, 200, false)
	e.Exec(`INSERT INTO billing_customers(org_id,currency,billing_profile) VALUES($1,'sek','{"name":"test","line1":"test","line2":"","city":"Test","postal_code":"11111","state":"","country":"SE"}')`, tn.OrgID)
	order := uuid.New()
	e.Exec(`INSERT INTO billing_orders(id,org_id,request_id,sku,currency,price_id,amount,state,country,return_project_id) VALUES($1,$2,$3,'pro_50k','sek','fixture_price',12900,'open','SE',$4)`, order, tn.OrgID, uuid.New(), tn.ProjectID)
	limits(50, 200, 50, 200, false)
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',10000,now()+interval '1 day')`, tn.OrgID)
	limits(100, 1000, 100, 1000, false)
	e.Exec(`INSERT INTO billing_invoices(id,org_id,order_id,state) VALUES('capacity_invoice',$1,$2,'granted')`, tn.OrgID, order)
	e.Exec(`INSERT INTO usage_periods(org_id,period_start,period_end,plan,included,data_included,stripe_invoice) VALUES($1,now()-interval '1 hour',now()+interval '27 days','pro_50k',50000,1073741824,'capacity_invoice')`, tn.OrgID)
	limits(250, 2500, 250, 2500, false)
	e.Exec(`UPDATE usage_periods SET plan='pro_100k' WHERE stripe_invoice='capacity_invoice'`)
	limits(500, 5000, 500, 5000, false)
	if e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()+interval '28 days') WHERE hourly=50 AND daily=200`, tn.ProjectID) != 1 {
		t.Fatal("expired paid cycle or Flex lot extended throughput")
	}
	e.Exec(`UPDATE projects SET sending_limits_manual=true,hourly_limit=25,daily_limit=100 WHERE id=$1`, tn.ProjectID)
	limits(25, 100, 500, 5000, true)
	if err := db.InOrg(e.Ctx, e.APIPool, tn.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
		return err
	}); err == nil {
		t.Fatal("API removed owner limit")
	}
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	e.Exec(`UPDATE usage_periods SET billing_usable=false WHERE stripe_invoice='capacity_invoice'`)
	limits(100, 1000, 100, 1000, false)
	e.Exec(`UPDATE credit_lots SET reserved=granted WHERE org_id=$1`, tn.OrgID)
	limits(50, 200, 50, 200, false)
	e.Exec(`UPDATE credit_lots SET reserved=0,consumed=granted WHERE org_id=$1`, tn.OrgID)
	limits(50, 200, 50, 200, false)
	e.Exec(`UPDATE credit_lots SET consumed=0,billing_usable=false WHERE org_id=$1`, tn.OrgID)
	limits(50, 200, 50, 200, false)
	e.Exec(`UPDATE credit_lots SET billing_usable=true,purchased_at=now()-interval '2 days',expires_at=now()-interval '1 day' WHERE org_id=$1`, tn.OrgID)
	limits(50, 200, 50, 200, false)

	// New billing capacity cannot release an organization's abuse restriction.
	e.Exec(`UPDATE organizations SET risk_status='restricted' WHERE id=$1`, tn.OrgID)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "risk-restricted", liveBody(tn, "restricted@example.test")); err == nil || apiErr(t, err).Status != 403 {
		t.Fatal("restricted organization could accept live mail", err)
	}
	other := e.NewTenant("free")
	if err := db.InOrg(e.Ctx, e.APIPool, other.OrgID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(e.Ctx, `SELECT count(*) FROM hedvi_live_limits($1,now())`, tn.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("cross-org limit lookup disclosed another project's membership")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPlanLimitSerializesIndependentProjects(t *testing.T) {
	e := testenv.New(t)
	first := e.NewTenant("free")
	second := &testenv.Tenant{OrgID: first.OrgID, Domain: "capacity-second.test"}
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, first.ProjectID)
	if err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
		id, err := console.NewProject(e.Ctx, tx, first.OrgID, "Second project", config.EnvTest)
		if err != nil {
			return err
		}
		second.ProjectID = id
		second.Live = accept.Principal{ProjectID: id, OrgID: first.OrgID, Mode: "live", Permission: "full_access"}
		if _, err = tx.Exec(e.Ctx, `UPDATE projects SET sending_status='approved' WHERE id=$1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(e.Ctx, `INSERT INTO domains(org_id,project_id,name,status,verified_at) VALUES($1,$2,$3,'verified',now())`, first.OrgID, id, second.Domain)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	e.Accept.Now = func() time.Time { return now }
	e.Accept.LoopThreshold = 0
	for _, window := range []string{"live_hour", "live_day"} {
		start := now.Truncate(time.Hour)
		n := 49
		if window == "live_day" {
			start = now.Truncate(24 * time.Hour)
			n = 199
		}
		e.Exec(`INSERT INTO send_counters(org_id,project_id,window_kind,window_start,count) VALUES($1,$2,$3,$4,$5)`, first.OrgID, first.ProjectID, window, start, n/2)
		e.Exec(`INSERT INTO send_counters(org_id,project_id,window_kind,window_start,count) VALUES($1,$2,$3,$4,$5)`, first.OrgID, second.ProjectID, window, start, n-n/2)
	}
	ctx, cancel := context.WithTimeout(e.Ctx, 15*time.Second)
	defer cancel()
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	e.Accept.Blobs = &observedPayloadStore{Store: e.Blobs, arrived: arrived, release: release}
	type outcome struct {
		tenant *testenv.Tenant
		result *accept.Result
		err    error
	}
	results := make(chan outcome, 2)
	for _, tn := range []*testenv.Tenant{first, second} {
		go func() {
			res, err := e.Accept.Send(ctx, tn.Live, "across-projects", liveBody(tn, "race@example.test"))
			results <- outcome{tn, res, err}
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			close(release)
			t.Fatal("contenders did not reach post-preflight upload")
		}
	}
	close(release)
	var winner *testenv.Tenant
	for range 2 {
		select {
		case x := <-results:
			if x.err != nil {
				if apiErr(t, x.err).Status != 429 {
					t.Fatal(x.err)
				}
			} else {
				if winner != nil || x.result == nil {
					t.Fatal("both projects exceeded aggregate capacity")
				}
				winner = x.tenant
			}
		case <-ctx.Done():
			t.Fatal("concurrent acceptance timed out")
		}
	}
	if winner == nil || e.Count(`SELECT sum(count) FROM send_counters WHERE org_id=$1 AND window_kind='live_hour'`, first.OrgID) != 50 || e.Count(`SELECT sum(count) FROM send_counters WHERE org_id=$1 AND window_kind='live_day'`, first.OrgID) != 200 {
		t.Fatal("shared counters or admission incorrect")
	}
	for _, sql := range []string{
		`DELETE FROM send_counters WHERE org_id=$1`,
		`UPDATE send_counters SET count=0 WHERE org_id=$1`,
		`UPDATE send_counters SET window_start=window_start-interval '2 days' WHERE org_id=$1`,
	} {
		if err := db.InOrg(ctx, e.APIPool, first.OrgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, first.OrgID)
			return err
		}); err == nil {
			t.Fatal("API role reset a shared window", sql)
		}
	}
	res, err := e.Accept.Send(ctx, winner.Live, "across-projects", liveBody(winner, "race@example.test"))
	if err != nil || !res.Replayed || e.Count(`SELECT count(*) FROM quota_reservations WHERE org_id=$1`, first.OrgID) != 1 {
		t.Fatal("idempotent replay at capacity", err)
	}
	// An elevated operator exception may use its own reviewed ceiling without
	// letting a sibling inherit the exception. Both still see the same counters.
	e.Accept.Blobs = e.Blobs
	e.Exec(`UPDATE projects SET sending_limits_manual=true,hourly_limit=60,daily_limit=220 WHERE id=$1`, first.ProjectID)
	if _, err = e.Accept.Send(ctx, first.Live, "reviewed", liveBody(first, "reviewed@example.test")); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Accept.Send(ctx, second.Live, "regular", liveBody(second, "regular@example.test")); err == nil || apiErr(t, err).Status != 429 {
		t.Fatal("owner exception crossed project boundary", err)
	}
}

func TestPlanLimitsUpgradePreservesReviewedExceptions(t *testing.T) {
	e := testenv.New(t)
	ordinary, manual, reduced := e.NewTenant("free"), e.NewTenant("free"), e.NewTenant("free")
	cfg := e.WorkerPool.Config()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "hedvi_owner", "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err = owner.Exec(e.Ctx, `DROP TRIGGER protect_sending_limit_mode ON projects;
	DROP FUNCTION hedvi_guard_sending_limit_mode(); DROP FUNCTION hedvi_live_limits(uuid,timestamptz);
	DROP TRIGGER prevent_api_counter_reset ON send_counters; DROP FUNCTION hedvi_guard_send_counter();
	ALTER TABLE projects DROP COLUMN sending_limits_manual; DROP INDEX send_counters_org_window;
	DELETE FROM schema_migrations WHERE version='0017_plan_sending_limits'`); err != nil {
		t.Fatal(err)
	}
	for _, tn := range []*testenv.Tenant{ordinary, reduced} {
		e.Exec(`UPDATE projects SET hourly_limit=25,daily_limit=100 WHERE id=$1`, tn.ProjectID)
		e.Exec(`INSERT INTO audit_log(org_id,actor_kind,action,target_type,target_id,metadata) VALUES($1,'staff','project.approved','project',$2,'{"hourly_limit":25,"daily_limit":100}')`, tn.OrgID, tn.ProjectID.String())
	}
	e.Exec(`INSERT INTO audit_log(org_id,actor_kind,action,target_type,target_id,metadata,created_at) VALUES($1,'system','project.approved','project',$2,'{"hourly":75,"daily":300}',now()-interval '10 days')`, reduced.OrgID, reduced.ProjectID.String())
	e.Exec(`UPDATE projects SET hourly_limit=11,daily_limit=70,sending_status='paused' WHERE id=$1`, manual.ProjectID)
	applied, err := db.Migrate(e.Ctx, owner)
	if err != nil || len(applied) != 1 || applied[0] != "0017_plan_sending_limits" {
		t.Fatal("migration", applied, err)
	}
	if e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()) WHERE NOT manual AND hourly=50 AND daily=200`, ordinary.ProjectID) != 1 ||
		e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()) WHERE manual AND hourly=25 AND daily=100`, reduced.ProjectID) != 1 ||
		e.Count(`SELECT count(*) FROM projects p CROSS JOIN LATERAL hedvi_live_limits(p.id,now()) l WHERE p.id=$1 AND p.sending_status='paused' AND l.manual AND l.hourly=11 AND l.daily=70`, manual.ProjectID) != 1 {
		t.Fatal("migration lost owner intent or preserved legacy form defaults as a permanent cap")
	}
}
