package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/sesevent"
	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/webhooks"
	"hedvi.dev/api/internal/worker"
)

func state(e *testenv.Env, id uuid.UUID) (sendState, lastEvent string) {
	e.T.Helper()
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT send_state, last_event FROM emails WHERE id = $1`, id).Scan(&sendState, &lastEvent); err != nil {
		e.T.Fatal(err)
	}
	return
}

func outbound(e *testenv.Env) int { return e.Count(`SELECT count(*) FROM localmock.ses_outbound`) }

func TestHappyPathPerRecipientOutcomes(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, err := e.Accept.Send(e.Ctx, tn.Live, "", testenv.Body(map[string]any{"from": "app@" + tn.Domain,
		"to": []string{"ok@example.com", "x+bounce@example.com"}, "bcc": "y+complaint@example.com", "subject": "s", "text": "t"}))
	if err != nil {
		t.Fatal(err)
	}
	e.Drain()
	e.MakeDue()
	e.Drain()
	e.MakeDue()
	e.Drain()
	want := map[string]string{"ok@example.com": "delivered", "x+bounce@example.com": "bounced", "y+complaint@example.com": "complained"}
	rows, _ := e.WorkerPool.Query(e.Ctx, `SELECT normalized, status, billing_state FROM recipients WHERE email_id = $1`, res.EmailID)
	defer rows.Close()
	for rows.Next() {
		var addr, status, billing string
		_ = rows.Scan(&addr, &status, &billing)
		if want[addr] != status || billing != "consumed" {
			t.Errorf("%s: status %s billing %s, want %s consumed", addr, status, billing, want[addr])
		}
	}
	// Bounce and complaint after acceptance are charged and suppressed.
	if n := e.Count(`SELECT count(*) FROM suppressions WHERE org_id = $1`, tn.OrgID); n != 2 {
		t.Fatalf("suppressions %d, want 2", n)
	}
	if n := e.Count(`SELECT consumed FROM usage_periods WHERE org_id = $1`, tn.OrgID); n != 3 {
		t.Fatalf("consumed %d", n)
	}
	if outbound(e) != 1 {
		t.Fatal("expected one SES call for one email")
	}
}

func TestDuplicatePublishAfterDispatcherCrashSendsOnce(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	e.Faults.Arm(faults.DispatchAfterPublish, 1)
	if _, err := e.Dispatcher.RunOnce(e.Ctx); !errors.Is(err, faults.ErrCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	if _, err := e.Dispatcher.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM localmock.queue_messages WHERE queue = 'send'`); n != 2 {
		t.Fatalf("expected duplicate publication, got %d messages", n)
	}
	e.Drain()
	if outbound(e) != 1 {
		t.Fatalf("duplicate job caused %d sends", outbound(e))
	}
	if s, _ := state(e, res.EmailID); s != "sent" {
		t.Fatal(s)
	}
}

func TestWorkerCrashAfterSESAcceptedIsReconciledWithoutResend(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	if _, err := e.Dispatcher.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	e.Faults.Arm(faults.SendAfterProviderCall, 1)
	if err := e.Sender.Handle(e.Ctx, res.EmailID); !errors.Is(err, faults.ErrCrash) {
		t.Fatalf("expected crash, got %v", err)
	}
	// The lease expires; the reaper must not retry blindly.
	e.Exec(`UPDATE emails SET lease_expires_at = now() - interval '1 second' WHERE id = $1`, res.EmailID)
	if err := e.Reaper.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if s, last := state(e, res.EmailID); s != "unknown" || last != "delivery_unknown" {
		t.Fatalf("after reaper: %s/%s", s, last)
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1 AND billing_state = 'reserved'`, res.EmailID); n != 1 {
		t.Fatal("uncertain attempt must keep its reservation")
	}
	if n := e.Count(`SELECT count(*) FROM alerts WHERE kind = 'delivery_unknown'`); n != 1 {
		t.Fatal("no alert for unknown outcome")
	}
	// SES events tagged with the attempt id reconcile it.
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "sent" {
		t.Fatalf("not reconciled: %s", s)
	}
	if n := e.Count(`SELECT count(*) FROM send_attempts WHERE email_id = $1 AND state = 'resolved_accepted'`, res.EmailID); n != 1 {
		t.Fatal("attempt not marked resolved_accepted")
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1 AND billing_state = 'consumed'`, res.EmailID); n != 1 {
		t.Fatal("reservation not consumed after reconciliation")
	}
	if outbound(e) != 1 || e.Count(`SELECT count(*) FROM send_attempts`) != 1 {
		t.Fatal("email was sent again")
	}
}

func TestCrashBeforeProviderCallIsUnknownNotRetried(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	e.SetFault("unknown_before_accept")
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "unknown" {
		t.Fatal(s)
	}
	e.SetFault("")
	e.MakeDue()
	for range 3 {
		_ = e.Reaper.RunOnce(e.Ctx)
		e.Drain()
	}
	if outbound(e) != 0 || e.Count(`SELECT count(*) FROM send_attempts`) != 1 {
		t.Fatal("unknown attempt was retried automatically")
	}
	// Staff resolve it as not sent: quota is released exactly once.
	err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
		var attempt uuid.UUID
		if err := tx.QueryRow(e.Ctx, `SELECT id FROM send_attempts WHERE email_id = $1`, res.EmailID).Scan(&attempt); err != nil {
			return err
		}
		if err := worker.ResolveUnknown(e.Ctx, tx, res.EmailID, attempt, "not_sent", "", "checked SES", uuid.Nil); err != nil {
			return err
		}
		return worker.ResolveUnknown(e.Ctx, tx, res.EmailID, attempt, "not_sent", "", "again", uuid.Nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT reserved + consumed FROM usage_periods WHERE org_id = $1`, tn.OrgID); n != 0 {
		t.Fatalf("quota not released: %d", n)
	}
	if n := e.Count(`SELECT count(*) FROM credit_ledger WHERE org_id = $1 AND kind = 'release'`, tn.OrgID); n != 1 {
		t.Fatalf("release recorded %d times", n)
	}
}

func TestThrottlingIsRetriedAndRejectionReleasesQuota(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	e.SetFault("throttle")
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "retry_wait" {
		t.Fatal(s)
	}
	e.SetFault("")
	e.MakeDue()
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "sent" {
		t.Fatal(s)
	}
	if n := e.Count(`SELECT count(*) FROM send_attempts WHERE email_id = $1`, res.EmailID); n != 2 {
		t.Fatalf("attempts %d", n)
	}

	rej, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "b@example.com"))
	e.SetFault("reject")
	e.Drain()
	e.SetFault("")
	if s, last := state(e, rej.EmailID); s != "failed" || last != "failed" {
		t.Fatal(s, last)
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1 AND billing_state = 'released'`, rej.EmailID); n != 1 {
		t.Fatal("rejected email kept its reservation")
	}
}

func TestPauseHoldsQueuedJobsUntilApproved(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	e.Exec(`UPDATE projects SET sending_status = 'paused' WHERE id = $1`, tn.ProjectID)
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "held" || outbound(e) != 0 {
		t.Fatalf("paused project sent mail (%s)", s)
	}
	e.Exec(`UPDATE projects SET sending_status = 'approved' WHERE id = $1`, tn.ProjectID)
	if err := e.Reaper.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	// The worker runs on the host and PostgreSQL in Docker; make the resumed
	// outbox job due instead of assuming their clocks are identical.
	e.MakeDue()
	e.Drain()
	if s, _ := state(e, res.EmailID); s != "sent" {
		t.Fatal(s)
	}
}

func TestEventsAreIdempotentAndOrderIndependent(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	// Send without processing the simulator's events.
	if _, err := e.Dispatcher.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Sender.Handle(e.Ctx, res.EmailID); err != nil {
		t.Fatal(err)
	}
	var mid string
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT provider_message_id FROM emails WHERE id = $1`, res.EmailID).Scan(&mid)
	var attemptID uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM send_attempts WHERE email_id=$1`, res.EmailID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	m := sesevent.Mail{MessageID: mid, Timestamp: now, Tags: map[string][]string{"hedvi_email": {res.EmailID.String()}, "hedvi_project": {tn.ProjectID.String()}, "hedvi_attempt": {attemptID.String()}}}
	bounce := sesevent.Event{EventType: "Bounce", Mail: m, Bounce: &sesevent.Bounce{BounceType: "Permanent", BounceSubType: "General",
		Timestamp: now.Add(2 * time.Second), FeedbackID: "fb1", BouncedRecipients: []sesevent.Recipient{{EmailAddress: "a@example.com"}}}}
	late := sesevent.Event{EventType: "Delivery", Mail: m, Delivery: &sesevent.Delivery{Timestamp: now.Add(time.Second), Recipients: []string{"a@example.com"}}}
	delay := sesevent.Event{EventType: "DeliveryDelay", Mail: m, DeliveryDelay: &sesevent.DeliveryDelay{Timestamp: now,
		DelayedRecipients: []sesevent.Recipient{{EmailAddress: "a@example.com"}}}}
	for _, ev := range []sesevent.Event{bounce, bounce, late, delay, bounce} {
		if err := e.Events.Apply(e.Ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT status FROM recipients WHERE email_id = $1`, res.EmailID).Scan(&status)
	if status != "bounced" {
		t.Fatalf("late events overwrote bounce: %s", status)
	}
	if n := e.Count(`SELECT count(*) FROM email_events WHERE email_id = $1 AND type = 'bounced'`, res.EmailID); n != 1 {
		t.Fatalf("duplicate bounce stored %d times", n)
	}
	if n := e.Count(`SELECT count(*) FROM suppressions WHERE org_id = $1 AND reason = 'hard_bounce' AND expires_at IS NOT NULL`, tn.OrgID); n != 1 {
		t.Fatal("hard bounce suppression missing or without expiry")
	}
	// A suppression in one organization does not affect another.
	other := e.NewTenant("free")
	r2, _ := e.Accept.Send(e.Ctx, other.Live, "", liveBody(other, "a@example.com"))
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1 AND status = 'queued'`, r2.EmailID); n != 1 {
		t.Fatal("suppression leaked across organizations")
	}
	// Unknown provider message ids are ignored, not errors.
	if err := e.Events.Apply(e.Ctx, sesevent.Event{EventType: "Delivery", Mail: sesevent.Mail{MessageID: "nope"},
		Delivery: &sesevent.Delivery{Recipients: []string{"z@example.com"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestSignedWebhooksRetryWithStableIDAndBlockSSRF(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	var calls atomic.Int32
	var mu sync.Mutex
	var ids []string
	var lastBody []byte
	var lastHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		ids = append(ids, r.Header.Get("webhook-id"))
		lastBody, lastHeaders = b, r.Header.Clone()
		mu.Unlock()
		if calls.Add(1) == 1 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	secret := webhooks.NewSecret()
	hookID := uuid.Must(uuid.NewV7())
	enc, _ := e.Sealer.Seal([]byte(secret), webhooks.SecretAAD(hookID))
	e.Exec(`INSERT INTO webhooks (id, org_id, project_id, url, events, secret_enc) VALUES ($1, $2, $3, $4, $5, $6)`,
		hookID, tn.OrgID, tn.ProjectID, srv.URL, []string{webhooks.EmailSent}, enc)
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com")); err != nil {
		t.Fatal(err)
	}
	e.Drain()
	e.MakeDue()
	e.Drain()
	if calls.Load() != 2 {
		t.Fatalf("webhook calls %d, want 2 (failure then retry)", calls.Load())
	}
	if ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("event id changed across retries: %v", ids)
	}
	if err := webhooks.Verify(secret, lastHeaders.Get("webhook-id"), lastHeaders.Get("webhook-timestamp"),
		lastHeaders.Get("webhook-signature"), lastBody, time.Now()); err != nil {
		t.Fatalf("signature: %v", err)
	}
	if lastHeaders.Get("svix-signature") != lastHeaders.Get("webhook-signature") {
		t.Fatal("svix alias missing")
	}
	var payload webhooks.Payload
	_ = json.Unmarshal(lastBody, &payload)
	if payload.Type != "email.sent" || payload.Data["email_id"] == nil {
		t.Fatalf("payload %s", lastBody)
	}
	if n := e.Count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'succeeded' AND attempt_count = 2`); n != 1 {
		t.Fatal("delivery not recorded as succeeded after retry")
	}

	// With production settings a loopback receiver is refused at dial time.
	strict := *e.Webhooks
	strict.Client = netsafeStrict()
	var deliveryID uuid.UUID
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM webhook_deliveries LIMIT 1`).Scan(&deliveryID)
	e.Exec(`UPDATE webhook_deliveries SET state = 'pending', next_attempt_at = now(), attempt_count = 0 WHERE id = $1`, deliveryID)
	before := calls.Load()
	if err := strict.Handle(e.Ctx, deliveryID); err != nil {
		t.Fatal(err)
	}
	var lastErr string
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT last_error FROM webhook_deliveries WHERE id = $1`, deliveryID).Scan(&lastErr)
	if calls.Load() != before || !strings.Contains(lastErr, "not allowed") {
		t.Fatalf("SSRF guard did not block loopback: calls=%d err=%q", calls.Load()-before, lastErr)
	}
}

func TestCreditsExpireWithoutExtensionByReservations(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("flex")
	now := time.Now()
	var lot uuid.UUID
	err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
		var err error
		lot, err = quota.GrantCredits(e.Ctx, tx, tn.OrgID, "grant", "test", 5, 0, time.Hour, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com", "b@example.com", "c@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "d@example.com", "e@example.com", "f@example.com")); err == nil {
		t.Fatal("flex sent beyond its credits")
	}
	// The lot expires while 3 units are reserved; then the email is rejected.
	e.Exec(`UPDATE credit_lots SET expires_at = now() - interval '1 second' WHERE id = $1`, lot)
	if err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error { _, err := quota.ExpireLots(e.Ctx, tx, time.Now()); return err }); err != nil {
		t.Fatal(err)
	}
	e.SetFault("reject")
	e.Drain()
	e.SetFault("")
	var granted, reserved, consumed, expired int
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT granted, reserved, consumed, expired_amount FROM credit_lots WHERE id = $1`, lot).Scan(&granted, &reserved, &consumed, &expired)
	if reserved != 0 || consumed != 0 || expired != 5 {
		t.Fatalf("lot after release: reserved=%d consumed=%d expired=%d", reserved, consumed, expired)
	}
	if s, _ := state(e, res.EmailID); s != "failed" {
		t.Fatal(s)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "g@example.com")); err == nil {
		t.Fatal("expired credits were usable")
	}
}

func TestContentRetentionDeletesPayloadButKeepsMetadata(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	res, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
	e.Drain()
	e.Exec(`UPDATE emails SET content_expires_at = now() - interval '1 second' WHERE id = $1`, res.EmailID)
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM emails WHERE id = $1 AND payload_ref IS NULL AND subject IS NULL AND content_deleted_at IS NOT NULL`, res.EmailID); n != 1 {
		t.Fatal("content not deleted")
	}
	if n := e.Count(`SELECT count(*) FROM recipients WHERE email_id = $1`, res.EmailID); n != 1 {
		t.Fatal("metadata deleted with content")
	}
	e.Exec(`UPDATE emails SET metadata_expires_at = now() - interval '1 second' WHERE id = $1`, res.EmailID)
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails`)+e.Count(`SELECT count(*) FROM recipients`)+e.Count(`SELECT count(*) FROM email_events`) != 0 {
		t.Fatal("metadata survived retention")
	}
	// Queued mail keeps its content until it reaches a terminal state.
	q, _ := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "b@example.com"))
	e.Exec(`UPDATE emails SET content_expires_at = now() - interval '1 second' WHERE id = $1`, q.EmailID)
	_ = e.Retention.RunOnce(e.Ctx)
	if n := e.Count(`SELECT count(*) FROM emails WHERE id = $1 AND payload_ref IS NOT NULL`, q.EmailID); n != 1 {
		t.Fatal("payload deleted before the email was sent")
	}
}

func TestRiskPolicyPausesHighBounceProjects(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	var to []string
	for i := range 40 {
		addr := testenv.Sprintf("u%d@example.com", i)
		if i < 2 {
			addr = testenv.Sprintf("u%d+complaint@example.com", i)
		}
		to = append(to, addr)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, to...)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		e.Drain()
		e.MakeDue()
	}
	if err := e.Risk.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = e.WorkerPool.QueryRow(e.Ctx, `SELECT sending_status FROM projects WHERE id = $1`, tn.ProjectID).Scan(&status)
	if status != "paused" {
		t.Fatalf("two complaints in a small sample should pause, got %s", status)
	}
	if n := e.Count(`SELECT count(*) FROM audit_log WHERE action = 'project.paused' AND org_id = $1`, tn.OrgID); n != 1 {
		t.Fatal("pause not audited")
	}
	e.Exec(`UPDATE projects SET sending_status='approved',status_changed_at=now() WHERE id=$1`, tn.ProjectID)
	if err := e.Risk.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='approved'`, tn.ProjectID); n != 1 {
		t.Fatal("old complaints immediately revoked staff reapproval")
	}
}
