package e2e

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/testenv"
)

func TestPaidGuardsUpgradeBothPublishedMigration18Shapes(t *testing.T) {
	for _, alreadyHadCounter := range []bool{false, true} {
		t.Run(fmt.Sprint(alreadyHadCounter), func(t *testing.T) {
			e := testenv.New(t)
			cfg := e.WorkerPool.Config()
			cfg.ConnConfig.User, cfg.ConnConfig.Password = "hedvi_owner", "hedvi_owner_dev"
			owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			if !alreadyHadCounter {
				if _, err := owner.Exec(e.Ctx, `DROP TABLE suppression_work_counters`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := owner.Exec(e.Ctx, `DELETE FROM schema_migrations WHERE version='0019_sending_work_guards'`); err != nil {
				t.Fatal(err)
			}
			applied, err := db.Migrate(e.Ctx, owner)
			if err != nil || len(applied) != 1 || applied[0] != "0019_sending_work_guards" {
				t.Fatal("published 0018 upgrade", applied, err)
			}
			tn := e.NewTenant("free")
			if _, err := e.Accept.Send(e.Ctx, tn.Live, "upgrade", liveBody(tn, "upgrade@example.test")); err != nil {
				t.Fatal("upgraded admission", err)
			}
			if e.Count(`SELECT count(*) FROM suppression_work_counters WHERE org_id=$1`, tn.OrgID) != 1 {
				t.Fatal("counter absent after upgrade")
			}
		})
	}
}

func TestQuotaSettlementCannotDeadlockConcurrentRevocation(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',10,now()+interval '1 day')`, tn.OrgID)
	res, err := e.Accept.Send(e.Ctx, tn.Live, "revoke-race", liveBody(tn, "r@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	var rid uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM recipients WHERE email_id=$1`, res.EmailID).Scan(&rid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	revoke, err := e.WorkerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback(context.Background())
	if _, err := revoke.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, tn.OrgID); err != nil {
		t.Fatal(err)
	}
	settlement, err := e.WorkerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer settlement.Rollback(context.Background())
	pid := settlement.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		_, err := quota.Release(ctx, settlement, tn.OrgID, []uuid.UUID{rid})
		if err == nil {
			err = settlement.Commit(ctx)
		}
		done <- err
	}()
	for {
		var blocked bool
		if err := revoke.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// This is the same org -> credit ordering as a gift revoke/refund. Before
	// the fix, settlement held the credit row while waiting for the org FK.
	if _, err := revoke.Exec(ctx, `UPDATE credit_lots SET billing_usable=false WHERE org_id=$1`, tn.OrgID); err != nil {
		t.Fatal("revocation deadlocked settlement", err)
	}
	if err := revoke.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal("settlement", err)
	}
	if e.Count(`SELECT released FROM quota_reservations WHERE email_id=$1`, res.EmailID) != 1 {
		t.Fatal("reservation not released")
	}
}

func TestCreditExpiryCannotDeadlockConcurrentRevocation(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',10,now()-interval '1 day')`, tn.OrgID)
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	revoke, err := e.WorkerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke.Rollback(context.Background())
	if _, err := revoke.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, tn.OrgID); err != nil {
		t.Fatal(err)
	}
	settlement, err := e.WorkerPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer settlement.Rollback(context.Background())
	pid := settlement.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		_, err := quota.ExpireLots(ctx, settlement, time.Now())
		if err == nil {
			err = settlement.Commit(ctx)
		}
		done <- err
	}()
	for {
		var blocked bool
		if err := revoke.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1))>0`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// This is the same org -> credit ordering as a gift revoke/refund. Before
	// the fix, expiry held the credit row while waiting for the org FK.
	if _, err := revoke.Exec(ctx, `UPDATE credit_lots SET billing_usable=false WHERE org_id=$1`, tn.OrgID); err != nil {
		t.Fatal("revocation deadlocked settlement", err)
	}
	if err := revoke.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal("settlement", err)
	}
	if e.Count(`SELECT count(*) FROM credit_ledger WHERE org_id=$1 AND kind='expire' AND quantity=10`, tn.OrgID) != 1 {
		t.Fatal("credit expiry was not recorded exactly once")
	}
}

type countedPayloadReads struct {
	blob.Store
	gets atomic.Int32
}

func (s *countedPayloadReads) Get(ctx context.Context, key string) ([]byte, error) {
	s.gets.Add(1)
	return s.Store.Get(ctx, key)
}

func TestPaidWindowsRespectFreeBaselineAndManualZeroStops(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',10000,now()+interval '1 day')`, tn.OrgID)
	e.Exec(`UPDATE organizations SET plan='free' WHERE id=$1`, tn.OrgID)
	if e.Count(`SELECT count(*) FROM hedvi_live_limits_v2($1,now()) WHERE hourly=50 AND daily=200`, tn.ProjectID) != 1 {
		t.Fatal("unused Flex credit bypassed chosen Free limits")
	}
	e.Exec(`UPDATE organizations SET plan='flex' WHERE id=$1`, tn.OrgID)
	for i, pair := range [][2]int{{0, 0}, {0, 100}, {50, 0}} {
		e.Exec(`UPDATE projects SET sending_limits_manual=true,hourly_limit=$2,daily_limit=$3 WHERE id=$1`, tn.ProjectID, pair[0], pair[1])
		if _, err := e.Accept.Send(e.Ctx, tn.Live, fmt.Sprintf("zero-%d", i), liveBody(tn, "blocked@example.test")); err == nil || apiErr(t, err).Status != 403 {
			t.Fatal("legacy manual zero stop was removed", err)
		}
	}
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "automatic", liveBody(tn, "paid@example.test")); err != nil {
		t.Fatal("automatic zero was not unlimited", err)
	}
}

func TestSuppressionWorkCannotReusePaidCreditsForUnlimitedEvents(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',10000,now()+interval '1 day')`, tn.OrgID)
	late, err := e.Accept.Send(e.Ctx, tn.Live, "late", liveBody(tn, "late@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`INSERT INTO suppressions(org_id,normalized,reason,source) VALUES($1,'blocked@example.test','manual','dashboard'),($1,'late@example.test','manual','dashboard')`, tn.OrgID)
	e.Exec(`UPDATE suppression_work_counters SET count=9999 WHERE org_id=$1`, tn.OrgID)
	// Two concurrent requests compete for the final suppression-event slot.
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, err := e.Accept.Send(e.Ctx, tn.Live, fmt.Sprintf("suppressed-%d", i), liveBody(tn, "blocked@example.test"))
			errs <- err
		}(i)
	}
	accepted := 0
	for range 2 {
		err := <-errs
		if err == nil {
			accepted++
		} else if apiErr(t, err).Name != "suppression_rate_exceeded" {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("suppression processing ceiling raced")
	}
	// A suppression added after admission must finish safely, release its credit
	// and count the work even when the ceiling was reached in the meantime.
	if err := e.Sender.Handle(e.Ctx, late.EmailID); err != nil {
		t.Fatal(err)
	}
	if outbound(e) != 0 || e.Count(`SELECT count FROM suppression_work_counters WHERE org_id=$1`, tn.OrgID) != 10001 || e.Count(`SELECT released FROM quota_reservations WHERE email_id=$1`, late.EmailID) != 1 {
		t.Fatal("late suppression sent, leaked credit or escaped work accounting")
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "no-reuse", liveBody(tn, "normal@example.test", "blocked@example.test")); err == nil || apiErr(t, err).Name != "suppression_rate_exceeded" {
		t.Fatal("mixed request bypassed work guard", err)
	}
	if replay, err := e.Accept.Send(e.Ctx, tn.Live, "late", liveBody(tn, "late@example.test")); err != nil || !replay.Replayed {
		t.Fatal("suppression work guard broke replay", err)
	}
	if err := db.InOrg(e.Ctx, e.APIPool, tn.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `UPDATE suppression_work_counters SET count=0 WHERE org_id=$1`, tn.OrgID)
		return err
	}); err == nil {
		t.Fatal("API reset processing budget")
	}
}

func TestProviderPacingSerializesConcurrentReplicas(t *testing.T) {
	e := testenv.New(t)
	first, second := e.NewTenant("free"), e.NewTenant("free")
	e.Sender.ProviderRecipientsPerSecond = 1
	a, err := e.Accept.Send(e.Ctx, first.Live, "a", liveBody(first, "one@example.test", "two@example.test", "three@example.test", "four@example.test", "five@example.test", "six@example.test", "seven@example.test", "eight@example.test", "nine@example.test", "ten@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Accept.Send(e.Ctx, second.Live, "b", liveBody(second, "one@example.test", "two@example.test", "three@example.test", "four@example.test", "five@example.test", "six@example.test", "seven@example.test", "eight@example.test", "nine@example.test", "ten@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	replica := *e.Sender
	replica.WorkerID = "replica-two"
	errs := make(chan error, 2)
	go func() { errs <- e.Sender.Handle(e.Ctx, a.EmailID) }()
	go func() { errs <- replica.Handle(e.Ctx, b.EmailID) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if outbound(e) != 1 || e.Count(`SELECT count(*) FROM send_attempts`) != 1 || e.Count(`SELECT count(*) FROM emails WHERE send_state='retry_wait' AND attempt_count=0`) != 1 {
		t.Fatal("concurrent replicas consumed the same provider slot")
	}
	if e.Count(`SELECT count(*) FROM provider_send_pacing WHERE next_at>now() AND next_at<=now()+interval '10 seconds'`) != 1 {
		t.Fatal("provider spacing was not recipient weighted")
	}
}

func TestPaidCapacityAcceptsBurstsButEnforcesCreditsAndRestrictions(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	e.Exec(`INSERT INTO credit_lots(org_id,source,granted,expires_at) VALUES($1,'grant',2,now()+interval '1 day')`, tn.OrgID)
	now := time.Now().UTC()
	e.Accept.Now = func() time.Time { return now }
	// Already above even the former Pro 100k ceiling: usable credits govern
	// acceptance, not a permanent per-day partition of the bought allowance.
	for _, w := range []struct {
		kind string
		at   time.Time
	}{{"live_hour", now.Truncate(time.Hour)}, {"live_day", now.Truncate(24 * time.Hour)}} {
		e.Exec(`INSERT INTO send_counters(org_id,project_id,window_kind,window_start,count) VALUES($1,$2,$3,$4,6000)`, tn.OrgID, tn.ProjectID, w.kind, w.at)
	}
	first, err := e.Accept.Send(e.Ctx, tn.Live, "paid-burst", liveBody(tn, "first@example.test"))
	if err != nil {
		t.Fatal("paid burst refused", err)
	}
	if replay, err := e.Accept.Send(e.Ctx, tn.Live, "paid-burst", liveBody(tn, "first@example.test")); err != nil || !replay.Replayed || replay.EmailID != first.EmailID {
		t.Fatal("burst replay changed outcome", err)
	}
	// A manual restriction still applies even while usable paid credits remain.
	e.Exec(`UPDATE projects SET sending_limits_manual=true,hourly_limit=50,daily_limit=200 WHERE id=$1`, tn.ProjectID)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "restricted", liveBody(tn, "other@example.test")); err == nil || apiErr(t, err).Status != 429 {
		t.Fatal("paid credits bypassed restriction", err)
	}
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	// Two concurrent requests contend for the last unit. Exactly one succeeds.
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, err := e.Accept.Send(e.Ctx, tn.Live, fmt.Sprintf("last-%d", i), liveBody(tn, "last@example.test"))
			results <- err
		}(i)
	}
	succeeded := 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if apiErr(t, err).Status != 429 {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || e.Count(`SELECT reserved FROM credit_lots WHERE org_id=$1`, tn.OrgID) != 2 || e.Count(`SELECT count(*) FROM emails WHERE org_id=$1`, tn.OrgID) != 2 {
		t.Fatal("paid capacity overspent or lost acceptance")
	}
	if e.Count(`SELECT count(*) FROM hedvi_live_limits_v2($1,now()) WHERE hourly=50 AND daily=200`, tn.ProjectID) != 1 {
		t.Fatal("exhausted credits retained paid throughput")
	}
	if replay, err := e.Accept.Send(e.Ctx, tn.Live, "paid-burst", liveBody(tn, "first@example.test")); err != nil || !replay.Replayed {
		t.Fatal("exhaustion broke idempotency", err)
	}
}

func TestProviderPacingSharesCapacityWithoutCreatingAttempts(t *testing.T) {
	e := testenv.New(t)
	first, second := e.NewTenant("free"), e.NewTenant("free")
	e.Sender.ProviderRecipientsPerSecond = 10
	a, err := e.Accept.Send(e.Ctx, first.Live, "paced-first", liveBody(first, "a@example.test", "b@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Accept.Send(e.Ctx, second.Live, "paced-second", liveBody(second, "c@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Sender.Handle(e.Ctx, a.EmailID); err != nil {
		t.Fatal(err)
	}
	// Simulate another replica using the same clock. Keep the test deterministic
	// without sleeping or making actual provider calls.
	e.Exec(`UPDATE provider_send_pacing SET next_at=now()+interval '10 seconds'`)
	replica := *e.Sender
	replica.WorkerID = "second-replica"
	reads := &countedPayloadReads{Store: e.Blobs}
	replica.Blobs = reads
	if err = replica.Handle(e.Ctx, b.EmailID); err != nil {
		t.Fatal(err)
	}
	if outbound(e) != 1 || e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, b.EmailID) != 0 || e.Count(`SELECT attempt_count FROM emails WHERE id=$1`, b.EmailID) != 0 {
		t.Fatal("capacity wait created a provider attempt")
	}
	if reads.gets.Load() != 0 {
		t.Fatal("capacity wait fetched payload before a slot was available")
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='retry_wait' AND lease_owner IS NULL`, b.EmailID) != 1 || e.Count(`SELECT count(*) FROM quota_reservations WHERE email_id=$1 AND consumed=0 AND released=0`, b.EmailID) != 1 {
		t.Fatal("capacity wait lost the durable send or allowance")
	}
	if err := db.InOrg(e.Ctx, e.APIPool, second.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `UPDATE provider_send_pacing SET next_at=now()-interval '1 hour'`)
		return err
	}); err == nil {
		t.Fatal("API role can reset provider capacity")
	}
	e.Exec(`UPDATE provider_send_pacing SET next_at=now()-interval '1 second'`)
	e.MakeDue()
	if err = replica.Handle(e.Ctx, b.EmailID); err != nil {
		t.Fatal(err)
	}
	if outbound(e) != 2 || e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1 AND state='accepted'`, b.EmailID) != 1 {
		t.Fatal("paced work did not resume exactly once")
	}
	if err = replica.Handle(e.Ctx, b.EmailID); err != nil {
		t.Fatal(err)
	}
	if outbound(e) != 2 {
		t.Fatal("duplicate job resent paced email")
	}
}
