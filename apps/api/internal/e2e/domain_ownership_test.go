package e2e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/testenv"
)

type interruptedIdentityCreate struct {
	provider.Provider
	once sync.Once
}

func (p *interruptedIdentityCreate) CreateIdentity(ctx context.Context, domain, mailFrom, tenant string, claimID uuid.UUID) (provider.Identity, error) {
	ident, err := p.Provider.CreateIdentity(ctx, domain, mailFrom, tenant, claimID)
	if err == nil {
		p.once.Do(func() { err = errors.New("simulated loss of provider response") })
	}
	return ident, err
}

func TestDomainReservationSurvivesAmbiguousProviderResult(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	service := *e.Domains
	service.Provider = &interruptedIdentityCreate{Provider: e.Sim}
	const name = "recovery.test"
	if _, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, name, "send"); err == nil {
		t.Fatal("expected interrupted create")
	}
	list, err := service.List(e.Ctx, a.OrgID, a.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var original *domains.Domain
	for _, d := range list {
		if d.Name == name {
			original = d
		}
	}
	if original == nil || original.Status == "verified" || original.ProviderState != "provisioning" {
		t.Fatalf("missing pending claim: %+v", original)
	}
	if _, err := service.Create(e.Ctx, b.OrgID, b.ProjectID, name, "send"); apiErr(t, err).Status != 409 {
		t.Fatalf("other project acquired pending claim: %v", err)
	}
	d, err := service.Create(e.Ctx, a.OrgID, a.ProjectID, name, "send")
	if err != nil || d.ID != original.ID || d.Status != "verified" {
		t.Fatalf("retry did not recover original identity: %+v %v", d, err)
	}

	// DNS alone and a previous verified result cannot authorize a replacement identity.
	other := uuid.New()
	e.Exec(`UPDATE localmock.ses_identities SET claim_id=$1 WHERE domain=$2`, other, name)
	d, err = service.CheckNow(e.Ctx, e.WorkerPool, d)
	if err != nil || d.Status == "verified" || d.VerifiedAt != nil {
		t.Fatalf("wrong owner verified: %+v %v", d, err)
	}
	if err := service.Delete(e.Ctx, a.OrgID, a.ProjectID, d.ID); apiErr(t, err).Status != 409 {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain=$1 AND claim_id=$2`, name, other); n != 1 {
		t.Fatal("removed another provider identity")
	}
	if _, err := service.Create(e.Ctx, b.OrgID, b.ProjectID, name, "send"); apiErr(t, err).Status != 409 {
		t.Fatal("name was released before cleanup")
	}
	e.Exec(`UPDATE localmock.ses_identities SET claim_id=$1 WHERE domain=$2`, d.ID, name)
	if err := service.Delete(e.Ctx, a.OrgID, a.ProjectID, d.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := service.Create(e.Ctx, b.OrgID, b.ProjectID, name, "send")
	if err != nil || replacement.ID == d.ID {
		t.Fatalf("new claim after confirmed deletion: %+v %v", replacement, err)
	}
	if _, err := service.CheckNow(e.Ctx, e.WorkerPool, d); apiErr(t, err).Status != 404 {
		t.Fatalf("stale check: %v", err)
	}
	if n := e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain=$1 AND claim_id=$2`, name, replacement.ID); n != 1 {
		t.Fatal("stale check changed replacement")
	}
}

func TestDomainClaimAndOrganizationLimitAreAtomic(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	type result struct {
		d   *domains.Domain
		err error
	}
	results := make(chan result, 8)
	for _, tenant := range []*testenv.Tenant{a, b} {
		go func() {
			d, err := e.Domains.Create(e.Ctx, tenant.OrgID, tenant.ProjectID, "overlap.test", "send")
			results <- result{d, err}
		}()
	}
	successes := 0
	for range 2 {
		r := <-results
		if r.err == nil {
			successes++
		} else if apiErr(t, r.err).Status != 409 {
			t.Fatal(r.err)
		}
	}
	if successes != 1 || e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain='overlap.test'`) != 1 {
		t.Fatalf("successful claims: %d", successes)
	}
	initial := e.Count(`SELECT count(*) FROM domains WHERE org_id=$1 AND deleted_at IS NULL`, a.OrgID)
	for i := range 6 {
		go func() {
			d, err := e.Domains.Create(e.Ctx, a.OrgID, a.ProjectID, fmt.Sprintf("quota-%d.test", i), "send")
			results <- result{d, err}
		}()
	}
	successes = 0
	for range 6 {
		r := <-results
		if r.err == nil {
			successes++
		} else if apiErr(t, r.err).Status != 403 {
			t.Fatal(r.err)
		}
	}
	if successes != 3-initial {
		t.Fatalf("org had %d domains and created %d more", initial, successes)
	}
	if n := e.Count(`SELECT count(*) FROM localmock.ses_identities WHERE domain LIKE 'quota-%'`); n != successes {
		t.Fatalf("%d provider identities for %d reservations", n, successes)
	}
}

type waitingIdentityCreate struct {
	provider.Provider
	started chan struct{}
	release chan struct{}
}

func (p *waitingIdentityCreate) CreateIdentity(ctx context.Context, name, mailFrom, tenant string, id uuid.UUID) (provider.Identity, error) {
	close(p.started)
	select {
	case <-p.release:
		return p.Provider.CreateIdentity(ctx, name, mailFrom, tenant, id)
	case <-ctx.Done():
		return provider.Identity{}, ctx.Err()
	}
}

func TestDomainDeletionWhileProviderCreateIsRunning(t *testing.T) {
	e := testenv.New(t)
	a, b := e.NewTenant("free"), e.NewTenant("free")
	service := *e.Domains
	slow := &waitingIdentityCreate{e.Sim, make(chan struct{}), make(chan struct{})}
	service.Provider = slow
	ctx, cancel := context.WithTimeout(e.Ctx, 8*time.Second)
	defer cancel()
	result := make(chan error, 1)
	name := "delete-during-create.test"
	go func() { _, err := service.Create(ctx, a.OrgID, a.ProjectID, name, "send"); result <- err }()
	select {
	case <-slow.started:
	case <-ctx.Done():
		t.Fatal("provider create did not start")
	}
	list, err := service.List(ctx, a.OrgID, a.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	for _, d := range list {
		if d.Name == name {
			id = d.ID
		}
	}
	if id == uuid.Nil {
		t.Fatal("missing durable reservation")
	}
	if err := service.Delete(ctx, a.OrgID, a.ProjectID, id); apiErr(t, err).Status != 409 {
		t.Fatal(err)
	}
	if _, err := e.Domains.Create(ctx, b.OrgID, b.ProjectID, name, "send"); apiErr(t, err).Status != 409 {
		t.Fatal("new holder during create", err)
	}
	close(slow.release)
	if err := <-result; apiErr(t, err).Status != 409 {
		t.Fatal("stale provider result", err)
	}
	d, err := service.Get(ctx, a.OrgID, a.ProjectID, id)
	if err != nil || d.ProviderState != "deleting" || d.Status == "verified" {
		t.Fatalf("stale state restored: %+v %v", d, err)
	}
	if err := service.Delete(ctx, a.OrgID, a.ProjectID, id); err != nil {
		t.Fatal(err)
	}
	replacement, err := e.Domains.Create(ctx, b.OrgID, b.ProjectID, name, "send")
	if err != nil || replacement.ID == id || replacement.Status != "verified" {
		t.Fatalf("replacement: %+v %v", replacement, err)
	}
}
