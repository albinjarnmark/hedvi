package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/worker"
)

type incidentSendProvider struct {
	provider.Provider
	send func(context.Context, provider.SendInput) (string, error)
}

func (p incidentSendProvider) Send(ctx context.Context, in provider.SendInput) (string, error) {
	return p.send(ctx, in)
}

func deniedProviderSend(context.Context, provider.SendInput) (string, error) {
	return "", &provider.SendError{Outcome: provider.Retryable, Code: "AccessDeniedException", Message: "private AWS resource details", Alert: true}
}

func incidentEmail(t *testing.T, e *testenv.Env, tenant *testenv.Tenant) uuid.UUID {
	t.Helper()
	res, err := e.Accept.Send(e.Ctx, tenant.Live, uuid.NewString(), liveBody(tenant, "recipient@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	return res.EmailID
}

func TestProviderSendIncidentLifecycleAndIsolation(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	e.Exec(`INSERT INTO alerts(kind,severity,dedupe_key,message,notified_at)
		VALUES('provider_sending_paused','critical','provider:AccessDeniedException','Legacy unscoped incident',now())`)
	if _, err := e.Domains.Create(e.Ctx, a.OrgID, a.ProjectID, "other-send-path.test", "send"); err != nil {
		t.Fatal(err)
	}
	otherDomain := *a
	otherDomain.Domain = "other-send-path.test"
	denied := *e.Sender
	denied.Provider = incidentSendProvider{Provider: e.Sim, send: deniedProviderSend}
	otherSet := denied
	otherSet.ConfigSet += "-other"
	first := incidentEmail(t, e, a)
	paths := []struct {
		id     uuid.UUID
		sender *worker.Sender
	}{{first, &denied}, {incidentEmail(t, e, b), &denied}, {incidentEmail(t, e, &otherDomain), &denied}, {incidentEmail(t, e, a), &otherSet}}
	for _, path := range paths {
		if err := path.sender.Handle(e.Ctx, path.id); err != nil {
			t.Fatal(err)
		}
	}
	e.Exec(`UPDATE emails SET next_attempt_at=now() WHERE id=$1`, first)
	if err := denied.Handle(e.Ctx, first); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`) != 4 ||
		e.Count(`SELECT count(*) FROM provider_send_routes WHERE last_outcome='failure'`) != 4 {
		t.Fatal("projects, domains or configuration sets shared an incident")
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key LIKE 'provider-send:%' AND (org_id IS NULL OR project_id IS NULL OR message LIKE '%private AWS%' OR data::text LIKE '%private AWS%')`) != 0 {
		t.Fatal("provider incident is unscoped or exposes raw provider details")
	}
	sns := &alertSNS{}
	monitor := &worker.Monitor{Pool: e.WorkerPool, Publisher: &worker.SNSAlertPublisher{Client: sns, TopicARN: "test"}}
	for range 2 {
		if err := monitor.RunOnce(e.Ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(sns.calls) != 4 {
		t.Fatal("deduplication failed", len(sns.calls))
	}
	// The ordinary retry succeeds: it consumes quota once and closes only its
	// exact path, preserving failures on the other domain/project/config set.
	e.Exec(`UPDATE emails SET next_attempt_at=now() WHERE id=$1`, first)
	if err := e.Sender.Handle(e.Ctx, first); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`) != 3 ||
		e.Count(`SELECT count(*) FROM provider_send_routes WHERE last_outcome='accepted'`) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='consumed'`, first) != 1 ||
		e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("successful retry changed another path or delivery accounting")
	}
	if err := e.Sender.Handle(e.Ctx, first); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("duplicate job resent the recovered email")
	}
	if err := denied.Handle(e.Ctx, incidentEmail(t, e, a)); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key LIKE 'provider-send:%'`) != 5 ||
		e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`) != 4 {
		t.Fatal("new failure did not rearm the recovered incident")
	}
	if err := monitor.RunOnce(e.Ctx); err != nil || len(sns.calls) != 5 {
		t.Fatal("rearmed incident was not notified once", err, len(sns.calls))
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key='provider:AccessDeniedException' AND resolved_at IS NULL`) != 1 {
		t.Fatal("one path's success falsely resolved the unscoped legacy incident")
	}
	// API sessions must not be able to forge provider health or attempt state.
	for _, query := range []string{`SELECT count(*) FROM provider_send_routes`, `UPDATE send_attempts SET state='accepted' WHERE false`} {
		if err := db.InOrg(e.Ctx, e.APIPool, a.OrgID, func(tx pgx.Tx) error {
			_, err := tx.Exec(e.Ctx, query)
			return err
		}); err == nil {
			t.Fatal("public API can access worker-owned health state", query)
		}
	}
}

func TestProviderSendIncidentIgnoresOutOfOrderResponses(t *testing.T) {
	e := testenv.New(t)
	for _, tc := range []struct {
		name       string
		oldFailure bool
		prune      bool
	}{{"older_failure_after_newer_accept", true, false}, {"older_accept_after_newer_failure", false, false}, {"older_failure_after_success_retention", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			tenant := e.NewTenant("free")
			denied := *e.Sender
			denied.Provider = incidentSendProvider{Provider: e.Sim, send: deniedProviderSend}
			if err := denied.Handle(e.Ctx, incidentEmail(t, e, tenant)); err != nil {
				t.Fatal(err)
			}
			oldEmail, newEmail := incidentEmail(t, e, tenant), incidentEmail(t, e, tenant)
			ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
			defer cancel()
			started, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			slow := *e.Sender
			slow.Provider = incidentSendProvider{Provider: e.Sim, send: func(ctx context.Context, in provider.SendInput) (string, error) {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				if tc.oldFailure {
					return deniedProviderSend(ctx, in)
				}
				return e.Sim.Send(ctx, in)
			}}
			done := make(chan error, 1)
			go func() { done <- slow.Handle(ctx, oldEmail) }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("older provider attempt did not start")
			}
			newSender := e.Sender
			if !tc.oldFailure {
				newSender = &denied
			}
			if err := newSender.Handle(ctx, newEmail); err != nil {
				t.Fatal(err)
			}
			var newestAttempt uuid.UUID
			if err := e.WorkerPool.QueryRow(ctx, `SELECT id FROM send_attempts WHERE email_id=$1`, newEmail).Scan(&newestAttempt); err != nil {
				t.Fatal(err)
			}
			if tc.prune {
				// Retention removes the settled email/attempt and resolved alert,
				// but must not remove the ordering watermark for this active path.
				e.Exec(`DELETE FROM alerts WHERE project_id=$1 AND resolved_at IS NOT NULL`, tenant.ProjectID)
				e.Exec(`DELETE FROM emails WHERE id=$1`, newEmail)
			}
			unblock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("older provider attempt did not finish")
			}
			wantOpen, wantOutcome, oldState := 0, "accepted", "retry_wait"
			if !tc.oldFailure {
				wantOpen, wantOutcome, oldState = 1, "failure", "sent"
			}
			if e.Count(`SELECT count(*) FROM alerts WHERE project_id=$1 AND dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`, tenant.ProjectID) != wantOpen ||
				e.Count(`SELECT count(*) FROM provider_send_routes WHERE project_id=$1 AND last_attempt_id=$2 AND last_outcome=$3`, tenant.ProjectID, newestAttempt, wantOutcome) != 1 ||
				e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state=$2`, oldEmail, oldState) != 1 {
				t.Fatal("late response changed a newer incident result or lost its own settlement")
			}
			if tc.prune && e.Count(`SELECT count(*) FROM alerts WHERE project_id=$1 AND dedupe_key LIKE 'provider-send:%'`, tenant.ProjectID) != 0 {
				t.Fatal("retention allowed the stale failure to create another incident")
			}
		})
	}
}

func TestProviderSendUnknownAndManualDecisionDoNotProveRecovery(t *testing.T) {
	e := testenv.New(t)
	tenant := e.NewTenant("free")
	denied := *e.Sender
	denied.Provider = incidentSendProvider{Provider: e.Sim, send: deniedProviderSend}
	if err := denied.Handle(e.Ctx, incidentEmail(t, e, tenant)); err != nil {
		t.Fatal(err)
	}
	emailID := incidentEmail(t, e, tenant)
	uncertain := *e.Sender
	uncertain.Provider = incidentSendProvider{Provider: e.Sim, send: func(context.Context, provider.SendInput) (string, error) {
		return "", errors.New("provider connection lost")
	}}
	if err := uncertain.Handle(e.Ctx, emailID); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='unknown'`, emailID) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND billing_state='reserved'`, emailID) != 1 {
		t.Fatal("uncertain send lost its no-retry state or reservation")
	}
	var attemptID uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM send_attempts WHERE email_id=$1 AND state='unknown'`, emailID).Scan(&attemptID); err != nil {
		t.Fatal(err)
	}
	if err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
		return worker.ResolveUnknown(e.Ctx, tx, emailID, attemptID, "accepted", "manually-confirmed", "Investigated provider records", uuid.Nil)
	}); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE project_id=$1 AND dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`, tenant.ProjectID) != 1 ||
		e.Count(`SELECT count(*) FROM provider_send_routes WHERE project_id=$1 AND last_outcome='failure'`, tenant.ProjectID) != 1 {
		t.Fatal("manual settlement claimed the provider path recovered")
	}
	if err := uncertain.Handle(e.Ctx, emailID); err != nil || e.Count(`SELECT count(*) FROM send_attempts WHERE email_id=$1`, emailID) != 1 {
		t.Fatal("resolved unknown was retried", err)
	}
}

func TestConcurrentProviderSendFailuresOpenOneIncident(t *testing.T) {
	e := testenv.New(t)
	tenant := e.NewTenant("free")
	ids := []uuid.UUID{incidentEmail(t, e, tenant), incidentEmail(t, e, tenant)}
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	started, release := make(chan struct{}, 2), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	sender := *e.Sender
	sender.Provider = incidentSendProvider{Provider: e.Sim, send: func(ctx context.Context, in provider.SendInput) (string, error) {
		started <- struct{}{}
		select {
		case <-release:
			return deniedProviderSend(ctx, in)
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}
	done := make(chan error, 2)
	for _, id := range ids {
		go func() { done <- sender.Handle(ctx, id) }()
	}
	for range ids {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("both provider calls did not start")
		}
	}
	unblock()
	for range ids {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("concurrent failures did not settle")
		}
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE project_id=$1 AND dedupe_key LIKE 'provider-send:%' AND resolved_at IS NULL`, tenant.ProjectID) != 1 ||
		e.Count(`SELECT count(*) FROM provider_send_routes WHERE project_id=$1 AND last_outcome='failure'`, tenant.ProjectID) != 1 ||
		e.Count(`SELECT count(*) FROM emails WHERE project_id=$1 AND send_state='retry_wait'`, tenant.ProjectID) != 2 ||
		e.Count(`SELECT count(*) FROM recipients WHERE org_id=$1 AND billing_state='reserved'`, tenant.OrgID) != 2 {
		t.Fatal("concurrent failures duplicated incidents or lost delivery reservations")
	}
	sns := &alertSNS{}
	monitor := &worker.Monitor{Pool: e.WorkerPool, Publisher: &worker.SNSAlertPublisher{Client: sns, TopicARN: "test"}}
	if err := monitor.RunOnce(e.Ctx); err != nil || len(sns.calls) != 1 {
		t.Fatal("concurrent incident produced duplicate notification", err, len(sns.calls))
	}
}
