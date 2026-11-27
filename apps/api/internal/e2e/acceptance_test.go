package e2e

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/testenv"
)

func liveBody(tn *testenv.Tenant, to ...string) []byte {
	return testenv.Body(map[string]any{"from": "App <app@" + tn.Domain + ">", "to": to, "subject": "Hej", "text": "Hej"})
}

func apiErr(t *testing.T, err error) *apierr.Error {
	t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("expected API error, got %v", err)
	}
	return ae
}

func TestIdempotencyReplayConflictAndConcurrency(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := liveBody(tn, "a@example.com")
	first, err := e.Accept.Send(e.Ctx, tn.Live, "order-1", body)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Accept.Send(e.Ctx, tn.Live, "order-1", body)
	if err != nil || !again.Replayed || again.EmailID != first.EmailID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	_, err = e.Accept.Send(e.Ctx, tn.Live, "order-1", liveBody(tn, "other@example.com"))
	if ae := apiErr(t, err); ae.Status != 409 || ae.Name != "invalid_idempotent_request" {
		t.Fatalf("conflict = %+v", ae)
	}

	// Twenty concurrent requests with one key create exactly one email.
	var wg sync.WaitGroup
	ids := make(chan uuid.UUID, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := e.Accept.Send(e.Ctx, tn.Live, "order-2", body)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- res.EmailID
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[uuid.UUID]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 || e.Count(`SELECT count(*) FROM emails WHERE idempotency_key = 'order-2'`) != 1 {
		t.Fatalf("concurrent idempotent requests created %d emails", len(seen))
	}
	// Idempotency is scoped per project: another tenant may reuse the key.
	other := e.NewTenant("free")
	if _, err := e.Accept.Send(e.Ctx, other.Live, "order-1", liveBody(other, "a@example.com")); err != nil {
		t.Fatal(err)
	}
}

func TestParallelReservationsNeverExceedQuota(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	// Accept once to create the period, then shrink the quota to 10.
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "warmup@example.com")); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE usage_periods SET included = 11 WHERE org_id = $1`, tn.OrgID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, limited := 0, 0
	for i := range 60 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, testenv.Sprintf("r%d@example.com", i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case apiErr(t, err).Name == "monthly_quota_exceeded":
				limited++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 10 || limited != 50 {
		t.Fatalf("accepted %d, limited %d; want 10 and 50", ok, limited)
	}
	if n := e.Count(`SELECT reserved + consumed FROM usage_periods WHERE org_id = $1`, tn.OrgID); n != 11 {
		t.Fatalf("period usage %d, want 11", n)
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE org_id = $1 AND billing_state = 'reserved'`, tn.OrgID); n != 11 {
		t.Fatalf("reserved recipients %d", n)
	}
}

func TestTestKeysNeverReachTheProvider(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, err := e.Accept.Send(e.Ctx, tn.Test, "", testenv.Body(map[string]any{
		"from": "x@unverified-domain.com", "to": "a@example.com", "subject": "s", "html": "<p>x</p>"}))
	if err != nil {
		t.Fatal(err)
	}
	e.Drain()
	if n := e.Count(`SELECT count(*) FROM localmock.ses_outbound`); n != 0 {
		t.Fatalf("test email reached the provider (%d)", n)
	}
	if n := e.Count(`SELECT count(*) FROM usage_periods WHERE org_id = $1 AND (reserved > 0 OR consumed > 0)`, tn.OrgID); n != 0 {
		t.Fatal("test email used live quota")
	}
	// The database refuses a send job for a test email even if code tried.
	_, err = e.WorkerPool.Exec(e.Ctx, `INSERT INTO outbox (org_id, topic, message) VALUES ($1, 'send', jsonb_build_object('email_id', $2::text))`,
		tn.OrgID, res.EmailID)
	if err == nil {
		t.Fatal("outbox accepted a send job for a test email")
	}
	_, err = e.WorkerPool.Exec(e.Ctx, `INSERT INTO send_attempts (id, org_id, email_id, worker_id, state) VALUES ($1, $2, $3, 'x', 'started')`,
		uuid.New(), tn.OrgID, res.EmailID)
	if err == nil {
		t.Fatal("send attempt allowed for a test email")
	}
	// A live-looking prefix does not matter: the row decides.
	e.Exec(`UPDATE api_keys SET mode = 'test' WHERE id = $1`, tn.Live.KeyID)
}

func TestCrashAfterPayloadUploadLeavesOnlyAnOrphan(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	e.Faults.Arm(faults.AcceptAfterBlobPut, 1)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com")); !errors.Is(err, faults.ErrCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	if e.Count(`SELECT count(*) FROM emails`) != 0 || e.Count(`SELECT count(*) FROM outbox`) != 0 {
		t.Fatal("crash before commit left an accepted email")
	}
	var keys []string
	_ = e.Blobs.List(e.Ctx, blob.PayloadPrefix, time.Now().Add(time.Hour), func(k string) error { keys = append(keys, k); return nil })
	if len(keys) != 1 {
		t.Fatalf("expected one orphan payload, got %v", keys)
	}
	e.Retention.OrphanAge = 0
	if n, err := e.Retention.SweepOrphans(e.Ctx); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	// Payloads that belong to accepted emails are never swept.
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "b@example.com")); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.Retention.SweepOrphans(e.Ctx); n != 0 {
		t.Fatal("sweeper deleted an accepted payload")
	}
}

func TestCrashBeforeCommitAcceptsNothing(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	e.Faults.Arm(faults.AcceptBeforeCommit, 1)
	_, err := e.Accept.Send(e.Ctx, tn.Live, "k", liveBody(tn, "a@example.com"))
	if !errors.Is(err, faults.ErrCrash) {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails`)+e.Count(`SELECT count(*) FROM idempotency_records`)+
		e.Count(`SELECT count(*) FROM quota_reservations`) != 0 {
		t.Fatal("partial acceptance persisted")
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "k", liveBody(tn, "a@example.com")); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
}

func TestCrashAfterCommitIsRecoveredByIdempotencyKey(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	e.Faults.Arm(faults.AcceptAfterCommit, 1)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "k", liveBody(tn, "a@example.com")); !errors.Is(err, faults.ErrCrash) {
		t.Fatal(err)
	}
	res, err := e.Accept.Send(e.Ctx, tn.Live, "k", liveBody(tn, "a@example.com"))
	if err != nil || !res.Replayed {
		t.Fatalf("retry = %+v %v", res, err)
	}
	if e.Count(`SELECT count(*) FROM emails`) != 1 {
		t.Fatal("retry after lost response created a duplicate")
	}
}

func TestSuppressedRecipientsAreNotChargedOrSent(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	e.Exec(`INSERT INTO suppressions (org_id, normalized, reason, source) VALUES ($1, 'blocked@example.com', 'manual', 'dashboard')`, tn.OrgID)
	res, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "Blocked@Example.com", "ok@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1 AND status = 'suppressed' AND billing_state = 'none'`, res.EmailID); n != 1 {
		t.Fatal("suppressed recipient was charged or queued")
	}
	e.Drain()
	var rcpts []string
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT recipients FROM localmock.ses_outbound`).Scan(&rcpts); err != nil {
		t.Fatal(err)
	}
	if len(rcpts) != 1 || rcpts[0] != "ok@example.com" {
		t.Fatalf("envelope %v", rcpts)
	}
	// All recipients suppressed: accepted for visibility, never sent.
	res2, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "blocked@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM emails WHERE id = $1 AND send_state = 'skipped' AND last_event = 'suppressed'`, res2.EmailID); n != 1 {
		t.Fatal("fully suppressed email not skipped")
	}
}

func TestLiveSendingRequiresApprovalAndVerifiedOwnDomain(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	_, err := e.Accept.Send(e.Ctx, a.Live, "", liveBody(b, "x@example.com")) // b's domain
	if ae := apiErr(t, err); ae.Status != 403 {
		t.Fatalf("sending from another tenant's domain: %+v", ae)
	}
	e.Exec(`UPDATE projects SET sending_status = 'pending_review' WHERE id = $1`, a.ProjectID)
	_, err = e.Accept.Send(e.Ctx, a.Live, "", liveBody(a, "x@example.com"))
	if ae := apiErr(t, err); ae.Status != 403 {
		t.Fatalf("unapproved project: %+v", ae)
	}
	if _, err := e.Accept.Send(e.Ctx, a.Test, "", liveBody(a, "x@example.com")); err != nil {
		t.Fatalf("test key must work before approval: %v", err)
	}
}

func TestRateWindowsAreAtomic(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	e.Exec(`UPDATE projects SET hourly_limit = 5 WHERE id = $1`, tn.ProjectID)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, testenv.Sprintf("h%d@example.com", i))); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ok != 5 {
		t.Fatalf("hourly limit allowed %d", ok)
	}
}

func TestRowLevelSecurityIsolatesTenants(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	if _, err := e.Accept.Send(e.Ctx, a.Live, "", liveBody(a, "x@example.com")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Accept.Send(e.Ctx, b.Live, "", liveBody(b, "y@example.com")); err != nil {
		t.Fatal(err)
	}
	count := func(org *uuid.UUID, table string) int {
		var n int
		run := func(tx pgx.Tx) error { return tx.QueryRow(e.Ctx, `SELECT count(*) FROM `+table).Scan(&n) }
		var err error
		if org == nil {
			err = db.Tx(e.Ctx, e.APIPool, run)
		} else {
			err = db.InOrg(e.Ctx, e.APIPool, *org, run)
		}
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"emails", "recipients", "outbox", "quota_reservations", "api_keys", "domains", "projects", "credit_ledger"} {
		if got := count(&a.OrgID, table); got == 0 {
			t.Fatalf("%s: tenant sees none of its own rows", table)
		}
		if got := count(nil, table); got != 0 {
			t.Fatalf("%s: API role without tenant context sees %d rows", table, got)
		}
	}
	if count(&a.OrgID, "emails") != 1 || count(&b.OrgID, "emails") != 1 {
		t.Fatal("tenant sees another tenant's emails")
	}
	// Writing a row for another tenant is refused by the policy.
	err := db.InOrg(e.Ctx, e.APIPool, a.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `INSERT INTO suppressions (org_id, normalized, reason, source) VALUES ($1, 'x@y.se', 'manual', 'api')`, b.OrgID)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant insert allowed")
	}
	// Updates cannot touch other tenants either.
	err = db.InOrg(e.Ctx, e.APIPool, a.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(e.Ctx, `UPDATE projects SET sending_status = 'paused' WHERE id = $1`, b.ProjectID)
		if err == nil && tag.RowsAffected() != 0 {
			return errors.New("updated another tenant's project")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
