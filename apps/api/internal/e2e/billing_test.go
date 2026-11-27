package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v87/webhook"
	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/billing"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/testenv"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type billingJSON = map[string]any
type stripeFixture struct {
	blockedPath                                                              string
	missingConsent                                                           bool
	currency                                                                 string
	portalEntered, portalContinue                                            chan struct{}
	wrongItem                                                                bool
	mu                                                                       sync.Mutex
	sku, price, order, customerRef, session, status, invoiceID, sub, dispute string
	amount, refunded                                                         int64
	paid, offStripe, conflict, reduced                                       bool
	start, end                                                               int64
}

func (f *stripeFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Host != "api.stripe.com" || r.Header.Get("Stripe-Version") != billing.APIVersion {
		return nil, fmt.Errorf("unexpected Stripe destination")
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1")
	if path == f.blockedPath && f.portalEntered != nil {
		close(f.portalEntered)
		select {
		case <-f.portalContinue:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	tax := int64(97)
	total := f.amount + tax
	if f.reduced {
		total = f.amount - 1
	}
	recurring := f.sku != "flex"
	items := func(data ...any) billingJSON {
		if data == nil {
			data = []any{}
		}
		return billingJSON{"data": data, "has_more": false}
	}
	cs := func() billingJSON {
		mode := "payment"
		if recurring {
			mode = "subscription"
		}
		consent := "accepted"
		if f.missingConsent {
			consent = ""
		}
		return billingJSON{"id": f.session, "consent": billingJSON{"terms_of_service": consent}, "consent_collection": billingJSON{"terms_of_service": "required"}, "mode": mode, "status": f.status, "payment_status": map[bool]string{true: "paid", false: "unpaid"}[f.paid], "customer": "cus_hedvi", "metadata": billingJSON{"hedvi_order": f.order}, "client_reference_id": f.order, "payment_intent": "pi_hedvi", "amount_subtotal": f.amount, "amount_total": total, "currency": f.currency, "automatic_tax": billingJSON{"enabled": true, "status": "complete"}, "customer_details": billingJSON{"address": billingJSON{"country": "SE"}}, "url": "https://checkout.stripe.com/c/pay/mock", "invoice": f.invoiceID, "subscription": f.sub}
	}
	var result any
	switch {
	case path == "/account":
		result = billingJSON{"id": "acct_hedvi"}
	case path == "/prices/"+f.price:
		typ := "one_time"
		p := billingJSON{"id": f.price, "product": billingJSON{"id": "prod_hedvi", "tax_code": "txcd_10103001", "active": true}, "unit_amount": f.amount, "unit_amount_decimal": fmt.Sprint(f.amount), "currency": f.currency, "active": true, "billing_scheme": "per_unit", "tax_behavior": "exclusive"}
		if recurring {
			typ = "recurring"
			p["recurring"] = billingJSON{"interval": "month", "interval_count": 1, "usage_type": "licensed"}
		}
		p["type"] = typ
		result = p
	case path == "/billing_portal/configurations/bpc_hedvi":
		result = billingJSON{"id": "bpc_hedvi", "active": true, "features": billingJSON{"customer_update": billingJSON{"enabled": false}, "invoice_history": billingJSON{"enabled": true}, "payment_method_update": billingJSON{"enabled": true}, "subscription_update": billingJSON{"enabled": false}, "subscription_cancel": billingJSON{"enabled": true, "mode": "at_period_end", "proration_behavior": "none"}}}
	case path == "/billing_portal/sessions":

		result = billingJSON{"id": "bps_hedvi", "customer": "cus_hedvi", "configuration": "bpc_hedvi", "url": "https://billing.stripe.com/p/session/mock"}
	case path == "/customers" && r.Method == "POST":
		_ = r.ParseForm()
		f.customerRef = r.Form.Get("metadata[hedvi_customer]")
		result = billingJSON{"id": "cus_hedvi"}
	case path == "/customers/cus_hedvi":
		result = billingJSON{"id": "cus_hedvi", "metadata": billingJSON{"hedvi_customer": f.customerRef}}
	case path == "/checkout/sessions" && r.Method == "POST":
		_ = r.ParseForm()
		f.order = r.Form.Get("client_reference_id")
		if r.Form.Get("metadata[hedvi_order]") != f.order || r.Form.Get("line_items[0][price]") != f.price || r.Form.Get("automatic_tax[enabled]") != "true" || r.Form.Get("consent_collection[terms_of_service]") != "required" {
			return nil, fmt.Errorf("invalid checkout")
		}
		result = cs()
	case path == "/checkout/sessions":
		result = items(cs())
	case path == "/checkout/sessions/"+f.session:
		result = cs()
	case path == "/checkout/sessions/"+f.session+"/line_items":
		result = items(billingJSON{"price": f.price, "quantity": 1})
	case path == "/subscriptions/"+f.sub:
		result = billingJSON{"id": f.sub, "customer": "cus_hedvi", "status": "active", "items": items(billingJSON{"id": "si_hedvi", "price": f.price, "quantity": 1}), "metadata": billingJSON{"hedvi_order": f.order}}
	case path == "/invoices":
		result = items(billingJSON{"id": f.invoiceID, "status": "paid"})
	case path == "/invoices/"+f.invoiceID:
		customer := "cus_hedvi"
		if f.conflict {
			customer = "cus_unrelated"
		}
		inv := billingJSON{"id": f.invoiceID, "customer": customer, "currency": f.currency, "status": "paid", "subtotal": f.amount, "subtotal_excluding_tax": f.amount, "total_excluding_tax": f.amount, "amount_due": total, "total_taxes": []any{billingJSON{"tax_behavior": "exclusive", "amount": tax}}, "total": total, "amount_paid": total, "amount_remaining": 0, "collection_method": "charge_automatically", "customer_address": billingJSON{"country": "SE"}, "automatic_tax": billingJSON{"enabled": true, "status": "complete"}, "status_transitions": billingJSON{"paid_at": f.start}}
		if f.offStripe {
			inv["amount_paid_off_stripe"] = total
		}
		if recurring {
			inv["billing_reason"] = "subscription_cycle"
			inv["parent"] = billingJSON{"subscription_details": billingJSON{"subscription": f.sub, "metadata": billingJSON{"hedvi_order": f.order}}}
		}
		result = inv
	case path == "/invoices/"+f.invoiceID+"/lines":
		l := billingJSON{"id": "il_hedvi", "amount": f.amount, "pricing": billingJSON{"price_details": billingJSON{"price": f.price}}, "quantity": 1, "currency": f.currency, "period": billingJSON{"start": f.start, "end": f.end}}
		if recurring {
			item := "si_hedvi"
			if f.wrongItem {
				item = "si_wrong"
			}
			l["parent"] = billingJSON{"subscription_item_details": billingJSON{"subscription": f.sub, "subscription_item": item, "proration": false}}
		}
		result = items(l)
	case path == "/invoice_payments":
		result = items(billingJSON{"id": "inpay_hedvi", "status": "paid", "invoice": f.invoiceID, "amount_paid": total, "currency": f.currency, "payment": billingJSON{"type": "payment_intent", "payment_intent": "pi_hedvi"}})
	case path == "/payment_intents/pi_hedvi":
		result = billingJSON{"id": "pi_hedvi", "status": "succeeded", "customer": "cus_hedvi", "amount_received": total, "currency": f.currency, "latest_charge": "ch_hedvi"}
	case path == "/charges/ch_hedvi":
		result = billingJSON{"id": "ch_hedvi", "customer": "cus_hedvi", "payment_intent": "pi_hedvi", "paid": true, "captured": true, "amount_captured": total, "currency": f.currency, "amount_refunded": f.refunded, "refunded": f.refunded == total, "disputed": f.dispute != "", "payment_method_details": billingJSON{"type": "card"}}
	case path == "/refunds":
		result = items()
	case path == "/disputes/dp_hedvi":
		result = billingJSON{"id": "dp_hedvi", "charge": "ch_hedvi", "status": f.dispute}
	case path == "/disputes":
		result = items(billingJSON{"id": "dp_hedvi", "charge": "ch_hedvi", "status": f.dispute})
	default:
		return nil, fmt.Errorf("unhandled fixture %s", path)
	}
	raw, _ := json.Marshal(result)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: make(http.Header)}, nil
}
func TestStripeOrdersGrantOnlyCanonicalEconomicSources(t *testing.T) {
	for _, product := range []struct {
		sku, currency    string
		amount, included int64
	}{
		{"flex", "sek", 3900, 0}, {"flex", "usd", 400, 0}, {"pro_50k", "sek", 12900, 50000}, {"pro_50k", "usd", 1300, 50000}, {"pro_100k", "sek", 24900, 100000}, {"pro_100k", "usd", 2500, 100000},
	} {
		sku := product.sku
		t.Run(sku+"_"+product.currency, func(t *testing.T) {
			e := testenv.New(t)
			cookie, user, org, project := signupForSecurity(t, e)
			now := time.Now().UTC()
			start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
			if product.currency == "usd" {
				start = now.AddDate(0, 0, -8).Truncate(time.Second)
			}
			f := &stripeFixture{sku: sku, currency: product.currency, amount: product.amount, price: "price_hedvi", session: "cs_test_hedvi", invoiceID: "in_hedvi", status: "open", start: start.Unix(), end: start.AddDate(0, 1, 0).Unix()}
			if sku != "flex" {
				f.sub = "sub_hedvi"
			}
			original := http.DefaultTransport
			http.DefaultTransport = f
			t.Cleanup(func() { http.DefaultTransport = original })
			c := billing.Config{Portal: "bpc_hedvi", Account: "acct_hedvi", AppURL: "https://app.example.test", Secret: "unit-signing-credential", Countries: map[string]bool{"SE": true}, Prices: map[string]billing.Price{sku + "_" + product.currency: {SKU: sku, Currency: product.currency, ID: f.price, Product: "prod_hedvi", Amount: f.amount}}}
			work := billing.New(c, e.WorkerPool, true)
			if err := work.Validate(e.Ctx); err != nil {
				t.Fatal(err)
			}
			srv := billing.New(c, e.APIPool, false)
			if err := srv.Validate(e.Ctx); err != nil {
				t.Fatal(err)
			}
			e.Console.Billing = srv
			path := "/console/v1/projects/" + project.String() + "/billing"
			token := uuid.New()
			req := map[string]any{"sku": sku, "currency": product.currency, "country": "SE", "request_id": token}
			usage := func(at time.Time) quota.Usage {
				var u quota.Usage
				err := db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error {
					var err error
					u, err = quota.LoadUsage(e.Ctx, tx, quota.Org{ID: org, Plan: "free"}, at)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				return u
			}
			if usage(now).Included != 5000 {
				t.Fatal("initial allowance")
			}
			if _, err := e.WorkerPool.Exec(e.Ctx, `UPDATE billing_installation SET live=true`); err == nil {
				t.Fatal("processor can rebind merchant")
			}
			if err := db.InOrg(e.Ctx, e.APIPool, org, func(tx pgx.Tx) error {
				_, err := tx.Exec(e.Ctx, `INSERT INTO credit_ledger(org_id,kind,source,quantity) VALUES($1,'grant','credit',500000)`, org)
				return err
			}); err == nil {
				t.Fatal("API forged ledger grant")
			}
			if got := cookieRequest(e.Handler, cookie, "POST", path+"/checkout", req); got != 200 {
				t.Fatalf("checkout %d", got)
			}
			if got := cookieRequest(e.Handler, cookie, "POST", path+"/checkout", req); got != 200 || e.Count(`SELECT count(*) FROM billing_orders`) != 1 {
				t.Fatal("checkout idempotency", got)
			}
			if sku == "flex" && product.currency == "sek" {
				for _, action := range []string{"checkout", "portal"} {
					sessionToken := uuid.NewString()
					testCookie := &http.Cookie{Name: cookie.Name, Value: sessionToken}
					tokenHash := e.Hasher.Sum("session", sessionToken)
					e.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`, user, tokenHash)
					f.blockedPath = "/checkout/sessions/" + f.session
					var body any = req
					if action == "portal" {
						f.blockedPath = "/billing_portal/sessions"
						body = struct{}{}
					}
					f.portalEntered = make(chan struct{})
					f.portalContinue = make(chan struct{})
					completed := make(chan int, 1)
					go func() { completed <- cookieRequest(e.Handler, testCookie, "POST", path+"/"+action, body) }()
					select {
					case <-f.portalEntered:
					case code := <-completed:
						t.Fatalf("%s exited early %d", action, code)
					case <-time.After(3 * time.Second):
						t.Fatal("Stripe call did not begin")
					}
					e.Exec(`UPDATE sessions SET revoked_at=now() WHERE token_hash=$1`, tokenHash)
					close(f.portalContinue)
					if code := <-completed; code != 401 {
						t.Fatalf("%s returned a bearer URL to a revoked session: %d", action, code)
					}
					f.blockedPath = ""
				}
			}
			if e.Count(`SELECT count(*) FROM credit_lots`)+e.Count(`SELECT count(*) FROM usage_periods WHERE stripe_invoice IS NOT NULL`) != 0 {
				t.Fatal("checkout created allowance")
			}
			other := e.NewTenant("free")
			if _, err := srv.Status(context.Background(), other.OrgID, user); err == nil {
				t.Fatal("foreign organization billing exposed")
			}
			f.status = "complete"
			f.paid = true
			f.offStripe = true
			f.missingConsent = true
			notify := func(id, kind, obj string, valid bool) int {
				body, _ := json.Marshal(billingJSON{"id": id, "type": kind, "api_version": billing.APIVersion, "livemode": false, "data": billingJSON{"object": billingJSON{"id": obj, "customer": "cus_hedvi", "charge": "ch_hedvi", "metadata": billingJSON{"hedvi_order": f.order}}}})
				req := httptest.NewRequest("POST", "/billing/stripe/webhook", bytes.NewReader(body))
				key := c.Secret
				if !valid {
					key = "invalid"
				}
				sig := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: body, Secret: key})
				req.Header.Set("Stripe-Signature", sig.Header)
				w := httptest.NewRecorder()
				srv.Webhook(w, req)
				return w.Code
			}
			if notify("evt_wrong", "checkout.session.completed", f.session, false) != 400 || e.Count(`SELECT count(*) FROM billing_events`) != 0 {
				t.Fatal("unsigned event accepted")
			}
			if notify("evt_dup", "checkout.session.completed", f.session, true) != 204 || notify("evt_dup", "checkout.session.completed", f.session, true) != 204 || e.Count(`SELECT count(*) FROM billing_events`) != 1 {
				t.Fatal("signed event deduplication")
			}
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_orders WHERE state='review' AND checkout_consent_observed_at IS NULL`) != 1 || e.Count(`SELECT count(*) FROM billing_invoices`) != 0 {
				t.Fatal("unconfirmed checkout terms allowed invoice discovery")
			}
			f.missingConsent = false
			notify("evt_consent", "checkout.session.completed", f.session, true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='review'`) != 1 || usage(now).Included != 5000 {
				t.Fatal("out-of-band invoice granted service")
			}
			f.offStripe = false
			f.conflict = true
			notify("evt_mismatch", "invoice.paid", f.invoiceID, true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 0 {
				t.Fatal("foreign invoice granted service")
			}
			f.conflict = false
			f.reduced = true
			notify("evt_reduced", "invoice.paid", f.invoiceID, true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 0 {
				t.Fatal("reduced total granted full service")
			}
			f.reduced = false
			if sku != "flex" {
				f.wrongItem = true
				notify("evt_wrong_item", "invoice.paid", f.invoiceID, true)
				if err := work.RunOnce(e.Ctx); err != nil {
					t.Fatal(err)
				}
				if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 0 {
					t.Fatal("foreign subscription item granted allowance")
				}
				f.wrongItem = false
			}
			if notify("evt_early", "charge.dispute.created", "dp_hedvi", true) != 204 {
				t.Fatal("early dispute")
			}
			notify("evt_early_invoice", "invoice.paid", f.invoiceID, true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 0 {
				t.Fatal("unknown early dispute granted funds")
			}
			f.dispute = "won"
			notify("evt_paid", "invoice.paid", f.invoiceID, true)
			var wg sync.WaitGroup
			errs := make(chan error, 3)
			for range 3 {
				wg.Go(func() { errs <- work.RunOnce(e.Ctx) })
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='granted'`) != 1 || e.Count(`SELECT count(*) FROM credit_ledger WHERE kind='grant'`) != 1 {
				t.Fatal("payment not granted exactly once")
			}
			if e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='test_only'`, project) != 1 {
				t.Fatal("payment changed sending permission")
			}
			if sku == "flex" {
				if e.Count(`SELECT count(*) FROM credit_lots WHERE granted=10000 AND data_granted=262144000 AND expires_at-purchased_at=interval '365 days'`) != 1 || len(usage(now).Credits) != 1 {
					t.Fatal("Flex lot or expiry")
				}
			} else {
				if u := usage(now); u.Included != product.included || !u.PeriodStart.Equal(start) || !u.PeriodEnd.Equal(start.AddDate(0, 1, 0)) {
					t.Fatalf("wrong paid allowance %+v", u)
				}
				if u := usage(start.AddDate(0, 1, 0)); u.Included != 5000 {
					t.Fatal("unpaid renewal created quota", u.Included)
				}
			}
			// Acceptance and settlement share their original lot/period even across a reversal.
			e.Exec(`UPDATE projects SET sending_status='approved' WHERE id=$1`, project)
			e.Exec(`INSERT INTO domains(org_id,project_id,name,status,verified_at) VALUES($1,$2,'billing-mail.test','verified',now())`, org, project)
			principal := accept.Principal{OrgID: org, ProjectID: project, Mode: "live", Permission: "full_access"}
			sent, err := e.Accept.Send(e.Ctx, principal, "", liveBody(&testenv.Tenant{Domain: "billing-mail.test"}, "first@example.com", "second@example.com"))
			if err != nil {
				t.Fatal(err)
			}
			rows, err := e.WorkerPool.Query(e.Ctx, `SELECT id FROM recipients WHERE email_id=$1 ORDER BY id`, sent.EmailID)
			if err != nil {
				t.Fatal(err)
			}
			recipients, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
			if err != nil || len(recipients) != 2 {
				t.Fatal("reserved recipients", err)
			}
			f.dispute = "needs_response"
			if notify("evt_dispute", "charge.dispute.created", "dp_hedvi", true) != 204 {
				t.Fatal("dispute ingress failed")
			}
			if n := e.Count(`SELECT count(*) FROM usage_periods WHERE stripe_invoice IS NOT NULL AND billing_usable`) + e.Count(`SELECT count(*) FROM credit_lots WHERE stripe_invoice IS NOT NULL AND billing_usable`); n != 0 {
				t.Fatal("confirmed reversal remained spendable before worker ran")
			}
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='held'`) != 1 {
				t.Fatal("dispute did not hold source")
			}
			f.dispute = "won"
			notify("evt_won", "charge.dispute.closed", "dp_hedvi", true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			f.refunded = f.amount + 97
			notify("evt_refund", "charge.refunded", "ch_hedvi", true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			notify("evt_old_paid", "invoice.paid", f.invoiceID, true)
			if err := work.RunOnce(e.Ctx); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM billing_invoices WHERE state='revoked'`) != 1 || e.Count(`SELECT count(*) FROM credit_ledger WHERE kind='grant'`) != 1 {
				t.Fatal("duplicate grant or lost refund")
			}
			if n := e.Count(`SELECT count(*) FROM usage_periods WHERE stripe_invoice IS NOT NULL AND billing_usable`) + e.Count(`SELECT count(*) FROM credit_lots WHERE stripe_invoice IS NOT NULL AND billing_usable`); n != 0 {
				t.Fatal("revoked balances remain spendable")
			}
			if err := db.Tx(e.Ctx, e.WorkerPool, func(tx pgx.Tx) error {
				if n, err := quota.Consume(e.Ctx, tx, org, recipients[:1]); err != nil || n != 1 {
					return fmt.Errorf("consume %d: %v", n, err)
				}
				if n, err := quota.Release(e.Ctx, tx, org, recipients[1:]); err != nil || n != 1 {
					return fmt.Errorf("release %d: %v", n, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if e.Count(`SELECT count(*) FROM credit_lots WHERE stripe_invoice=$1 AND consumed=1 AND reserved=0 AND NOT billing_usable`, f.invoiceID)+e.Count(`SELECT count(*) FROM usage_periods WHERE stripe_invoice=$1 AND consumed=1 AND reserved=0 AND NOT billing_usable`, f.invoiceID) != 1 {
				t.Fatal("existing reservation settled in a different source")
			}
			if sku == "flex" {
				if code := cookieRequest(e.Handler, cookie, "POST", path+"/baseline", map[string]string{"plan": "free"}); code != 200 || usage(now).Included != 5000 {
					t.Fatal("Free baseline unavailable after refund", code)
				}
				if code := cookieRequest(e.Handler, cookie, "POST", path+"/baseline", map[string]string{"plan": "flex"}); code != 409 {
					t.Fatal("held pack authorized Flex baseline", code)
				}
			}
			if status, err := srv.Status(e.Ctx, org, user); err != nil || len(status.Orders) != 1 || status.Orders[0].State != "reversed" {
				t.Errorf("customer-visible financial state: %+v %v", status, err)
			}
			if code := cookieRequest(e.Handler, cookie, "GET", path+"/orders/"+f.order, nil); code != 200 {
				t.Fatal("payment return failed to load", code)
			}
			f.blockedPath = "/billing_portal/sessions"
			finished := make(chan int, 1)
			f.portalEntered = make(chan struct{})
			f.portalContinue = make(chan struct{})
			go func() { finished <- cookieRequest(e.Handler, cookie, "POST", path+"/portal", struct{}{}) }()
			select {
			case <-f.portalEntered:
			case code := <-finished:
				t.Fatalf("portal failed before revalidation %d", code)
			case <-time.After(3 * time.Second):
				t.Fatal("portal did not start")
			}
			e.Exec(`UPDATE memberships SET role='developer' WHERE org_id=$1 AND user_id=$2`, org, user)
			close(f.portalContinue)
			if code := <-finished; code != 404 {
				t.Fatal("portal returned after billing access revoked", code)
			}

		})
	}
}

func TestStripeActionBudgetSurvivesConcurrentReplicas(t *testing.T) {
	e := testenv.New(t)
	_, user, org, _ := signupForSecurity(t, e)
	accepted := make(chan bool, 24)
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			ok := false
			err := db.InUser(e.Ctx, e.APIPool, user, &org, func(tx pgx.Tx) error {
				if err := tx.QueryRow(e.Ctx, `SELECT hedvi_admit_billing_action($1)`, org).Scan(&ok); err != nil {
					return err
				}
				if !ok {
					return pgx.ErrNoRows
				}
				return nil
			})
			if err != nil && err != pgx.ErrNoRows {
				t.Error(err)
			}
			accepted <- ok
		})
	}
	wg.Wait()
	close(accepted)
	n := 0
	for ok := range accepted {
		if ok {
			n++
		}
	}
	if n != 8 {
		t.Fatalf("admitted %d, want 8", n)
	}
}
