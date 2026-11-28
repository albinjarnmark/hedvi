package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

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
	if err = replica.Handle(e.Ctx, b.EmailID); err != nil {
		t.Fatal(err)
	}
	if outbound(e) != 1 || e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, b.EmailID) != 0 || e.Count(`SELECT attempt_count FROM emails WHERE id=$1`, b.EmailID) != 0 {
		t.Fatal("capacity wait created a provider attempt")
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
