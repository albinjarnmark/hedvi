package e2e

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
)

// The published DNS is independent of the service's current expected records.
// It intentionally survives deleting and reclaiming the provider identity.
type publishedDomainDNS struct{ records []domains.Record }

func (r *publishedDomainDNS) find(name, kind string) []domains.Record {
	var found []domains.Record
	for _, record := range r.records {
		if record.FQDN == strings.TrimSuffix(name, ".") && record.Type == kind {
			found = append(found, record)
		}
	}
	return found
}

func dnsMissing(name string) error {
	return &net.DNSError{Name: name, Err: "no such host", IsNotFound: true}
}

func (r *publishedDomainDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	var values []string
	for _, record := range r.find(name, "TXT") {
		values = append(values, record.Value)
	}
	if len(values) == 0 {
		return nil, dnsMissing(name)
	}
	return values, nil
}

func (r *publishedDomainDNS) LookupCNAME(_ context.Context, name string) (string, error) {
	if records := r.find(name, "CNAME"); len(records) != 0 {
		return records[0].Value + ".", nil
	}
	return "", dnsMissing(name)
}

func (r *publishedDomainDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	var values []*net.MX
	for _, record := range r.find(name, "MX") {
		values = append(values, &net.MX{Host: record.Value + ".", Pref: uint16(*record.Priority)})
	}
	if len(values) == 0 {
		return nil, dnsMissing(name)
	}
	return values, nil
}

func (r *publishedDomainDNS) LookupNS(_ context.Context, name string) ([]*net.NS, error) {
	return nil, dnsMissing(name)
}

func TestReclaimedDomainRequiresFreshDNSOwnership(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	service := *e.Domains
	published := &publishedDomainDNS{}
	service.MockDNS, service.Resolver = false, published
	const name = "reclaimed.example.org"
	first, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, name, "send")
	if err != nil || first.Status == "verified" {
		t.Fatalf("unpublished first claim: %+v %v", first, err)
	}
	other, err := service.Create(e.Ctx, b.OrgID, b.ProjectID, name, "send")
	if err != nil || other.ID == first.ID {
		t.Fatalf("independent challenge: %+v %v", other, err)
	}
	if _, err := service.Verify(e.Ctx, b.OrgID, b.ProjectID, other.ID); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain=$1`, name) != 0 || len(first.DKIMTokens) != 0 {
		t.Fatal("unproven challenge allocated a provider identity")
	}
	published.records = append([]domains.Record(nil), first.Records...)
	first, err = service.Verify(e.Ctx, a.OrgID, a.ProjectID, first.ID)
	if err != nil || len(first.DKIMTokens) == 0 || first.Status == "verified" {
		t.Fatalf("proved first claim: %+v %v", first, err)
	}
	published.records = append([]domains.Record(nil), first.Records...)
	first, err = service.CheckNow(e.Ctx, e.WorkerPool, first)
	if err != nil || first.Status != "verified" {
		t.Fatalf("published first claim: %+v %v", first, err)
	}
	if err := service.Delete(e.Ctx, a.OrgID, a.ProjectID, first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := service.Create(e.Ctx, b.OrgID, b.ProjectID, name, "send")
	if err != nil || second.ID == first.ID || second.Status == "verified" || second.VerifiedAt != nil {
		t.Fatalf("old DNS authorized new claim: %+v %v", second, err)
	}
	second, err = service.Verify(e.Ctx, b.OrgID, b.ProjectID, second.ID)
	if err != nil || second.ProviderState != "awaiting_dns" || len(second.DKIMTokens) != 0 {
		t.Fatalf("old DNS reached provider: %+v %v", second, err)
	}
	b.Domain = name
	if _, err := e.Accept.Send(e.Ctx, b.Live, "", liveBody(b, "recipient@example.com")); apiErr(t, err).Status != 403 {
		t.Fatal("old ownership proof allowed live sending")
	}
	// Leave all old selectors published while adding the correct new proof.
	published.records = append(published.records, second.Records...)
	second, err = service.Verify(e.Ctx, b.OrgID, b.ProjectID, second.ID)
	if err != nil || second.Status != "verified" {
		t.Fatalf("new DNS proof did not verify: %+v %v", second, err)
	}
	if !reflect.DeepEqual(first.DKIMTokens, second.DKIMTokens) {
		t.Fatal("expected reused provider selectors")
	}
	accepted, err := e.Accept.Send(e.Ctx, b.Live, "", liveBody(b, "recipient@example.com"))
	if err != nil {
		t.Fatalf("fresh proof did not allow sending: %v", err)
	}
	// Removing proof also revokes eligibility at the next scheduled check.
	published.records = first.Records
	second, err = service.CheckNow(e.Ctx, e.WorkerPool, second)
	if err != nil || second.Status == "verified" || second.VerifiedAt != nil {
		t.Fatalf("removed proof retained eligibility: %+v %v", second, err)
	}
	if _, err := e.Accept.Send(e.Ctx, b.Live, "", liveBody(b, "recipient@example.com")); apiErr(t, err).Status != 403 {
		t.Fatal("removed ownership proof allowed live sending")
	}
	// The worker also rechecks eligibility for a message accepted before the
	// DNS proof disappeared, so queued work cannot bypass this revocation.
	e.Drain()
	if outbound(e) != 0 || e.Count(`SELECT count(*) FROM emails WHERE id=$1 AND send_state='failed'`, accepted.EmailID) != 1 ||
		e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND status_detail LIKE 'sender_domain_unavailable:%'`, accepted.EmailID) != 1 {
		t.Fatal("queued message escaped revoked domain ownership")
	}
}

func TestDomainDNSOwnershipMigrationRevokesLegacyVerification(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	// Rerun only 0005 on this private database, populated with a previously
	// verified domain that has no ownership record, as on a 0004 installation.
	cfg := e.WorkerPool.Config()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "hedvi_owner", "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(e.Ctx, `DELETE FROM schema_migrations WHERE version='0005_domain_dns_ownership'`); err != nil {
		t.Fatal(err)
	}
	applied, err := db.Migrate(e.Ctx, owner)
	if err != nil || len(applied) != 1 || applied[0] != "0005_domain_dns_ownership" {
		t.Fatalf("upgrade applied %v: %v", applied, err)
	}
	list, err := e.Domains.List(e.Ctx, tn.OrgID, tn.ProjectID)
	if err != nil || len(list) != 1 {
		t.Fatalf("legacy domain list: %+v %v", list, err)
	}
	d := list[0]
	if d.Status != "pending" || d.VerifiedAt != nil || d.NextCheckAt == nil || len(d.Records) != 1 ||
		d.Records[0].Value != "hedvi-domain="+d.ID.String() || !d.Records[0].Required {
		t.Fatalf("legacy domain did not fail closed with a current DNS guide: %+v", d)
	}
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "recipient@example.com")); apiErr(t, err).Status != 403 {
		t.Fatal("unproven legacy domain allowed live sending")
	}
}

type waitingDomainDNS struct {
	*publishedDomainDNS
	started chan struct{}
	release chan struct{}
}

func (r *waitingDomainDNS) LookupTXT(ctx context.Context, name string) ([]string, error) {
	close(r.started)
	select {
	case <-r.release:
		return nil, dnsMissing(name)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSlowUnprovenChallengeReleasesDatabaseConnectionAndExpires(t *testing.T) {
	e := testenv.New(t)
	a := e.NewTenant("free")
	cfg := e.APIPool.Config()
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	dns := &waitingDomainDNS{&publishedDomainDNS{}, make(chan struct{}), make(chan struct{})}
	service := *e.Domains
	service.MockDNS, service.Pool, service.Resolver = false, p, dns
	d, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, "challenge.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	ctx, cancel := context.WithTimeout(e.Ctx, 5*time.Second)
	defer cancel()
	go func() { _, err := service.Verify(ctx, a.OrgID, a.ProjectID, d.ID); result <- err }()
	select {
	case <-dns.started:
	case <-ctx.Done():
		t.Fatal("DNS was not reached")
	}
	pingErr := p.Ping(ctx)
	close(dns.release)
	if err := <-result; err != nil || pingErr != nil {
		t.Fatalf("verification occupied only connection: %v / %v", err, pingErr)
	}
	e.Exec(`UPDATE domains SET created_at=now()-interval '73 hours' WHERE id=$1`, d.ID)
	_, err = service.Verify(e.Ctx, a.OrgID, a.ProjectID, d.ID)
	if apiErr(t, err).Status != 404 {
		t.Fatal(err)
	}
	replacement, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, d.Name, "send")
	if err != nil || replacement.ID == d.ID || e.Count(`SELECT count(*) FROM localmock.ses_identities`) != 0 {
		t.Fatalf("expired challenge: %+v %v", replacement, err)
	}
}

type countingTenantProvider struct {
	provider.Provider
	calls int
}

func (p *countingTenantProvider) EnsureTenant(ctx context.Context, name string) error {
	p.calls++
	return p.Provider.EnsureTenant(ctx, name)
}

func TestUnreviewedProjectWithDNSCannotAllocateSES(t *testing.T) {
	e := testenv.New(t)
	a := e.NewTenant("free")
	dns := &publishedDomainDNS{}
	service := *e.Domains
	service.MockDNS, service.Resolver = false, dns
	observed := &countingTenantProvider{Provider: e.Sim}
	service.Provider = observed
	e.Exec(`UPDATE projects SET sending_status='test_only' WHERE id=$1`, a.ProjectID)
	d, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, "review.example.org", "send")
	if err != nil {
		t.Fatal(err)
	}
	dns.records = d.Records
	for _, state := range []string{"test_only", "pending_review", "paused"} {
		e.Exec(`UPDATE projects SET sending_status=$2 WHERE id=$1`, a.ProjectID, state)
		d, err = service.Verify(e.Ctx, a.OrgID, a.ProjectID, d.ID)
		if err != nil || d.ProviderState != "awaiting_dns" || d.Records[0].Status != "verified" ||
			len(d.Analysis.Findings) != 1 || d.Analysis.Findings[0].Code != "live_access_required" ||
			e.Count(`SELECT count(*) FROM localmock.ses_identities`) != 0 || observed.calls != 0 {
			t.Fatalf("project %s consumed resources or lost proof: %+v %v", state, d, err)
		}
	}
	e.Exec(`UPDATE projects SET sending_status='approved' WHERE id=$1`, a.ProjectID)
	d, err = service.Verify(e.Ctx, a.OrgID, a.ProjectID, d.ID)
	if err != nil || d.ProviderState != "active" || len(d.DKIMTokens) == 0 {
		t.Fatalf("approved project failed: %+v %v", d, err)
	}
}
