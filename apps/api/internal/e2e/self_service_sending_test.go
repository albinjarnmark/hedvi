package e2e

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/testenv"
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
	assertWaiting := func(reason string) {
		t.Helper()
		// An unchanged cached response would also be awaiting_dns. Require a new check.
		e.Exec(`UPDATE domains SET last_checked_at=NULL WHERE id=$1`, d.ID)
		checked, err := service.Verify(e.Ctx, org, project, d.ID)
		if err != nil {
			t.Fatal(reason, err)
		}
		if checked.ProviderState != "awaiting_dns" || checked.LastCheckedAt == nil ||
			len(checked.Records) != 1 || checked.Records[0].Record != "Ownership" || checked.Records[0].Status != "verified" {
			t.Fatal(reason, "expected fresh ownership proof without provider provisioning")
		}
	}
	e.Exec(`UPDATE memberships SET role='developer' WHERE org_id=$1 AND user_id=$2`, org, user)
	assertWaiting("a developer's current receipt")

	// Receipts are append-only. Give a different verified owner an original
	// receipt with the correct version, adoption flag and six documents. Only
	// one digest differs from the published manifest.
	manifest := legal.Current()
	otherOwner := uuid.New()
	e.Exec(`INSERT INTO users(id,email,password_hash,email_verified_at) VALUES ($1,'other-owner@example.test','unused-test-hash',now())`, otherOwner)
	e.Exec(`INSERT INTO memberships(org_id,user_id,role) VALUES ($1,$2,'owner')`, org, otherOwner)
	e.Exec(`INSERT INTO legal_receipts(user_id,manifest_version,documents,document_origin,manifest_adopted,user_agent)
		VALUES ($1,$2,jsonb_set($3::jsonb,'{0,sha256}',to_jsonb(repeat('0',64)),false),'https://legal.example.test',$4,'fixture')`,
		otherOwner, manifest.Version, manifest.Documents, manifest.Adopted)
	assertWaiting("an owner's mismatched document digests")

	e.Exec(`UPDATE memberships SET role='owner' WHERE org_id=$1 AND user_id=$2`, org, user)
	other := e.NewTenant("free")
	for _, scope := range []uuid.UUID{org, other.OrgID} {
		var ok bool
		err := db.InOrg(e.Ctx, e.APIPool, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(e.Ctx, `SELECT hedvi_org_has_live_consent($1,$2,$3,$4)`, org, manifest.Version, manifest.Documents, manifest.Adopted).Scan(&ok)
		})
		if err != nil {
			t.Fatal(err)
		}
		if ok != (scope == org) {
			t.Fatal("valid receipt must be visible only within its own organization")
		}
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
	assertWaiting("blocked organization with a valid owner's receipt")
}
