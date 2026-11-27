package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/sesevent"
	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/webhooks"
	"hedvi.dev/api/internal/worker"
)

type slowDomainDNS struct {
	publishedDomainDNS
	entered chan string
	release chan struct{}
}

func (s *slowDomainDNS) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if strings.HasPrefix(name, "_hedvi-verification.") {
		s.entered <- name
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.publishedDomainDNS.LookupTXT(ctx, name)
}

func TestWorkerDomainPollsHaveBoundedConcurrency(t *testing.T) {
	e := testenv.New(t)
	for range 5 {
		e.NewTenant("free")
	}
	e.Exec(`UPDATE domains SET provider_state='active', next_check_at=now()`)
	resolver := &slowDomainDNS{entered: make(chan string, 5), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(resolver.release) })
	defer release()
	svc := *e.Domains
	svc.Resolver, svc.MockDNS = resolver, false
	d := worker.DomainChecker{Pool: e.WorkerPool, Domains: &svc, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	done := make(chan error, 1)
	go func() { done <- d.RunOnce(e.Ctx) }()
	for range 4 {
		select {
		case <-resolver.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("one slow domain blocked other due DNS checks")
		}
	}
	if n := e.WorkerPool.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("worker held %d database connections during DNS", n)
	}
	select {
	case <-resolver.entered:
		t.Fatal("fifth DNS check started while four were blocked")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("domain checker did not drain its batch")
	}
	if e.Count(`SELECT count(*) FROM domains WHERE last_checked_at IS NOT NULL`) != 5 || e.Count(`SELECT count(*) FROM localmock.ses_identities`) != 0 {
		t.Fatal("poll did not check every due claim read-only at the provider")
	}
}

func TestWorkerEventBindingSuppressionAndOrdering(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	res, err := e.Accept.Send(e.Ctx, a.Live, "", liveBody(a, "a@example.com", "s@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`INSERT INTO suppressions(org_id,normalized,reason,source) VALUES($1,'s@example.com','manual','dashboard')`, a.OrgID)
	if err := e.Sender.Handle(e.Ctx, res.EmailID); err != nil {
		t.Fatal(err)
	}
	var mid string
	var att uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id,provider_message_id FROM send_attempts WHERE email_id=$1`, res.EmailID).Scan(&att, &mid); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	event := func() sesevent.Event {
		return sesevent.Event{
			EventType: "Bounce",
			Mail: sesevent.Mail{MessageID: mid, Tags: map[string][]string{
				"hedvi_email":   {res.EmailID.String()},
				"hedvi_project": {a.ProjectID.String()},
				"hedvi_attempt": {att.String()},
			}},
			Bounce: &sesevent.Bounce{BounceType: "Permanent", Timestamp: at, FeedbackID: "feedback", BouncedRecipients: []sesevent.Recipient{{EmailAddress: "a@example.com"}}},
		}
	}
	for _, change := range []func(*sesevent.Event){
		func(v *sesevent.Event) { delete(v.Mail.Tags, "hedvi_email") },
		func(v *sesevent.Event) { v.Mail.Tags["hedvi_project"] = []string{b.ProjectID.String()} },
		func(v *sesevent.Event) { v.Mail.Tags["hedvi_attempt"] = []string{uuid.NewString()} },
		func(v *sesevent.Event) {
			v.Mail.Tags["hedvi_email"] = append(v.Mail.Tags["hedvi_email"], uuid.NewString())
		},
		func(v *sesevent.Event) { v.Mail.MessageID = "other-message" },
	} {
		v := event()
		change(&v)
		if err := e.Events.Apply(e.Ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	if e.Count(`SELECT count(*) FROM email_events`) != 0 || e.Count(`SELECT count(*) FROM suppressions WHERE reason='hard_bounce'`) != 0 {
		t.Fatal("unbound event changed tenant state")
	}
	v := event()
	if err := e.Events.Apply(e.Ctx, v); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND last_event='bounced'`, res.EmailID) != 1 {
		t.Fatal("local acceptance timestamp froze the remote event")
	}
	v.EventType = "Reject"
	v.Mail.Destination = []string{"s@example.com"}
	v.Reject = &sesevent.Reject{Reason: "sensitive provider detail"}
	if err := e.Events.Apply(e.Ctx, v); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND normalized='s@example.com' AND status='suppressed'`, res.EmailID) != 1 {
		t.Fatal("reject resurrected a suppressed recipient")
	}
	v.EventType = "Complaint"
	v.Complaint = &sesevent.Complaint{Timestamp: at, FeedbackID: "complaint", ComplainedRecipients: []sesevent.Recipient{{EmailAddress: "a@example.com"}}}
	if err := e.Events.Apply(e.Ctx, v); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM suppressions WHERE org_id=$1 AND reason='complaint' AND expires_at IS NULL AND removed_at IS NULL`, a.OrgID) != 1 {
		t.Fatal("complaint was lost behind bounce suppression")
	}
	if err := e.Risk.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerSuppressionExpiryComplaintProtection(t *testing.T) {
	e := testenv.New(t)
	cookie, userID, org, project := signupForSecurity(t, e)
	path := "/console/v1/projects/" + project.String() + "/suppressions"
	e.Exec(`INSERT INTO suppressions(org_id,normalized,reason,source,expires_at) VALUES($1,'a@example.com','hard_bounce','provider_event',now()-interval '1 day')`, org)
	code := cookieRequest(e.Handler, cookie, "POST", path, map[string]string{"email": "a@example.com"})
	if code != 201 {
		t.Fatalf("manual reactivation = %d", code)
	}
	if e.Count(`SELECT count(*) FROM suppressions WHERE org_id=$1 AND normalized='a@example.com' AND removed_at IS NULL AND reason='manual' AND expires_at IS NULL`, org) != 1 {
		t.Fatal("manual suppression vanished after expiry")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reason := "hard_bounce"
			until := time.Now().Add(time.Hour)
			expiry := &until
			if i%3 == 0 {
				reason = "complaint"
				expiry = nil
			}
			_, err := e.WorkerPool.Exec(e.Ctx, `SELECT hedvi_suppress($1,'b@example.com',$2,'provider_event','',$3,'',NULL)`, org, reason, expiry)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM suppressions WHERE org_id=$1 AND normalized='b@example.com' AND removed_at IS NULL AND expires_at IS NULL AND reason='complaint'`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	code = cookieRequest(e.Handler, cookie, "DELETE", path+"/"+id.String(), map[string]string{"note": "request"})
	if code != 403 {
		t.Fatalf("customer complaint removal = %d", code)
	}
	if e.Count(`SELECT count(*) FROM suppressions WHERE id=$1 AND removed_at IS NULL AND removed_by IS NULL AND remove_note=''`, id) != 1 {
		t.Fatal("rejected removal changed the complaint")
	}
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM suppressions WHERE org_id=$1 AND normalized='a@example.com' AND removed_at IS NULL`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"unknown":"field"}`, `{"note":`, `{"note":"one"} {}`} {
		req := httptest.NewRequest(http.MethodDelete, path+"/"+id.String(), strings.NewReader(body))
		req.Header.Set("X-Hedvi-Console", "1")
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		w := httptest.NewRecorder()
		e.Handler.ServeHTTP(w, req)
		if w.Code != 422 {
			t.Fatalf("invalid removal request = %d", w.Code)
		}
	}
	code = cookieRequest(e.Handler, cookie, "DELETE", path+"/"+id.String(), map[string]string{"note": "address ownership checked"})
	if code != 200 || e.Count(`SELECT count(*) FROM suppressions WHERE id=$1 AND removed_by=$2 AND remove_note='address ownership checked'`, id, userID) != 1 ||
		e.Count(`SELECT count(*) FROM audit_log WHERE org_id=$1 AND actor_user_id=$2 AND action='suppression.removed' AND target_id=$3 AND metadata->>'note'='address ownership checked'`, org, userID, id.String()) != 1 {
		t.Fatalf("manual removal and actor audit were not recorded together (HTTP %d)", code)
	}
}

type inspectDelete struct {
	blob.Store
	check func() error
}

func (s inspectDelete) Delete(ctx context.Context, key string) error {
	if err := s.check(); err != nil {
		return err
	}
	return s.Store.Delete(ctx, key)
}

func TestWorkerUnknownRetentionAndAlertURL(t *testing.T) {
	e := testenv.New(t)
	a := e.NewTenant("free")
	res, err := e.Accept.Send(e.Ctx, a.Live, "", liveBody(a, "a@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	e.SetFault("unknown_before_accept")
	if err := e.Sender.Handle(e.Ctx, res.EmailID); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE emails SET content_expires_at=now()-interval '1 day',metadata_expires_at=now()-interval '1 day' WHERE id=$1`, res.EmailID)
	e.Retention.Blobs = inspectDelete{e.Blobs, func() error {
		if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND payload_ref IS NULL`, res.EmailID) != 1 {
			return errors.New("blob delete preceded DB commit")
		}
		return nil
	}}
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='reserved'`, res.EmailID) != 1 {
		t.Fatal("unresolved reservation lost")
	}
	var att uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM send_attempts WHERE email_id=$1`, res.EmailID).Scan(&att); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
		return worker.ResolveUnknown(e.Ctx, tx, res.EmailID, att, "not_sent", "", "operator verified", uuid.Nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Retention.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1`, res.EmailID) != 0 || e.Count(`SELECT reserved FROM usage_periods WHERE org_id=$1`, a.OrgID) != 0 {
		t.Fatal("metadata and quota were not released after resolution")
	}
	m := worker.Monitor{Pool: e.WorkerPool, Log: slog.Default(), AlertURL: ":invalid", HTTPClient: http.DefaultClient}
	if err := m.RunOnce(e.Ctx); err == nil {
		t.Fatal("invalid operator webhook should return an error")
	}
}

type blockingSES struct {
	provider.Provider
	entered chan struct{}
	release chan struct{}
}

func (b blockingSES) Send(ctx context.Context, in provider.SendInput) (string, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return uuid.NewString(), nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestWorkerPerOrganizationSendConcurrency(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	var ids []uuid.UUID
	for _, tn := range []*testenv.Tenant{a, a, a, b} {
		res, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "a@example.com"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.EmailID)
	}
	gate := blockingSES{e.Sim, make(chan struct{}, 4), make(chan struct{})}
	defer close(gate.release)
	s := *e.Sender
	s.Provider = gate
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	done := make(chan error, 4)
	for _, id := range ids[:2] {
		go func() { done <- s.Handle(ctx, id) }()
	}
	for range 2 {
		select {
		case <-gate.entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := s.Handle(ctx, ids[2]); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT attempt_count FROM emails WHERE id=$1`, ids[2]) != 0 || e.Count(`SELECT count(*) FROM outbox WHERE message->>'email_id'=$1 AND available_at>now()`, ids[2].String()) != 1 {
		t.Fatal("busy tenant consumed an attempt or lost job")
	}
	go func() { done <- s.Handle(ctx, ids[3]) }()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal("other tenant stalled")
	}
	for range 3 {
		gate.release <- struct{}{}
	}
	for range 3 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

type hookTransport func(*http.Request) (*http.Response, error)

func (f hookTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWorkerWebhookConcurrencyFenceAndSafeResponses(t *testing.T) {
	e := testenv.New(t)
	a := e.NewTenant("free")
	var hooks, ds []uuid.UUID
	for range 3 {
		h := uuid.New()
		sealed, err := e.Sealer.Seal([]byte(webhooks.NewSecret()), webhooks.SecretAAD(h))
		if err != nil {
			t.Fatal(err)
		}
		e.Exec(`INSERT INTO webhooks(id,org_id,project_id,url,events,secret_enc) VALUES($1,$2,$3,'https://example.com/hook','{}',$4)`, h, a.OrgID, a.ProjectID, sealed)
		hooks = append(hooks, h)
	}
	var eventIDs []string
	for i, h := range []uuid.UUID{hooks[0], hooks[1], hooks[0], hooks[2]} {
		event := fmt.Sprintf("fixture-%d", i)
		eventIDs = append(eventIDs, event)
		e.Exec(`INSERT INTO webhook_events(id,org_id,project_id,type,payload) VALUES($1,$2,$3,'email.sent','{}')`, event, a.OrgID, a.ProjectID)
		var id uuid.UUID
		if err := e.WorkerPool.QueryRow(e.Ctx, `INSERT INTO webhook_deliveries(org_id,webhook_id,event_id) VALUES($1,$2,$3) RETURNING id`, a.OrgID, h, event).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ds = append(ds, id)
	}
	entered := make(chan struct{}, 4)
	release := make(chan struct{}, 4)
	d := *e.Webhooks
	d.Client = &http.Client{Transport: hookTransport(func(r *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("hello\x00world"))}, nil
	})}
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	done := make(chan error, 4)
	go func() { done <- d.Handle(ctx, ds[0]) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := d.Handle(ctx, ds[2]); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(ctx, e.WorkerPool, func(tx pgx.Tx) error {
		return webhooks.ScheduleDelivery(ctx, tx, a.OrgID, hooks[0], eventIDs[0], time.Now())
	}); apiErr(t, err).Status != 409 {
		t.Fatal("manual resend stole in-flight delivery")
	}
	go func() { done <- d.Handle(ctx, ds[1]) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := d.Handle(ctx, ds[3]); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM webhook_deliveries WHERE state='delivering'`) != 2 || e.Count(`SELECT count(*) FROM webhook_deliveries WHERE state='pending' AND attempt_count=0 AND next_attempt_at>now()`) != 2 {
		t.Fatal("concurrency limits or deferred delivery budget failed")
	}
	release <- struct{}{}
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if e.Count(`SELECT count(*) FROM webhook_deliveries WHERE state='succeeded' AND last_response='helloworld'`) != 2 {
		t.Fatal("NUL response prevented durable settlement")
	}
}
