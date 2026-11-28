package e2e

import (
	"errors"
	"hedvi.dev/api/internal/apierr"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/billing"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
)

func testBillingProfile() billing.Profile {
	return billing.Profile{Name: "Örebro Test AB", Line1: "Storgatan 1", City: "Örebro", PostalCode: "701 10", Country: "SE"}
}

type billingAddressHarness struct {
	project   uuid.UUID
	e         *testenv.Env
	f         *stripeFixture
	cookie    *http.Cookie
	user, org uuid.UUID
	path      string
	worker    *billing.Service
}

func newBillingAddressHarness(t *testing.T) billingAddressHarness {
	t.Helper()
	e := testenv.New(t)
	cookie, user, org, project := signupForSecurity(t, e)
	now := time.Now().UTC().Truncate(time.Second)
	f := &stripeFixture{sku: "flex", currency: "sek", amount: 3900, price: "price_hedvi", session: "cs_test_hedvi", invoiceID: "in_hedvi", status: "open", start: now.Unix(), end: now.AddDate(0, 1, 0).Unix(), customerDetailsCountry: "US"}
	original := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = original })
	c := billing.Config{Portal: "bpc_hedvi", Account: "acct_hedvi", AppURL: "https://app.example.test", Countries: map[string]bool{"SE": true}, Prices: map[string]billing.Price{"flex_sek": {SKU: "flex", Currency: "sek", ID: f.price, Product: "prod_hedvi", Amount: f.amount}}}
	worker := billing.New(c, e.WorkerPool, true)
	if err := worker.Validate(e.Ctx); err != nil {
		t.Fatal(err)
	}
	e.Console.Billing = billing.New(c, e.APIPool, false)
	return billingAddressHarness{project: project, e: e, f: f, cookie: cookie, user: user, org: org, path: "/console/v1/projects/" + project.String() + "/billing/checkout", worker: worker}
}
func (h billingAddressHarness) request(profile any) int {
	return cookieRequest(h.e.Handler, h.cookie, "POST", h.path, billingJSON{"sku": "flex", "currency": "sek", "billing_profile": profile, "request_id": uuid.New()})
}

func TestStripeBillingAddressBoundBeforeCheckout(t *testing.T) {
	h := newBillingAddressHarness(t)
	for _, mutate := range []func(*billing.Profile){
		func(p *billing.Profile) { p.Country = "DE" },
		func(p *billing.Profile) { p.Name = "" },
		func(p *billing.Profile) { p.PostalCode = "" },
		func(p *billing.Profile) { p.Line1 = "line\nheader" },
	} {
		profile := testBillingProfile()
		mutate(&profile)
		if got := h.request(profile); got != 422 {
			t.Fatalf("invalid profile: %d", got)
		}
	}
	if h.f.customerCreates != 0 || h.f.sessionCreates != 0 || h.e.Count(`SELECT count(*) FROM billing_customers`) != 0 {
		t.Fatal("invalid address reached Stripe or persisted")
	}
	if got := h.request(testBillingProfile()); got != 200 {
		t.Fatalf("checkout: %d", got)
	}
	if h.e.Count(`SELECT count(*) FROM billing_customers c JOIN billing_orders o USING(org_id) WHERE c.billing_profile->>'country'=o.country AND o.country='SE'`) != 1 {
		t.Fatal("order not bound to saved country")
	}
	status, err := h.e.Console.Billing.Status(h.e.Ctx, h.org, h.user)
	if err != nil || status.Profile == nil || *status.Profile != testBillingProfile() {
		t.Fatalf("saved invoice profile: %+v %v", status.Profile, err)
	}
	if _, err := h.e.WorkerPool.Exec(h.e.Ctx, `UPDATE billing_customers SET billing_profile=jsonb_set(billing_profile,'{country}','"DE"')`); err == nil {
		t.Fatal("saved profile was mutable")
	}
	if _, err := h.e.WorkerPool.Exec(h.e.Ctx, `UPDATE billing_orders SET country='DE'`); err == nil {
		t.Fatal("order country diverged")
	}
	if err := db.InOrg(h.e.Ctx, h.e.APIPool, h.org, func(tx pgx.Tx) error {
		_, err := tx.Exec(h.e.Ctx, `UPDATE billing_customers SET billing_profile=NULL WHERE org_id=$1`, h.org)
		return err
	}); err == nil {
		t.Fatal("API can erase profile binding")
	}
}

func TestStripeBillingAddressConcurrentProfiles(t *testing.T) {
	h := newBillingAddressHarness(t)
	first, second := testBillingProfile(), testBillingProfile()
	second.Line1 = "Other street 2"
	results := make(chan int, 2)
	go func() { results <- h.request(first) }()
	go func() { results <- h.request(second) }()
	codes := map[int]int{}
	for range 2 {
		codes[<-results]++
	}
	if codes[200] != 1 || codes[409] != 1 || h.f.customerCreates != 1 || h.f.sessionCreates != 1 {
		t.Fatalf("parallel profiles: %v, customers=%d sessions=%d", codes, h.f.customerCreates, h.f.sessionCreates)
	}
	if h.e.Count(`SELECT count(*) FROM billing_customers`) != 1 || h.e.Count(`SELECT count(*) FROM billing_orders`) != 1 {
		t.Fatal("parallel duplicate binding")
	}
}

func TestStripeBillingAddressRetryKeepsParameters(t *testing.T) {
	for _, lost := range []string{"customer", "session"} {
		t.Run(lost, func(t *testing.T) {
			h := newBillingAddressHarness(t)
			if lost == "customer" {
				h.f.lostCustomerResponses = 1
			} else {
				h.f.lostSessionResponses = 1
			}
			if got := h.request(testBillingProfile()); got != 503 {
				t.Fatalf("lost response: %d", got)
			}
			other := testBillingProfile()
			other.Line1 = "Changed street 3"
			if got := h.request(other); got != 409 {
				t.Fatalf("ambiguous outcome allowed replacement: %d", got)
			}
			if got := h.request(testBillingProfile()); got != 200 {
				t.Fatalf("retry: %d", got)
			}
			if lost == "customer" {
				if len(h.f.customerKeys) != 2 || h.f.customerKeys[0] != h.f.customerKeys[1] || h.f.customerForms[0] != h.f.customerForms[1] {
					t.Fatal("customer replay changed idempotency key or body")
				}
			} else if len(h.f.checkoutForms) != 2 || h.f.checkoutForms[0] != h.f.checkoutForms[1] {
				t.Fatal("checkout replay changed invoice binding")
			}
			if h.e.Count(`SELECT count(*) FROM billing_customers`) != 1 || h.e.Count(`SELECT count(*) FROM billing_orders`) != 1 {
				t.Fatal("retry duplicated local purchase")
			}
		})
	}
}

func TestStripeBillingAddressRejectsExternalDrift(t *testing.T) {
	h := newBillingAddressHarness(t)
	if got := h.request(testBillingProfile()); got != 200 {
		t.Fatalf("checkout: %d", got)
	}
	h.f.customerAddress["country"] = "DE"
	if got := h.request(testBillingProfile()); got != 409 {
		t.Fatalf("foreign invoice address: %d", got)
	}
	h.f.customerAddress["country"] = "SE"
	h.f.customerShipping = billingJSON{"name": "Company", "address": billingJSON{"country": "SE"}}
	if got := h.request(testBillingProfile()); got != 409 {
		t.Fatalf("shipping can override tax address: %d", got)
	}
	h.f.customerShipping = nil
	h.f.customerAddress["postal_code"] = "10000"
	if got := h.request(testBillingProfile()); got != 409 {
		t.Fatalf("changed tax location: %d", got)
	}
	if h.f.sessionCreates != 1 {
		t.Fatal("drift created another payable session")
	}
}

func TestStripeBillingAddressTaxRejectionCannotCreateCheckout(t *testing.T) {
	h := newBillingAddressHarness(t)
	h.f.rejectCustomer = true
	if got := h.request(testBillingProfile()); got != 422 {
		t.Fatalf("tax address rejection: %d", got)
	}
	if h.f.sessionCreates != 0 || h.e.Count(`SELECT count(*) FROM billing_customers WHERE customer_id IS NOT NULL`) != 0 {
		t.Fatal("unvalidated location reached checkout")
	}
	if h.e.Count(`SELECT count(*) FROM billing_orders`) != 0 {
		t.Fatal("rejected customer has an order")
	}
	status, err := h.e.Console.Billing.Status(h.e.Ctx, h.org, h.user)
	if err != nil || !status.ProfileEditable {
		t.Fatal("definite rejection not editable", err)
	}
	other := testBillingProfile()
	other.PostalCode = "702 10"
	h.f.rejectCustomer = false
	if got := h.request(other); got != 200 {
		t.Fatalf("corrected address: %d", got)
	}
	if len(h.f.customerKeys) != 2 || h.f.customerKeys[0] == h.f.customerKeys[1] || h.f.customerForms[0] == h.f.customerForms[1] {
		t.Fatal("changed profile reused its old idempotency key")
	}
	if h.f.customerAddress["postal_code"] != other.PostalCode || h.f.sessionCreates != 1 {
		t.Fatal("corrected profile not used for invoice")
	}
	status, err = h.e.Console.Billing.Status(h.e.Ctx, h.org, h.user)
	if err != nil || status.ProfileEditable {
		t.Fatal("successful profile still editable", err)
	}
}

func TestStripeForeignInvoiceStillWithholdsCredits(t *testing.T) {
	h := newBillingAddressHarness(t)
	if got := h.request(testBillingProfile()); got != 200 {
		t.Fatalf("checkout: %d", got)
	}
	h.f.paid, h.f.status, h.f.invoiceCountry = true, "complete", "DE"
	if err := h.worker.RunOnce(h.e.Ctx); err != nil {
		t.Fatal(err)
	}
	if h.e.Count(`SELECT count(*) FROM billing_invoices WHERE state='review'`) != 1 || h.e.Count(`SELECT count(*) FROM credit_lots`) != 0 {
		t.Fatal("foreign canonical invoice granted credits")
	}
}

func TestStripeBillingAddressMigrationDoesNotAdoptLegacyCustomers(t *testing.T) {
	h := newBillingAddressHarness(t)
	cfg := h.e.WorkerPool.Config()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "hedvi_owner", "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(h.e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err := owner.Exec(h.e.Ctx, `DROP TRIGGER billing_order_country ON billing_orders;
        DROP TRIGGER billing_profile_immutable ON billing_customers;
        DROP FUNCTION hedvi_billing_profile_immutable();
        DROP FUNCTION hedvi_retry_billing_profile(uuid,jsonb,text),hedvi_claim_billing_customer(uuid,uuid,uuid),hedvi_fail_billing_customer(uuid,uuid,uuid,boolean);
        ALTER TABLE billing_customers DROP COLUMN billing_profile,DROP COLUMN profile_rejected,DROP COLUMN creation_uncertain,DROP COLUMN creation_lease,DROP COLUMN creation_lease_until;
        DELETE FROM schema_migrations WHERE version='0011_billing_address'`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(h.e.Ctx, `INSERT INTO billing_customers(org_id,currency,customer_id) VALUES($1,'sek','cus_hedvi')`, h.org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(h.e.Ctx, owner); err != nil {
		t.Fatal(err)
	}
	if got := h.request(testBillingProfile()); got != 409 {
		t.Fatalf("legacy customer silently adopted: %d", got)
	}
	if h.f.customerCreates != 0 || h.f.sessionCreates != 0 || h.e.Count(`SELECT count(*) FROM billing_orders`) != 0 {
		t.Fatal("legacy state reached a new checkout")
	}
	if h.e.Count(`SELECT count(*) FROM billing_customers WHERE billing_profile IS NULL AND customer_id='cus_hedvi'`) != 1 {
		t.Fatal("migration inferred an invoice address")
	}
}

func TestStripeBillingAddressRejectionAfterTimeoutStaysLocked(t *testing.T) {
	h := newBillingAddressHarness(t)
	h.f.lostCustomerResponses = 1
	if got := h.request(testBillingProfile()); got != 503 {
		t.Fatalf("lost response: %d", got)
	}
	h.f.rejectCustomer = true
	if got := h.request(testBillingProfile()); got != 422 {
		t.Fatalf("later rejection: %d", got)
	}
	status, err := h.e.Console.Billing.Status(h.e.Ctx, h.org, h.user)
	if err != nil || status.ProfileEditable {
		t.Fatal("ambiguous attempt became editable", err)
	}
	other := testBillingProfile()
	other.PostalCode = "702 10"
	if got := h.request(other); got != 409 {
		t.Fatalf("ambiguous customer replaced: %d", got)
	}
	if h.e.Count(`SELECT count(*) FROM billing_orders`) != 0 || h.f.sessionCreates != 0 {
		t.Fatal("unconfirmed customer reached checkout")
	}
}

func TestStripeBillingAddressLeaseTakeoverCannotPermitEditing(t *testing.T) {
	h := newBillingAddressHarness(t)
	profile := testBillingProfile()
	var attempt uuid.UUID
	firstClaim, secondClaim := uuid.New(), uuid.New()
	if err := db.InUser(h.e.Ctx, h.e.APIPool, h.user, &h.org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(h.e.Ctx, `INSERT INTO billing_customers(org_id,currency,billing_profile) VALUES($1,'sek',$2) RETURNING id`, h.org, profile).Scan(&attempt); err != nil {
			return err
		}
		var ok bool
		if err := tx.QueryRow(h.e.Ctx, `SELECT hedvi_claim_billing_customer($1,$2,$3)`, h.org, attempt, firstClaim).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			t.Fatal("first claim denied")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := h.request(profile); got != 409 || h.f.customerCreates != 0 {
		t.Fatal("live lease allowed a competing creator", got)
	}
	h.e.Exec(`UPDATE billing_customers SET creation_lease_until=now()-interval '1 second' WHERE org_id=$1`, h.org)
	if err := db.InUser(h.e.Ctx, h.e.APIPool, h.user, &h.org, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(h.e.Ctx, `SELECT hedvi_claim_billing_customer($1,$2,$3)`, h.org, attempt, secondClaim).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			t.Fatal("expired claim could not recover")
		}
		if err := tx.QueryRow(h.e.Ctx, `SELECT hedvi_fail_billing_customer($1,$2,$3,true)`, h.org, attempt, firstClaim).Scan(&ok); err != nil {
			return err
		}
		if ok {
			t.Fatal("stale failure unlocked profile")
		}
		if err := tx.QueryRow(h.e.Ctx, `SELECT hedvi_fail_billing_customer($1,$2,$3,true)`, h.org, attempt, secondClaim).Scan(&ok); err != nil {
			return err
		}
		if ok {
			t.Fatal("takeover failure unlocked ambiguous profile")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	other := profile
	other.Line1 = "Another address"
	if got := h.request(other); got != 409 {
		t.Fatalf("crashed attempt replaced: %d", got)
	}
}

func TestStripeBillingAddressOnlyDefinitiveLocationErrorsAllowCorrection(t *testing.T) {
	for _, scenario := range []string{"other_400", "retryable_400"} {
		t.Run(scenario, func(t *testing.T) {
			h := newBillingAddressHarness(t)
			h.f.rejectCustomer = true
			if scenario == "other_400" {
				h.f.customerErrorCode = "idempotency_error"
			} else {
				h.f.customerRetryable = true
			}
			if got := h.request(testBillingProfile()); got != 503 {
				t.Fatalf("uncertain error: %d", got)
			}
			status, err := h.e.Console.Billing.Status(h.e.Ctx, h.org, h.user)
			if err != nil || status.ProfileEditable {
				t.Fatal("unproven failure unlocked profile", err)
			}
			other := testBillingProfile()
			other.PostalCode = "702 10"
			if got := h.request(other); got != 409 {
				t.Fatalf("unknown error allowed rebind: %d", got)
			}
			if h.f.sessionCreates != 0 {
				t.Fatal("failed customer created checkout")
			}
		})
	}
}

func TestStripeBillingAddressCreationSerializesAcrossReplicas(t *testing.T) {
	h := newBillingAddressHarness(t)
	replica := billing.New(h.e.Console.Billing.C, h.e.APIPool, false)
	if err := replica.Validate(h.e.Ctx); err != nil {
		t.Fatal(err)
	}
	h.f.blockedPath = "/customers"
	h.f.portalEntered = make(chan struct{})
	h.f.portalContinue = make(chan struct{})
	h.f.rejectCustomer = true
	finished := make(chan int, 1)
	go func() { finished <- h.request(testBillingProfile()) }()
	select {
	case <-h.f.portalEntered:
	case code := <-finished:
		t.Fatalf("first request exited before Customer create: %d", code)
	case <-time.After(3 * time.Second):
		t.Fatal("first Customer create did not start")
	}
	profile := testBillingProfile()
	_, err := replica.Checkout(h.e.Ctx, h.org, h.user, h.project, uuid.New(), "flex", "sek", &profile)
	var public *apierr.Error
	rejected := errors.As(err, &public) && public.Status == 409
	close(h.f.portalContinue)
	if code := <-finished; code != 422 {
		t.Fatalf("first rejection: %d", code)
	}
	if !rejected || h.f.customerCreates != 1 {
		t.Fatalf("replica overlapped creation: %v calls=%d", err, h.f.customerCreates)
	}
	status, err := replica.Status(h.e.Ctx, h.org, h.user)
	if err != nil || !status.ProfileEditable {
		t.Fatal("exclusive rejection was not editable", err)
	}
}

// An allowed international company must receive exactly one allowance from its
// canonical paid invoice, even when the card address is in another country.
func TestStripeInternationalBillingAndZeroTax(t *testing.T) {
	for _, country := range []string{"DE", "US", "GB", "HK"} {
		t.Run(country, func(t *testing.T) {
			h := newBillingAddressHarness(t)
			h.e.Console.Billing.C.Countries[country] = true
			h.f.zeroTax = country != "DE"
			p := testBillingProfile()
			p.Country = country
			if country == "US" {
				p.State, p.PostalCode = "CA", "94103"
			}
			if country == "HK" {
				p.PostalCode = ""
			}
			if got := h.request(p); got != 200 {
				t.Fatalf("international checkout: %d", got)
			}
			// Market closure blocks new purchases, not an already issued paid order.
			delete(h.e.Console.Billing.C.Countries, country)
			if got := h.request(p); got != 422 {
				t.Fatalf("closed market accepted new checkout: %d", got)
			}
			h.f.paid, h.f.status, h.f.invoiceCountry = true, "complete", country
			if err := h.worker.RunOnce(h.e.Ctx); err != nil {
				t.Fatal(err)
			}
			if h.e.Count(`SELECT count(*) FROM credit_lots WHERE granted=10000`) != 1 || h.e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 1 {
				t.Fatal("paid international invoice did not grant exactly one allowance")
			}
			if err := h.worker.RunOnce(h.e.Ctx); err != nil {
				t.Fatal(err)
			}
			if h.e.Count(`SELECT count(*) FROM credit_lots`) != 1 {
				t.Fatal("duplicate allowance")
			}
		})
	}
}

func TestStripeNorthAmericanAddressRequiresRegion(t *testing.T) {
	h := newBillingAddressHarness(t)
	for _, country := range []string{"US", "CA"} {
		h.e.Console.Billing.C.Countries[country] = true
		p := testBillingProfile()
		p.Country = country
		if got := h.request(p); got != 422 {
			t.Fatalf("missing region accepted: %d", got)
		}
		p.State, p.PostalCode = "CA", ""
		if got := h.request(p); got != 422 {
			t.Fatalf("missing postal code accepted: %d", got)
		}
	}
	if h.f.customerCreates != 0 || h.f.sessionCreates != 0 {
		t.Fatal("invalid address reached Stripe")
	}
}

func TestStripeLegacyRequestBodiesSurviveRollingRelease(t *testing.T) {
	h := newBillingAddressHarness(t)
	p := testBillingProfile()
	// An old API writer omits the new field; its pending create must stay v1.
	h.e.Exec(`INSERT INTO billing_customers(org_id,currency,billing_profile) VALUES($1,'sek',$2)`, h.org, p)
	h.f.legacyCustomer, h.f.lostCustomerResponses = true, 1
	if got := h.request(p); got != 503 {
		t.Fatalf("lost legacy customer response: %d", got)
	}
	if got := h.request(p); got != 200 {
		t.Fatalf("legacy customer retry: %d", got)
	}
	if len(h.f.customerForms) != 2 || h.f.customerForms[0] != h.f.customerForms[1] || h.f.customerKeys[0] != h.f.customerKeys[1] {
		t.Fatal("legacy customer changed idempotent request")
	}
	if h.e.Count(`SELECT count(*) FROM billing_customers WHERE request_version=1`) != 1 {
		t.Fatal("legacy version upgraded")
	}
	if h.e.Count(`SELECT count(*) FROM billing_orders WHERE request_version=2`) != 1 {
		t.Fatal("new writer did not select v2")
	}
	if _, err := h.e.WorkerPool.Exec(h.e.Ctx, `UPDATE billing_customers SET request_version=2`); err == nil {
		t.Fatal("customer version was mutable")
	}
}

func TestStripeLegacyCheckoutRetriesUnchanged(t *testing.T) {
	h := newBillingAddressHarness(t)
	p := testBillingProfile()
	// A confirmed legacy customer, then a pending v1 order from an older API.
	h.e.Exec(`INSERT INTO billing_customers(org_id,currency,billing_profile,customer_id) VALUES($1,'sek',$2,'cus_hedvi')`, h.org, p)
	var id uuid.UUID
	if err := h.e.WorkerPool.QueryRow(h.e.Ctx, `SELECT id FROM billing_customers WHERE org_id=$1`, h.org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	h.f.customerRef = id.String()
	h.f.customerAddress = billingJSON{"line1": p.Line1, "line2": p.Line2, "city": p.City, "state": p.State, "postal_code": p.PostalCode, "country": p.Country}
	h.e.Exec(`INSERT INTO billing_orders(org_id,request_id,sku,currency,country,price_id,amount,return_project_id) VALUES($1,$2,'flex','sek','SE','price_hedvi',3900,$3)`, h.org, uuid.New(), h.project)
	h.f.legacyCheckout, h.f.lostSessionResponses = true, 1
	if got := h.request(p); got != 503 {
		t.Fatalf("lost legacy session response: %d", got)
	}
	if got := h.request(p); got != 200 {
		t.Fatalf("legacy checkout retry: %d", got)
	}
	if len(h.f.checkoutForms) != 2 || h.f.checkoutForms[0] != h.f.checkoutForms[1] {
		t.Fatal("legacy checkout changed idempotent request")
	}
	if _, err := h.e.WorkerPool.Exec(h.e.Ctx, `UPDATE billing_orders SET request_version=2`); err == nil {
		t.Fatal("order version was mutable")
	}
}
