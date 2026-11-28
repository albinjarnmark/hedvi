package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/worker"
)

type deniedDomainTenant struct{ provider.Provider }

func (p deniedDomainTenant) EnsureTenant(context.Context, string) error {
	return errors.New("AccessDeniedException private-account-reference")
}

type deniedDomainIdentity struct {
	provider.Provider
	duringRead func(context.Context) error
}

func (p deniedDomainIdentity) GetIdentity(ctx context.Context, _ string, _ uuid.UUID) (provider.IdentityStatus, error) {
	if p.duringRead != nil {
		if err := p.duringRead(ctx); err != nil {
			return provider.IdentityStatus{}, err
		}
	}
	return provider.IdentityStatus{}, errors.New("AccessDeniedException private-account-reference")
}

type interruptedDomainTenant struct {
	provider.Provider
	duringEnsure func(context.Context) error
}

func (p interruptedDomainTenant) EnsureTenant(ctx context.Context, _ string) error {
	if err := p.duringEnsure(ctx); err != nil {
		return err
	}
	return errors.New("AccessDeniedException private-account-reference")
}

func TestDomainProviderFailureIsRetryableAndNotifiesOperations(t *testing.T) {
	e := testenv.New(t)
	cookie, _, org, project := signupForSecurity(t, e)
	dns := &publishedDomainDNS{}
	e.Domains.MockDNS, e.Domains.Resolver = false, dns
	d, err := e.Domains.Create(e.Ctx, org, project, "provider-failure.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	dns.records = d.Records
	e.Domains.Provider = deniedDomainTenant{e.Sim}
	for range 2 {
		req := httptest.NewRequest("POST", "/console/v1/projects/"+project.String()+"/domains/"+d.ID.String()+"/verify", nil)
		req.AddCookie(cookie)
		req.Header.Set("X-Hedvi-Console", "1")
		w := httptest.NewRecorder()
		e.Handler.ServeHTTP(w, req)
		body := w.Body.String()
		if w.Code != 503 || !strings.Contains(body, "domain_verification_unavailable") || strings.Contains(body, "Idempotency-Key") || strings.Contains(body, "private-account") {
			t.Fatal(w.Code, body)
		}
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, "domain-provider:"+d.ID.String()) != 1 {
		t.Fatal("failure not deduplicated")
	}
	sns := &alertSNS{}
	monitor := &worker.Monitor{Pool: e.WorkerPool, Publisher: &worker.SNSAlertPublisher{Client: sns, TopicARN: "test"}}
	if err := monitor.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if len(sns.calls) != 1 {
		t.Fatal("operations not notified", len(sns.calls))
	}
	e.Domains.Provider = e.Sim
	d, err = e.Domains.Verify(e.Ctx, org, project, d.ID)
	if err != nil || len(d.DKIMTokens) != 3 {
		t.Fatal("retry did not recover same claim", err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, "domain-provider:"+d.ID.String()) != 0 {
		t.Fatal("recovered incident still open")
	}
	if e.Count(`SELECT count(*) FROM domains WHERE org_id=$1 AND id=$2`, org, d.ID) != 1 {
		t.Fatal("retry duplicated claim")
	}

	// A new incident after recovery must alert once again, while repeated
	// failures in the same incident must not enqueue duplicate notifications.
	e.Domains.Provider = deniedDomainIdentity{Provider: e.Sim}
	for range 2 {
		d, err = e.Domains.CheckNow(e.Ctx, e.WorkerPool, d)
		if err != nil || d.Status != "temporary_failure" || strings.Contains(d.LastError, "private-account") {
			t.Fatal("provider read failure was not safely persisted", err)
		}
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1`, "domain-provider:"+d.ID.String()) != 2 ||
		e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, "domain-provider:"+d.ID.String()) != 1 {
		t.Fatal("new incident missing or duplicate alerts created")
	}
	for range 2 {
		if err := monitor.RunOnce(e.Ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(sns.calls) != 2 {
		t.Fatal("new incident was not notified exactly once", len(sns.calls))
	}
	e.Domains.Provider = e.Sim
	if _, err := e.Domains.CheckNow(e.Ctx, e.WorkerPool, d); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, "domain-provider:"+d.ID.String()) != 0 {
		t.Fatal("provider read recovery did not close the new incident")
	}
}

func TestStaleDomainProviderFailureCannotReopenResolvedIncident(t *testing.T) {
	e := testenv.New(t)
	for _, outcome := range []string{"recovery", "deletion"} {
		t.Run(outcome, func(t *testing.T) {
			a := e.NewTenant("free")
			service := *e.Domains
			d, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, "stale-provider-"+outcome+".test", "send")
			if err != nil || d.Status != "verified" {
				t.Fatal("initial verification", err)
			}
			service.Provider = deniedDomainIdentity{Provider: e.Sim}
			d, err = service.CheckNow(e.Ctx, e.WorkerPool, d)
			if err != nil || d.Status != "temporary_failure" {
				t.Fatal("initial incident", err)
			}
			key := "domain-provider:" + d.ID.String()
			if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, key) != 1 {
				t.Fatal("missing initial incident")
			}
			service.Provider = deniedDomainIdentity{Provider: e.Sim, duringRead: func(ctx context.Context) error {
				if outcome == "deletion" {
					// Read-only provider checks do not hold the provisioning lock;
					// the real deletion can finish while their response is pending.
					return service.Delete(ctx, a.OrgID, a.ProjectID, d.ID)
				}
				// The per-process check guard prevents two local checkers. Model
				// another replica's successful commit on a separate DB connection
				// while this replica is still waiting for its provider response.
				return db.Tx(ctx, e.WorkerPool, func(tx pgx.Tx) error {
					if _, err := tx.Exec(ctx, `UPDATE domains SET status='verified',verified_at=now(),last_error='',
						last_checked_at=last_checked_at+interval '1 second',next_check_at=now()+interval '24 hours' WHERE id=$1`, d.ID); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, key)
					return err
				})
			}}
			if _, err := service.CheckNow(e.Ctx, e.WorkerPool, d); apiErr(t, err).Status != 409 {
				t.Fatal("stale result was not rejected", err)
			}
			if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1`, key) != 1 ||
				e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1 AND resolved_at IS NULL`, key) != 0 {
				t.Fatal("stale failure reopened a resolved incident")
			}
			if outcome == "recovery" && e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND status='verified' AND deleted_at IS NULL`, d.ID) != 1 {
				t.Fatal("stale response overwrote successful verification")
			}
			if outcome == "deletion" && e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND deleted_at IS NOT NULL`, d.ID) != 1 {
				t.Fatal("deletion did not finish")
			}
			sns := &alertSNS{}
			monitor := &worker.Monitor{Pool: e.WorkerPool, Publisher: &worker.SNSAlertPublisher{Client: sns, TopicARN: "test"}}
			if err := monitor.RunOnce(e.Ctx); err != nil || len(sns.calls) != 0 {
				t.Fatal("resolved incident was notified", err, len(sns.calls))
			}
		})
	}
}

func TestSupersededDomainProvisioningFailureDoesNotAlert(t *testing.T) {
	e := testenv.New(t)
	for _, outcome := range []string{"deleting", "new-generation", "active"} {
		t.Run(outcome, func(t *testing.T) {
			a := e.NewTenant("free")
			service := *e.Domains
			dns := &publishedDomainDNS{}
			service.MockDNS, service.Resolver = false, dns
			d, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, outcome+".example.org", "send")
			if err != nil {
				t.Fatal(err)
			}
			dns.records = d.Records
			service.Provider = interruptedDomainTenant{Provider: e.Sim, duringEnsure: func(ctx context.Context) error {
				if outcome == "deleting" {
					// Delete first disables the domain, then reports the occupied
					// provider lock. Its in-flight provisioner must not reopen an alert.
					if err := service.Delete(ctx, a.OrgID, a.ProjectID, d.ID); apiErr(t, err).Status != 409 {
						t.Fatal("provider lock did not protect deletion", err)
					}
					return nil
				}
				if outcome == "active" {
					_, err := e.WorkerPool.Exec(ctx, `UPDATE domains SET provider_state='active' WHERE id=$1`, d.ID)
					return err
				}
				_, err := e.WorkerPool.Exec(ctx, `UPDATE domains SET last_checked_at=now() WHERE id=$1`, d.ID)
				return err
			}}
			if _, err := service.Verify(e.Ctx, a.OrgID, a.ProjectID, d.ID); err == nil {
				t.Fatal("provider failure disappeared")
			}
			if e.Count(`SELECT count(*) FROM alerts WHERE dedupe_key=$1`, "domain-provider:"+d.ID.String()) != 0 {
				t.Fatal("superseded provisioning attempt opened an incident")
			}
			if outcome == "deleting" {
				if err := service.Delete(e.Ctx, a.OrgID, a.ProjectID, d.ID); err != nil {
					t.Fatal("deletion retry after provider lock release", err)
				}
				if e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND deleted_at IS NOT NULL`, d.ID) != 1 {
					t.Fatal("deletion retry did not finish")
				}
			}
		})
	}
}
