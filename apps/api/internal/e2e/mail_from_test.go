package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
)

type mailFromProvider struct {
	provider.Provider
	reportedHost string
}

func (p *mailFromProvider) GetIdentity(ctx context.Context, name string, id uuid.UUID) (provider.IdentityStatus, error) {
	if _, err := p.Provider.GetIdentity(ctx, name, id); err != nil {
		return provider.IdentityStatus{}, err
	}
	return provider.IdentityStatus{VerifiedForSending: true, DKIMStatus: "SUCCESS", MailFromStatus: "SUCCESS", MailFromDomain: p.reportedHost}, nil
}

func TestMailFromCorrectionPreservesClaimAndScopedKey(t *testing.T) {
	e := testenv.New(t)
	cookie, _, org, project := signupForSecurity(t, e)
	const domain = "customer.example.org"
	dns := &publishedDomainDNS{}
	p := &mailFromProvider{Provider: e.Sim, reportedHost: "send." + domain}
	e.Domains.MockDNS, e.Domains.Provider, e.Domains.Resolver = false, p, dns
	d, err := e.Domains.Create(e.Ctx, org, project, domain, "send")
	if err != nil {
		t.Fatal(err)
	}
	verify := func() {
		t.Helper()
		e.Exec(`UPDATE domains SET last_checked_at=NULL WHERE id=$1`, d.ID)
		d, err = e.Domains.Verify(e.Ctx, org, project, d.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	dns.records = d.Records
	verify()
	dns.records = d.Records
	verify()
	if d.Status != "verified" {
		t.Fatal(d.Status)
	}
	claim := d.ID
	other := e.NewTenant("free")
	for _, tc := range []struct {
		org, project uuid.UUID
		label        string
		revision     int64
	}{{other.OrgID, project, "bounces", 0}, {org, other.ProjectID, "bounces", 0}, {org, project, "bad.label", 0}, {org, project, "bounces", 1}} {
		if err := db.InOrg(e.Ctx, e.APIPool, tc.org, func(tx pgx.Tx) error {
			var changed bool
			err := tx.QueryRow(e.Ctx, `SELECT hedvi_change_mail_from($1,$2,'send',$3,$4)`, tc.project, claim, tc.revision, tc.label).Scan(&changed)
			if changed {
				t.Fatal("invalid or out-of-scope transition succeeded")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(e.Ctx, `UPDATE domains SET mail_from_revision=mail_from_revision+1 WHERE id=$1`, claim)
		return err
	}); err == nil {
		t.Fatal("direct revision mutation allowed")
	}
	prefix := "/console/v1/projects/" + project.String()
	path := prefix + "/domains/" + claim.String()
	if code := cookieRequest(e.Handler, cookie, "POST", prefix+"/api-keys", map[string]any{"name": "dedicated sender", "domain_id": claim, "mode": "live", "permission": "sending_access"}); code != 201 {
		t.Fatal(code)
	}
	change := func(label string) int {
		return cookieRequest(e.Handler, cookie, "PATCH", path+"/mail-from", map[string]any{"previous": "send", "revision": 0, "label": label})
	}
	if code := cookieRequest(e.Handler, cookie, "POST", path+"/dns-export", nil); code != 200 {
		t.Fatal(code)
	}
	priority := 5
	dns.records = append(dns.records, domains.Record{Type: "MX", FQDN: "send." + domain, Value: "smtp.google.com", Priority: &priority},
		domains.Record{Type: "MX", FQDN: "inbox." + domain, Value: "customer.mail.protection.outlook.com", Priority: &priority})
	if code := cookieRequest(e.Handler, cookie, "POST", path+"/dns-export", nil); code != 409 {
		t.Fatal("stale clear result allowed an export", code)
	}
	if code := change("inbox"); code != 409 {
		t.Fatal("occupied replacement accepted", code)
	}
	if code := change("bounces"); code != 200 {
		t.Fatal("unused replacement refused", code)
	}
	if code := change("bounces"); code != 200 {
		t.Fatal("retry after successful change", code)
	}
	if code := change("another"); code != 409 {
		t.Fatal("stale edit won", code)
	}
	if e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND provider_state='provisioning' AND status='pending' AND verified_at IS NULL AND mail_from_revision=1 AND mail_from_reconfiguring`, claim) != 1 ||
		e.Count(`SELECT count(*) FROM api_keys WHERE domain_id=$1 AND revoked_at IS NULL`, claim) != 1 ||
		e.Count(`SELECT count(*) FROM audit_log WHERE action='domain.mail_from_changed' AND target_id=$1`, claim.String()) != 1 {
		t.Fatal("scope, verification or audit not preserved")
	}
	verify()
	dns.records = d.Records
	verify()
	if d.Status == "verified" {
		t.Fatal("provider SUCCESS for previous MAIL FROM was trusted")
	}
	p.reportedHost = "bounces." + domain
	verify()
	if d.ID != claim || d.Status != "verified" || e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND mail_from_revision=1 AND NOT mail_from_reconfiguring`, claim) != 1 || e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain=$1`, domain) != 1 {
		t.Fatal("same identity did not recover")
	}
}
