package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

type observedPayloadStore struct {
	blob.Store
	puts    atomic.Int32
	arrived chan<- struct{}
	release <-chan struct{}
}

func (s *observedPayloadStore) Put(ctx context.Context, key string, data []byte) error {
	s.puts.Add(1)
	if s.arrived != nil {
		select {
		case s.arrived <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.Store.Put(ctx, key, data)
}

func storedObjects(t *testing.T, e *testenv.Env) int {
	t.Helper()
	var count int
	if err := e.Blobs.List(e.Ctx, blob.PayloadPrefix, time.Now().Add(time.Minute), func(string) error {
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLiveAcceptanceBudgetsCountSuppressedRecipients(t *testing.T) {
	e := testenv.New(t)
	at := time.Now()
	e.Accept.Now = func() time.Time { return at }
	tn := e.NewTenant("free")
	e.Exec(`UPDATE projects SET hourly_limit=3, daily_limit=4 WHERE id=$1`, tn.ProjectID)
	e.Exec(`INSERT INTO suppressions (org_id,normalized,reason,source)
		VALUES ($1,'a@example.com','manual','dashboard'),($1,'b@example.com','manual','dashboard')`, tn.OrgID)
	e.Exec(`INSERT INTO webhooks (id,org_id,project_id,url,events,secret_enc)
		VALUES ($1,$2,$3,'https://example.com/hook','{email.suppressed}',$4)`, uuid.New(), tn.OrgID, tn.ProjectID, []byte("fixture"))
	store := &observedPayloadStore{Store: e.Blobs}
	e.Accept.Blobs = store
	mixed := liveBody(tn, "a@example.com", "b@example.com", "ok@example.com")
	first, err := e.Accept.Send(e.Ctx, tn.Live, "mixed", mixed)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Accept.Send(e.Ctx, tn.Live, "mixed", mixed)
	if err != nil || !again.Replayed || first.EmailID != again.EmailID {
		t.Fatalf("replay at full hourly limit: %+v, %v", again, err)
	}
	suppressed := liveBody(tn, "a@example.com")
	_, err = e.Accept.Send(e.Ctx, tn.Live, "", suppressed)
	if got := apiErr(t, err); got.Status != 429 || got.Name != "rate_limit_exceeded" {
		t.Fatal(got)
	}
	e.Exec(`UPDATE projects SET hourly_limit=10 WHERE id=$1`, tn.ProjectID)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "suppressed", suppressed); err != nil {
		t.Fatal(err)
	}
	_, err = e.Accept.Send(e.Ctx, tn.Live, "", suppressed)
	if got := apiErr(t, err); got.Status != 429 || got.Name != "daily_quota_exceeded" {
		t.Fatal(got)
	}
	if n := store.puts.Load(); n != 2 {
		t.Fatalf("expected 2 new uploads, got %d", n)
	}
	for _, kind := range []string{"live_hour", "live_day"} {
		if n := e.Count(`SELECT count FROM send_counters WHERE project_id=$1 AND window_kind=$2`, tn.ProjectID, kind); n != 4 {
			t.Fatalf("%s counts %d of 4 accepted recipients", kind, n)
		}
	}
	if e.Count(`SELECT count(*) FROM quota_reservations`) != 1 || e.Count(`SELECT count(*) FROM recipients WHERE billing_state='reserved'`) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE status='suppressed' AND billing_state='none' AND reservation_id IS NULL`) != 3 {
		t.Fatal("expected one billable recipient and three uncharged suppressions")
	}
	if e.Count(`SELECT count(*) FROM webhook_deliveries`) != 3 || e.Count(`SELECT count(*) FROM webhook_events`) != 3 {
		t.Fatal("expected exactly one subscribed event and delivery per accepted suppression")
	}
}

func TestLiveStorageBudgetIsAtomicAndRetentionReleasesSpace(t *testing.T) {
	e := testenv.New(t)
	at := time.Now()
	e.Accept.Now = func() time.Time { return at }
	tn := e.NewTenant("free")
	e.Exec(`INSERT INTO suppressions (org_id,normalized,reason,source) VALUES ($1,'blocked@example.com','manual','dashboard')`, tn.OrgID)
	body := testenv.Body(map[string]any{"from": "app@" + tn.Domain, "to": "blocked@example.com", "subject": "storage", "text": strings.Repeat("x", 32*1024)})
	prime, err := e.Accept.Send(e.Ctx, tn.Live, "prime", body)
	if err != nil {
		t.Fatal(err)
	}
	size := e.Count(`SELECT payload_bytes FROM emails WHERE id=$1`, prime.EmailID)
	meter := func() int { return e.Count(`SELECT live_stored_payload_bytes FROM projects WHERE id=$1`, tn.ProjectID) }
	if size < 32*1024 || meter() != size {
		t.Fatal("canonical byte meter differs from stored bytes")
	}
	e.Exec(`UPDATE projects SET live_storage_limit_bytes=$2 WHERE id=$1`, tn.ProjectID, 2*size)

	// All callers pass preflight with the same remaining capacity. They then
	// write independent blobs and contend for the last slot in PostgreSQL.
	const attempts = 8
	arrived := make(chan struct{}, attempts)
	release := make(chan struct{})
	store := &observedPayloadStore{Store: e.Blobs, arrived: arrived, release: release}
	e.Accept.Blobs = store
	ctx, cancel := context.WithTimeout(e.Ctx, 20*time.Second)
	defer cancel()
	type outcome struct {
		key    string
		result *accept.Result
		err    error
	}
	results := make(chan outcome, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("race-%d", i)
			res, err := e.Accept.Send(ctx, tn.Live, key, body)
			results <- outcome{key, res, err}
		}()
	}
	for range attempts {
		select {
		case <-arrived:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			t.Fatal("all competitors must reach the upload before the capacity race")
		}
	}
	close(release)
	wg.Wait()
	close(results)
	var winner outcome
	var accepted int
	for x := range results {
		if x.err == nil {
			accepted++
			winner = x
		} else if got := apiErr(t, x.err); got.Status != 429 || got.Name != "live_storage_limit_exceeded" {
			t.Fatalf("unexpected capacity result: %+v", got)
		}
	}
	if accepted != 1 || meter() != 2*size || storedObjects(t, e) != 2 || e.Count(`SELECT count(*) FROM emails`) != 2 {
		t.Fatalf("%d winners, %d retained bytes, %d objects", accepted, meter(), storedObjects(t, e))
	}
	if e.Count(`SELECT count(*) FROM quota_reservations`) != 0 || e.Count(`SELECT count FROM send_counters WHERE window_kind='live_hour'`) != 2 {
		t.Fatal("failed sends must roll back quotas, windows and email rows")
	}
	again, err := e.Accept.Send(e.Ctx, tn.Live, winner.key, body)
	if err != nil || !again.Replayed || again.EmailID != winner.result.EmailID || store.puts.Load() != attempts {
		t.Fatalf("winner replay at exact capacity: %+v, %v", again, err)
	}
	e.Accept.Blobs = e.Blobs
	e.Exec(`UPDATE emails SET content_expires_at=now()-interval '1 minute' WHERE id=$1`, prime.EmailID)
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if meter() != size || storedObjects(t, e) != 1 {
		t.Fatalf("retention left %d bytes in %d objects", meter(), storedObjects(t, e))
	}
	e.Exec(`DELETE FROM emails WHERE id=$1`, prime.EmailID)
	if meter() != size {
		t.Fatal("metadata deletion released already removed bytes a second time")
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "after-retention", body); err != nil || meter() != 2*size {
		t.Fatalf("reuse released capacity: %v, meter %d", err, meter())
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Test, "test-at-live-limit", body); err != nil || meter() != 2*size {
		t.Fatalf("test capture changed live storage: %v, meter %d", err, meter())
	}
}

// Only a transaction that wrote an email loses its COMMIT response. Reads and
// idempotency lookups keep their ordinary pgx behavior.
type lostCommitResponse struct {
	db.Beginner
	commit bool
}

var errCommitResponseLost = errors.New("test: lost commit response")

func (p lostCommitResponse) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Beginner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &unacknowledgedTx{Tx: tx, commit: p.commit}, nil
}

type unacknowledgedTx struct {
	pgx.Tx
	commit, wroteEmail bool
}

func (tx *unacknowledgedTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := tx.Tx.Exec(ctx, sql, args...)
	if err == nil && strings.HasPrefix(sql, "INSERT INTO emails (") {
		tx.wroteEmail = true
	}
	return tag, err
}

func (tx *unacknowledgedTx) Commit(ctx context.Context) error {
	if !tx.wroteEmail {
		return tx.Tx.Commit(ctx)
	}
	var err error
	if tx.commit {
		err = tx.Tx.Commit(ctx)
	} else {
		err = tx.Tx.Rollback(ctx)
	}
	if err != nil {
		return err
	}
	return errCommitResponseLost
}

func TestAcceptancePreservesBlobAcrossAmbiguousCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		commit bool
		key    string
	}{
		{"saved-with-idempotency", true, "saved"},
		{"saved-without-idempotency", true, ""},
		{"rolled-back", false, "rolled-back"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testenv.New(t)
			tn := e.NewTenant("free")
			e.Accept.Pool = lostCommitResponse{e.APIPool, tc.commit}
			res, err := e.Accept.Send(e.Ctx, tn.Live, tc.key, liveBody(tn, "a@example.com"))
			if tc.commit && tc.key != "" {
				if err != nil || !res.Replayed {
					t.Fatalf("durable acceptance must be recoverable: %+v, %v", res, err)
				}
			} else if !errors.Is(err, errCommitResponseLost) {
				t.Fatalf("expected lost acknowledgment, got %v", err)
			}
			if storedObjects(t, e) != 1 {
				t.Fatal("acceptance deleted a blob while commit outcome was uncertain")
			}
			e.Retention.OrphanAge = 0
			deleted, err := e.Retention.SweepOrphans(e.Ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.commit {
				if deleted != 0 || storedObjects(t, e) != 1 || e.Count(`SELECT count(*) FROM emails`) != 1 ||
					e.Count(`SELECT count(*) FROM outbox WHERE topic='send'`) != 1 {
					t.Fatal("committed row, payload and delivery job must survive reconciliation")
				}
				e.Drain()
				if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
					t.Fatal("sender could not deliver the preserved object")
				}
			} else if deleted != 1 || storedObjects(t, e) != 0 || e.Count(`SELECT count(*) FROM emails`) != 0 ||
				e.Count(`SELECT live_stored_payload_bytes FROM projects WHERE id=$1`, tn.ProjectID) != 0 {
				t.Fatal("sweeper must remove only the unreferenced blob after rollback")
			}
		})
	}
}

// Run the embedded migration itself on a populated disposable database.
// Its historical payload refs and recipients model an upgrade from 0003.
func TestLiveBudgetMigrationPreservesExistingUsage(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	// testenv creates and migrates this private hedvi_t_* clone using these
	// same development owner credentials. Keep the target host/database from
	// this test's pool even if the caller supplied a custom test admin URL.
	cfg := e.WorkerPool.Config()
	cfg.ConnConfig.User = "hedvi_owner"
	cfg.ConnConfig.Password = "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	var hour time.Time
	if err := owner.QueryRow(e.Ctx, `SELECT date_trunc('hour', now(), 'UTC')`).Scan(&hour); err != nil {
		t.Fatal(err)
	}
	e.Accept.Now = func() time.Time { return hour }
	e.Exec(`INSERT INTO suppressions(org_id,normalized,reason,source) VALUES ($1,'s@example.com','manual','dashboard')`, tn.OrgID)
	current, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "s@example.com", "c@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	expired, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "s@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Test, "", liveBody(tn, "s@example.com")); err != nil {
		t.Fatal(err)
	}
	e.Accept.Now = func() time.Time { return hour.Add(-48 * time.Hour) }
	old, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "s@example.com"))
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Tx(e.Ctx, owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(e.Ctx, `DROP TRIGGER emails_live_payload_storage ON emails;
			DROP FUNCTION hedvi_account_live_payload();
			ALTER TABLE projects DROP COLUMN live_storage_limit_bytes, DROP COLUMN live_stored_payload_bytes;
			DELETE FROM schema_migrations WHERE version='0004_live_acceptance_budgets'`); err != nil {
			return err
		}
		if _, err := tx.Exec(e.Ctx, `UPDATE emails SET content_deleted_at=now(),payload_ref=NULL WHERE id=$1`, expired.EmailID); err != nil {
			return err
		}
		if _, err := tx.Exec(e.Ctx, `UPDATE emails SET payload_bytes=1073741825 WHERE id=$1`, old.EmailID); err != nil {
			return err
		}
		// One previously billed recipient in the hour and a larger existing
		// daily total that must never be decreased by a backfill.
		_, err := tx.Exec(e.Ctx, `UPDATE send_counters SET count=CASE window_kind WHEN 'live_day' THEN 8 ELSE 1 END
			WHERE project_id=$1 AND window_kind IN ('live_hour','live_day')`, tn.ProjectID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	applied, err := db.Migrate(e.Ctx, owner)
	if err != nil || len(applied) != 1 || applied[0] != "0004_live_acceptance_budgets" {
		t.Fatalf("upgrade applied %v: %v", applied, err)
	}
	want := 1073741825 + e.Count(`SELECT payload_bytes FROM emails WHERE id=$1`, current.EmailID)
	if got := e.Count(`SELECT live_stored_payload_bytes FROM projects WHERE id=$1`, tn.ProjectID); got != want {
		t.Fatalf("backfilled %d bytes, want %d retained live bytes", got, want)
	}
	if e.Count(`SELECT count FROM send_counters WHERE project_id=$1 AND window_kind='live_hour' AND window_start=$2`, tn.ProjectID, hour) != 3 ||
		e.Count(`SELECT count FROM send_counters WHERE project_id=$1 AND window_kind='live_day' AND window_start=date_trunc('day',$2::timestamptz,'UTC')`, tn.ProjectID, hour) != 8 {
		t.Fatal("backfill must include current suppressions and preserve higher existing windows")
	}
	e.Accept.Now = func() time.Time { return hour }
	_, err = e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "s@example.com"))
	if got := apiErr(t, err); got.Status != 429 || got.Name != "live_storage_limit_exceeded" {
		t.Fatal(got)
	}
	e.Exec(`UPDATE emails SET content_expires_at=now()-interval '1 minute' WHERE id=$1`, old.EmailID)
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "s@example.com")); err != nil {
		t.Fatalf("over-cap legacy project must recover once its old payload expires: %v", err)
	}
}
