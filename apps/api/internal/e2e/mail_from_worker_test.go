package e2e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
)

type mailFromBlobGate struct {
	blob.Store
	beforeRead func(context.Context) error
}

func (b mailFromBlobGate) Get(ctx context.Context, key string) ([]byte, error) {
	if err := b.beforeRead(ctx); err != nil {
		return nil, err
	}
	return b.Store.Get(ctx, key)
}

func mailFromWorkerFixture(t *testing.T) (*testenv.Env, *testenv.Tenant, uuid.UUID, *domains.Domain) {
	t.Helper()
	e := testenv.New(t)
	tenant := e.NewTenant("free")
	user := uuid.New()
	e.Exec(`INSERT INTO users(id,email,password_hash,email_verified_at) VALUES($1,$2,'unused-test-fixture',now())`, user, user.String()+"@example.test")
	e.Exec(`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'owner')`, tenant.OrgID, user)
	list, err := e.Domains.List(e.Ctx, tenant.OrgID, tenant.ProjectID)
	if err != nil || len(list) != 1 {
		t.Fatal("fixture domain", err, len(list))
	}
	d := list[0]
	e.Exec(`UPDATE api_keys SET domain_id=$2 WHERE id=$1`, tenant.Live.KeyID, d.ID)
	tenant.Live.DomainID = &d.ID
	return e, tenant, user, d
}

func assertMailFromWaiting(t *testing.T, e *testenv.Env, id uuid.UUID) {
	t.Helper()
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='retry_wait' AND attempt_count=0 AND lease_owner IS NULL`, id) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='reserved' AND status='queued'`, id) != 1 ||
		e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, id) != 0 ||
		e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 0 {
		t.Fatal("MAIL FROM wait lost quota, started a provider attempt or sent prematurely")
	}
}

func TestQueuedMailSurvivesMailFromChangeUntilVerified(t *testing.T) {
	e, tenant, user, d := mailFromWorkerFixture(t)
	body := liveBody(tenant, "recipient@example.com")
	res, err := e.Accept.Send(e.Ctx, tenant.Live, "queued-before-change", body)
	if err != nil {
		t.Fatal(err)
	}
	var deadline time.Time
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT queue_deadline_at FROM emails WHERE id=$1`, res.EmailID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	d, err = e.Domains.ChangeMailFrom(e.Ctx, tenant.OrgID, tenant.ProjectID, d.ID, user, d.MailFrom, "bounces", d.MailFromRevision)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.Exec(`UPDATE emails SET next_attempt_at=now() WHERE id=$1`, res.EmailID)
		if err := e.Sender.Handle(e.Ctx, res.EmailID); err != nil {
			t.Fatal(err)
		}
		assertMailFromWaiting(t, e, res.EmailID)
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND queue_deadline_at=$2`, res.EmailID, deadline) != 1 {
		t.Fatal("waiting extended the original queue deadline")
	}
	replay, err := e.Accept.Send(e.Ctx, tenant.Live, "queued-before-change", body)
	if err != nil || !replay.Replayed || replay.EmailID != res.EmailID {
		t.Fatal("domain correction broke the existing idempotency result", err)
	}
	if d, err = e.Domains.Verify(e.Ctx, tenant.OrgID, tenant.ProjectID, d.ID); err != nil || d.Status != "verified" {
		t.Fatal("new MAIL FROM did not verify", err)
	}
	e.Exec(`UPDATE emails SET next_attempt_at=now() WHERE id=$1`, res.EmailID)
	for range 2 {
		if err := e.Sender.Handle(e.Ctx, res.EmailID); err != nil {
			t.Fatal(err)
		}
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 ||
		e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1 AND state='accepted'`, res.EmailID) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='consumed'`, res.EmailID) != 1 {
		t.Fatal("verified retry did not send and settle exactly once")
	}
	// A DNS wait does not bypass expiration or a deliberate removal.
	expired, deleted := incidentEmail(t, e, tenant), incidentEmail(t, e, tenant)
	if _, err := e.Domains.ChangeMailFrom(e.Ctx, tenant.OrgID, tenant.ProjectID, d.ID, user, d.MailFrom, "next-bounces", d.MailFromRevision); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE emails SET queue_deadline_at=now()-interval '1 second' WHERE id=$1`, expired)
	if err := e.Sender.Handle(e.Ctx, expired); err != nil {
		t.Fatal(err)
	}
	if err := e.Domains.Delete(e.Ctx, tenant.OrgID, tenant.ProjectID, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Sender.Handle(e.Ctx, deleted); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{expired, deleted} {
		if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='failed'`, id) != 1 ||
			e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='released'`, id) != 1 {
			t.Fatal("terminal queue condition kept an unusable reservation")
		}
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("expired or deleted-domain mail was sent")
	}
}

func TestMailFromChangeDuringPayloadReadDefersStaleClaim(t *testing.T) {
	for _, verifyBeforeReturn := range []bool{false, true} {
		name := "pending"
		if verifyBeforeReturn {
			name = "new_generation_already_verified"
		}
		t.Run(name, func(t *testing.T) {
			e, tenant, user, d := mailFromWorkerFixture(t)
			id := incidentEmail(t, e, tenant)
			sender := *e.Sender
			sender.Blobs = mailFromBlobGate{Store: e.Blobs, beforeRead: func(ctx context.Context) error {
				var err error
				d, err = e.Domains.ChangeMailFrom(ctx, tenant.OrgID, tenant.ProjectID, d.ID, user, d.MailFrom, "bounces", d.MailFromRevision)
				if err == nil && verifyBeforeReturn {
					d, err = e.Domains.Verify(ctx, tenant.OrgID, tenant.ProjectID, d.ID)
				}
				return err
			}}
			if err := sender.Handle(e.Ctx, id); err != nil {
				t.Fatal(err)
			}
			assertMailFromWaiting(t, e, id)
			if !verifyBeforeReturn {
				var err error
				d, err = e.Domains.Verify(e.Ctx, tenant.OrgID, tenant.ProjectID, d.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if d.Status != "verified" {
				t.Fatal("fixture did not verify the new generation")
			}
			e.Exec(`UPDATE emails SET next_attempt_at=now() WHERE id=$1`, id)
			if err := e.Sender.Handle(e.Ctx, id); err != nil || e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
				t.Fatal("fresh claim did not resume", err)
			}
		})
	}
}

func TestMailFromUpdateSerializesWithAttemptStart(t *testing.T) {
	e, tenant, _, d := mailFromWorkerFixture(t)
	id := incidentEmail(t, e, tenant)
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	read, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	sender := *e.Sender
	sender.Blobs = mailFromBlobGate{Store: e.Blobs, beforeRead: func(ctx context.Context) error {
		close(read)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	done := make(chan error, 1)
	go func() { done <- sender.Handle(ctx, id) }()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("sender did not reach payload read")
	}
	tx, err := e.APIPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT set_config('hedvi.org_id',$1,true)`, tenant.OrgID.String()); err != nil {
		t.Fatal(err)
	}
	var changed bool
	if err := tx.QueryRow(ctx, `SELECT hedvi_change_mail_from($1,$2,$3,$4,'bounces')`, tenant.ProjectID, d.ID, d.MailFrom, d.MailFromRevision).Scan(&changed); err != nil || !changed {
		t.Fatal("MAIL FROM transition", err)
	}
	unblock()
	for {
		var waiting bool
		if err := e.WorkerPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			AND wait_event_type='Lock' AND query LIKE 'SELECT status,provider_state,mail_from_revision,mail_from_reconfiguring FROM domains%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatal("sender bypassed the pending domain row lock", err)
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("attempt-start did not synchronize with the domain row")
		}
	}
	if e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, id) != 0 {
		t.Fatal("provider attempt started before MAIL FROM update committed")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("sender did not resume after the domain commit")
	}
	assertMailFromWaiting(t, e, id)
}

func TestMailFromChangeDoesNotRecallOrRepeatStartedProviderAttempt(t *testing.T) {
	e, tenant, user, d := mailFromWorkerFixture(t)
	id := incidentEmail(t, e, tenant)
	sender := *e.Sender
	sender.Provider = incidentSendProvider{Provider: e.Sim, send: func(ctx context.Context, in provider.SendInput) (string, error) {
		if e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1 AND state='started'`, id) != 1 {
			t.Fatal("provider call began without durable attempt")
		}
		if _, err := e.Domains.ChangeMailFrom(ctx, tenant.OrgID, tenant.ProjectID, d.ID, user, d.MailFrom, "bounces", d.MailFromRevision); err != nil {
			return "", err
		}
		return e.Sim.Send(ctx, in)
	}}
	if err := sender.Handle(e.Ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := sender.Handle(e.Ctx, id); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 ||
		e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1 AND state='accepted'`, id) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='consumed'`, id) != 1 {
		t.Fatal("in-flight provider attempt was duplicated or lost its settlement")
	}
}

func TestRestrictionDuringPayloadReadHoldsMailBeforeProviderAttempt(t *testing.T) {
	e, tenant, _, _ := mailFromWorkerFixture(t)
	id := incidentEmail(t, e, tenant)
	sender := *e.Sender
	sender.Blobs = mailFromBlobGate{Store: e.Blobs, beforeRead: func(ctx context.Context) error {
		return db.Tx(ctx, e.WorkerPool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE organizations SET risk_status='restricted' WHERE id=$1`, tenant.OrgID)
			return err
		})
	}}
	if err := sender.Handle(e.Ctx, id); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='held'`, id) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='reserved'`, id) != 1 ||
		e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, id) != 0 ||
		e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 0 {
		t.Fatal("mid-claim risk restriction was bypassed")
	}
}
