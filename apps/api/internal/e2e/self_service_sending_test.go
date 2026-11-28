package e2e

import (
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/testenv"
	"testing"
	"time"
)

func TestDomainAndTermsAutomaticallyEnableSending(t *testing.T) {
	e := testenv.New(t)
	_, _, org, project := signupForSecurity(t, e)
	service := *e.Domains
	dns := &publishedDomainDNS{}
	service.MockDNS, service.Resolver = false, dns
	d, err := service.Create(e.Ctx, org, project, "automatic.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	dns.records = d.Records
	e.Exec(`UPDATE domains SET last_checked_at=now()-interval '11 seconds' WHERE id=$1`, d.ID)
	d, err = service.Verify(e.Ctx, org, project, d.ID)
	if err != nil || len(d.DKIMTokens) != 3 {
		t.Fatal("ownership did not provision", err)
	}
	dns.records = d.Records
	e.Exec(`UPDATE domains SET last_checked_at=now()-interval '11 seconds' WHERE id=$1`, d.ID)
	d, err = service.Verify(e.Ctx, org, project, d.ID)
	if err != nil || d.Status != "verified" {
		t.Fatal("DNS", err)
	}
	if e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='test_only'`, project) != 1 {
		t.Fatal("API made worker decision")
	}
	if d.NextCheckAt == nil || d.NextCheckAt.After(time.Now().Add(time.Minute)) {
		t.Fatal("worker not scheduled")
	}
	for range 2 {
		d, err = service.CheckNow(e.Ctx, e.WorkerPool, d)
		if err != nil {
			t.Fatal(err)
		}
	}
	if e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='approved' AND hourly_limit=25 AND daily_limit=100`, project) != 1 {
		t.Fatal("automatic activation failed")
	}
	if e.Count(`SELECT count(*) FROM audit_log WHERE action='project.live_auto_activated' AND target_id=$1`, project.String()) != 1 {
		t.Fatal("activation was not exactly once")
	}
	e.Exec(`UPDATE projects SET sending_status='paused' WHERE id=$1`, project)
	_, err = service.CheckNow(e.Ctx, e.WorkerPool, d)
	if err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='paused'`, project) != 1 {
		t.Fatal("verification reopened paused project")
	}
}

func TestAutomaticSendingRequiresExactOwnerConsentAndImmutableClaim(t *testing.T) {
	e := testenv.New(t)
	_, user, org, project := signupForSecurity(t, e)
	service := *e.Domains
	dns := &publishedDomainDNS{}
	service.MockDNS, service.Resolver = false, dns
	d, err := service.Create(e.Ctx, org, project, "consent.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	dns.records = d.Records
	for _, mutation := range []string{
		`UPDATE memberships SET role='developer' WHERE user_id=$1`,
		`UPDATE legal_receipts SET documents='[]'::jsonb WHERE user_id=$1`,
	} {
		e.Exec(mutation, user)
		d, err = service.Verify(e.Ctx, org, project, d.ID)
		if err != nil || d.ProviderState != "awaiting_dns" {
			t.Fatal("missing valid consent provisioned", err)
		}
		e.Exec(`UPDATE memberships SET role='owner' WHERE user_id=$1`, user)
	}
	e.Exec(`UPDATE legal_receipts SET documents=$2 WHERE user_id=$1`, user, legal.Current().Documents)
	other := e.NewTenant("free")
	err = db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(e.Ctx, `SELECT hedvi_org_has_live_consent($1,$2,$3,$4)`, other.OrgID, legal.Current().Version, legal.Current().Documents, legal.Current().Adopted).Scan(&ok); err != nil {
			return err
		}
		if ok {
			t.Fatal("other organization consent leaked")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`UPDATE domains SET name='different.example.org' WHERE id=$1`,
		`UPDATE domains SET mail_from_subdomain='changed' WHERE id=$1`,
		`DELETE FROM domains WHERE id=$1`,
	} {
		err = db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error { _, err := tx.Exec(e.Ctx, sql, d.ID); return err })
		if err == nil {
			t.Fatal("claim mutation allowed", sql)
		}
	}
	err = db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `UPDATE projects SET sending_status='approved' WHERE id=$1`, project)
		return err
	})
	if err == nil {
		t.Fatal("API approved project")
	}
	e.Exec(`UPDATE organizations SET risk_status='blocked' WHERE id=$1`, org)
	d, err = service.Verify(e.Ctx, org, project, d.ID)
	if err != nil || d.ProviderState != "awaiting_dns" {
		t.Fatal("blocked organization provisioned", err)
	}
}
