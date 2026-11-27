package e2e

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

func TestEnvironmentTenantBindingBeforeProviderCalls(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	var prod uuid.UUID
	if err := db.InOrg(e.Ctx, e.APIPool, tn.OrgID, func(tx pgx.Tx) error {
		var err error
		prod, err = console.NewProject(e.Ctx, tx, tn.OrgID, "Production", config.EnvProduction)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := config.EnvStaging.ProjectTenant(tn.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	prodName, err := config.EnvProduction.ProjectTenant(prod)
	if err != nil {
		t.Fatal(err)
	}
	if e.Count("SELECT count(*) FROM projects WHERE (id=$1 AND ses_tenant_name=$2) OR (id=$3 AND ses_tenant_name=$4)", tn.ProjectID, legacy, prod, prodName) != 2 {
		t.Fatal("wrong tenant persisted at project creation")
	}
	// Simulate a restore into a deployment with a different environment, and
	// a corrupt same-environment binding to a different project UUID.
	correct, err := config.EnvProduction.ProjectTenant(tn.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := config.EnvProduction.ProjectTenant(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	observed := &countingTenantProvider{Provider: e.Sim}
	dsvc := *e.Domains
	dns := &publishedDomainDNS{}
	dsvc.Resolver = dns
	dsvc.MockDNS = false
	dsvc.Provider = observed
	dsvc.TenantEnv = config.EnvProduction
	d, err := dsvc.Create(e.Ctx, tn.OrgID, tn.ProjectID, "namespace.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	dns.records = d.Records
	e.Sender.TenantEnv = config.EnvProduction
	for _, invalid := range []string{legacy, unrelated, "operator-hedvi-production-auth"} {
		e.Exec("UPDATE projects SET ses_tenant_name=$2 WHERE id=$1", tn.ProjectID, invalid)
		if _, err := dsvc.Verify(e.Ctx, tn.OrgID, tn.ProjectID, d.ID); !errors.Is(err, config.ErrProjectTenant) {
			t.Fatalf("untrusted domain binding: %v", err)
		}
		accepted, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "recipient@example.com"))
		if err != nil {
			t.Fatal(err)
		}
		if err = e.Sender.Handle(e.Ctx, accepted.EmailID); err != nil {
			t.Fatal(err)
		}
		if observed.calls != 0 || e.Count("SELECT count(*) FROM localmock.ses_outbound") != 0 || e.Count("SELECT count(*) FROM send_attempts WHERE email_id=$1", accepted.EmailID) != 0 || e.Count("SELECT count(*) FROM emails WHERE id=$1 AND send_state='failed'", accepted.EmailID) != 1 {
			t.Fatal("incorrect SES binding reached provider or left send unfinished")
		}
	}
	e.Exec("UPDATE projects SET ses_tenant_name=$2 WHERE id=$1", tn.ProjectID, correct)
	if _, err = dsvc.Verify(e.Ctx, tn.OrgID, tn.ProjectID, d.ID); err != nil || observed.calls != 1 {
		t.Fatal("valid project could not associate", err, observed.calls)
	}
	accepted, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "valid@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Sender.Handle(e.Ctx, accepted.EmailID); err != nil {
		t.Fatal(err)
	}
	if e.Count("SELECT count(*) FROM localmock.ses_outbound WHERE tenant=$1", correct) != 1 {
		t.Fatal("valid production send did not use project's tenant")
	}
}
