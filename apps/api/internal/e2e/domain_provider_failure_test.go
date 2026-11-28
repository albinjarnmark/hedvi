package e2e

import (
	"context"
	"errors"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/worker"
	"net/http/httptest"
	"strings"
	"testing"
)

type deniedDomainTenant struct{ provider.Provider }

func (p deniedDomainTenant) EnsureTenant(context.Context, string) error {
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
}
