package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
	"hedvi.dev/api/internal/db"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var eventKinds = map[string]bool{
	"checkout.session.completed": true, "checkout.session.expired": true, "checkout.session.async_payment_succeeded": true, "checkout.session.async_payment_failed": true,
	"customer.subscription.created": true, "customer.subscription.updated": true, "customer.subscription.deleted": true,
	"invoice.paid": true, "invoice.payment_failed": true, "invoice.payment_action_required": true, "invoice.finalization_failed": true, "invoice.voided": true, "invoice.marked_uncollectible": true,
	"charge.refunded": true, "refund.created": true, "refund.updated": true, "refund.failed": true, "charge.dispute.created": true, "charge.dispute.updated": true, "charge.dispute.closed": true,
	"credit_note.created": true, "credit_note.updated": true, "credit_note.voided": true,
}

func (s *Service) Webhook(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 512<<10))
	if err != nil {
		http.Error(w, "invalid event", 400)
		return
	}
	header := r.Header.Get("Stripe-Signature")
	if webhook.ValidatePayload(b, header, s.C.Secret) != nil && (s.C.PreviousSecret == "" || webhook.ValidatePayload(b, header, s.C.PreviousSecret) != nil) {
		http.Error(w, "invalid signature", 400)
		return
	}
	var evt struct {
		ID, Type, Account, Context string
		APIVersion                 string `json:"api_version"`
		Livemode                   bool
		Data                       struct{ Object json.RawMessage }
	}
	if json.Unmarshal(b, &evt) != nil || !stripeID.MatchString(evt.ID) || !strings.HasPrefix(evt.ID, "evt_") || evt.Livemode != s.C.Live || evt.Context != "" || evt.Account != "" && evt.Account != s.C.Account || evt.APIVersion != APIVersion {
		http.Error(w, "unexpected event", 400)
		return
	}
	if !eventKinds[evt.Type] {
		w.WriteHeader(204)
		return
	}
	var o struct {
		ID, Customer, Charge, Invoice, Subscription string
		Metadata                                    map[string]string
		Parent                                      struct {
			SubscriptionDetails struct{ Subscription string } `json:"subscription_details"`
		}
	}
	if json.Unmarshal(evt.Data.Object, &o) != nil || !stripeID.MatchString(o.ID) {
		http.Error(w, "invalid object", 400)
		return
	}
	if o.Subscription == "" {
		o.Subscription = o.Parent.SubscriptionDetails.Subscription
	}
	if strings.HasPrefix(evt.Type, "invoice.") {
		o.Invoice = o.ID
	}
	if strings.HasPrefix(evt.Type, "customer.subscription.") {
		o.Subscription = o.ID
	}
	if evt.Type == "charge.refunded" {
		o.Charge = o.ID
	}
	if err := s.bound(r.Context()); err != nil {
		http.Error(w, "billing temporarily unavailable", 503)
		return
	}
	_, err = s.Pool.Exec(r.Context(), `INSERT INTO billing_events(id,kind,object_id,customer_id,charge_id,invoice_id,subscription_id,order_ref) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, evt.ID, evt.Type, o.ID, o.Customer, o.Charge, o.Invoice, o.Subscription, o.Metadata["hedvi_order"])
	if err != nil {
		http.Error(w, "billing temporarily unavailable", 503)
		return
	}
	w.WriteHeader(204)
}

func (s *Service) wake(ctx context.Context) error {
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,kind,object_id,customer_id,charge_id,invoice_id,subscription_id,order_ref FROM billing_events WHERE processed_at IS NULL ORDER BY received_at LIMIT 50 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		batch, err := pgx.CollectRows(rows, pgx.RowToStructByPos[refreshRow])
		if err != nil {
			return err
		}
		for _, e := range batch {

			if strings.HasPrefix(e.Kind, "invoice.") && stripeID.MatchString(e.Sub) && e.Customer != "" {
				if _, err = tx.Exec(ctx, `INSERT INTO billing_invoices(id,org_id,order_id) SELECT $1,o.org_id,o.id FROM billing_orders o JOIN billing_customers c ON c.org_id=o.org_id WHERE o.subscription_id=$2 AND c.customer_id=$3 ON CONFLICT DO NOTHING`, e.Invoice, e.Sub, e.Customer); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `UPDATE billing_invoices SET next_check_at=now(),lease=NULL,lease_until=NULL WHERE id=$1 OR charge_id=$2`, e.Invoice, e.Charge); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE billing_orders o SET next_check_at=now(),lease=NULL,lease_until=NULL FROM billing_customers c WHERE c.org_id=o.org_id AND c.customer_id=$1 AND (o.id::text=$2 OR o.session_id=$3 OR o.subscription_id=$4 OR (o.sku<>'flex' AND o.state IN ('creating','open') AND $4<>''))`, e.Customer, e.Order, e.Object, e.Sub); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE billing_events SET processed_at=now() WHERE id=$1`, e.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

type refreshRow struct{ ID, Kind, Object, Customer, Charge, Invoice, Sub, Order string }
type job struct {
	id      string
	lease   uuid.UUID
	invoice bool
}

func (s *Service) claim(ctx context.Context, invoice bool) (job, error) {
	j := job{lease: uuid.New(), invoice: invoice}
	table := "billing_orders"
	if invoice {
		table = "billing_invoices"
	}
	err := s.Pool.QueryRow(ctx, `UPDATE `+table+` SET lease=$1,lease_until=now()+interval '3 minutes' WHERE id=(SELECT id FROM `+table+` WHERE next_check_at<=now() AND (lease IS NULL OR lease_until<=now()) ORDER BY next_check_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id::text`, j.lease).Scan(&j.id)
	return j, err
}

// Each worker claim has a fencing token. An event arriving during Stripe I/O
// invalidates the snapshot, leaving the resource ready for a fresh check.
func (s *Service) RunOnce(ctx context.Context) error {
	if err := s.Validate(ctx); err != nil {
		return err
	}
	if err := s.wake(ctx); err != nil {
		return err
	}
	var failures []error
	for range 8 {
		progress := false
		for _, invoice := range []bool{false, true} {
			j, err := s.claim(ctx, invoice)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			progress = true
			c, cancel := context.WithTimeout(ctx, 90*time.Second)
			if invoice {
				err = s.checkInvoice(c, j)
			} else {
				err = s.checkOrder(c, j)
			}
			cancel()
			if err != nil {
				table := "billing_orders"
				if invoice {
					table = "billing_invoices"
				}
				_, cleanupErr := s.Pool.Exec(ctx, `WITH delayed AS (UPDATE `+table+` SET lease=NULL,lease_until=NULL,check_failures=least(check_failures+1,60),next_check_at=now()+least(check_failures+1,60)*interval '1 minute' WHERE id=$1 AND lease=$2 RETURNING org_id,check_failures)
                    INSERT INTO alerts(org_id,kind,severity,dedupe_key,message) SELECT org_id,'billing_read_failed','warning',$3,'A billing resource could not be reconciled. An operator should check Stripe access and retry.' FROM delayed WHERE check_failures>=3 ON CONFLICT(dedupe_key) WHERE resolved_at IS NULL DO NOTHING`, j.id, j.lease, "billing-read:"+j.id)
				failures = append(failures, fmt.Errorf("billing resource %s: %w", j.id, errors.Join(err, cleanupErr)))
			}
		}
		if !progress {
			break
		}
	}
	return errors.Join(failures...)
}

type page[T any] struct {
	Data []T  `json:"data"`
	More bool `json:"has_more"`
}

// scanPages fails closed on an unexpectedly large response. Persisting invoice
// discoveries is idempotent; a bounded overlap on the next poll covers late events.
func scanPages[T any](ctx context.Context, s *Service, path string, v url.Values, id func(T) string, visit func(T) error) error {
	v.Set("limit", "100")
	for range 20 {
		var list page[T]
		if err := s.get(ctx, path, v, &list); err != nil {
			return err
		}
		for _, x := range list.Data {
			if err := visit(x); err != nil {
				return err
			}
		}
		if !list.More {
			return nil
		}
		if len(list.Data) == 0 || !stripeID.MatchString(id(list.Data[len(list.Data)-1])) {
			return errors.New("invalid Stripe pagination")
		}
		v.Set("starting_after", id(list.Data[len(list.Data)-1]))
	}
	return errors.New("Stripe reconciliation page limit reached")
}
func (s *Service) checkOrder(ctx context.Context, j job) error {
	var o Order
	var org, customerRef uuid.UUID
	var cursor, cancelAt *time.Time
	var subStatus string
	termsAccepted := false
	scanStarted := time.Now().UTC()
	err := s.Pool.QueryRow(ctx, `SELECT o.org_id,c.id,COALESCE(c.customer_id,''),o.id,o.sku,o.currency,o.country,o.price_id,o.amount,COALESCE(o.session_id,''),COALESCE(o.subscription_id,''),COALESCE(o.subscription_item_id,''),o.created_at,o.state,o.invoice_cursor,COALESCE(o.subscription_status,''),o.cancel_at FROM billing_orders o JOIN billing_customers c ON c.org_id=o.org_id WHERE o.id=$1`, j.id).Scan(&org, &customerRef, &o.Customer, &o.ID, &o.SKU, &o.Currency, &o.Country, &o.PriceID, &o.Amount, &o.Session, &o.Subscription, &o.Item, &o.Created, &o.State, &cursor, &subStatus, &cancelAt)
	if err != nil {
		return err
	}
	state := o.State
	delay := int64(120)
	invoices := []string{}
	if o.Customer == "" && time.Since(o.Created) > 23*time.Hour {
		var found page[stripe.Customer]
		if err = s.get(ctx, "/customers/search", url.Values{"query": {"metadata['hedvi_customer']:'" + customerRef.String() + "'"}, "limit": {"2"}}, &found); err != nil {
			return err
		}
		if len(found.Data) == 1 && !found.More && found.Data[0].Livemode == s.C.Live && found.Data[0].Metadata["hedvi_customer"] == customerRef.String() {
			o.Customer = found.Data[0].ID
		} else {
			state = "review"
			delay = 3600
		}
	}
	if o.Customer != "" {
		if err := s.verifyCustomer(ctx, o.Customer, customerRef, nil); err != nil {
			return err
		}
	}
	if o.Customer != "" && o.Session == "" {
		err = scanPages(ctx, s, "/checkout/sessions", url.Values{"customer": {o.Customer}, "created[gte]": {strconv.FormatInt(o.Created.Add(-time.Minute).Unix(), 10)}, "created[lte]": {strconv.FormatInt(o.Created.Add(24*time.Hour).Unix(), 10)}}, func(v stripe.CheckoutSession) string { return v.ID }, func(v stripe.CheckoutSession) error {
			if v.ClientReferenceID == o.ID.String() {
				if o.Session != "" && o.Session != v.ID {
					return errors.New("multiple Stripe sessions require review")
				}
				o.Session = v.ID
			}
			return nil
		})
		if err != nil {
			return err
		}
		if o.Session == "" && time.Since(o.Created) > 24*time.Hour {
			state = "expired"
		}
	}
	if o.Session != "" {
		var cs stripe.CheckoutSession
		if err = s.get(ctx, "/checkout/sessions/"+o.Session, nil, &cs); err != nil {
			return err
		}
		if cs.ID != o.Session || cs.Customer == nil || cs.Customer.ID != o.Customer || cs.ClientReferenceID != o.ID.String() || cs.Metadata["hedvi_order"] != o.ID.String() || cs.Livemode != s.C.Live || string(cs.Mode) != s.C.Prices[o.SKU+"_"+o.Currency].mode() {
			return errors.New("checkout ownership mismatch")
		}
		switch cs.Status {
		case "expired":
			state = "expired"
		case "open":
			state = "open"
		case "complete":
			var items page[stripe.LineItem]
			if err = s.get(ctx, "/checkout/sessions/"+cs.ID+"/line_items", url.Values{"limit": {"2"}}, &items); err != nil {
				return err
			}
			termsAccepted = cs.ConsentCollection != nil && cs.ConsentCollection.TermsOfService == "required" && cs.Consent != nil && cs.Consent.TermsOfService == "accepted"
			// The payment method address may legitimately differ from the locked
			// company address. inspectInvoice still requires the canonical invoice
			// country, complete automatic tax and the paid economic source.
			if !termsAccepted || cs.AutomaticTax == nil || !cs.AutomaticTax.Enabled || cs.AutomaticTax.Status != "complete" || string(cs.Currency) != o.Currency || cs.AmountSubtotal != o.Amount || len(items.Data) != 1 || items.More || items.Data[0].Price == nil || items.Data[0].Price.ID != o.PriceID || items.Data[0].Quantity != 1 {
				state = "review"
				delay = 3600
				break
			}
			if o.SKU == "flex" {
				if cs.PaymentStatus == "paid" && cs.Invoice != nil && cs.PaymentIntent != nil {
					invoices = append(invoices, cs.Invoice.ID)
					if state != "paid" {
						state = "open"
					}
					delay = 3600
				}
			} else if cs.Subscription != nil {
				if o.Subscription != "" && o.Subscription != cs.Subscription.ID {
					return errors.New("subscription binding conflict")
				}
				o.Subscription = cs.Subscription.ID
				var sub stripe.Subscription
				if err = s.get(ctx, "/subscriptions/"+o.Subscription, nil, &sub); err != nil {
					return err
				}
				if sub.ID != o.Subscription || sub.Customer == nil || sub.Customer.ID != o.Customer || sub.Metadata["hedvi_order"] != o.ID.String() || sub.Livemode != s.C.Live {
					return errors.New("subscription ownership mismatch")
				}
				subStatus = string(sub.Status)
				cancelAt = nil
				if sub.CancelAt > 0 {
					at := time.Unix(sub.CancelAt, 0).UTC()
					cancelAt = &at
				} else if sub.CancelAtPeriodEnd && sub.Items != nil && len(sub.Items.Data) > 0 && sub.Items.Data[0] != nil && sub.Items.Data[0].CurrentPeriodEnd > 0 {
					at := time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0).UTC()
					cancelAt = &at
				}
				if sub.Items == nil || sub.Items.HasMore || len(sub.Items.Data) != 1 || sub.Items.Data[0] == nil || sub.Items.Data[0].Price == nil || sub.Items.Data[0].Price.ID != o.PriceID || sub.Items.Data[0].Quantity != 1 || !stripeID.MatchString(sub.Items.Data[0].ID) || o.Item != "" && o.Item != sub.Items.Data[0].ID {
					state, delay = "review", 3600
					if sub.Status == "canceled" || sub.Status == "incomplete_expired" {
						state = "ended"
					}
					break
				}
				o.Item = sub.Items.Data[0].ID
				if sub.Status == "canceled" || sub.Status == "incomplete_expired" {
					state = "ended"
				} else if o.State != "active" {
					state = "open"
				}
				since := o.Created.Add(-time.Hour)
				if cursor != nil && cursor.Add(-48*time.Hour).After(since) {
					since = cursor.Add(-48 * time.Hour)
				}
				v := url.Values{"customer": {o.Customer}, "subscription": {o.Subscription}, "created[gte]": {strconv.FormatInt(since.Unix(), 10)}}
				err = scanPages(ctx, s, "/invoices", v, func(i stripe.Invoice) string { return i.ID }, func(i stripe.Invoice) error {
					if i.Status == "paid" || i.Status == "open" || i.Status == "draft" {
						invoices = append(invoices, i.ID)
					}
					return nil
				})
				if err != nil {
					return err
				}
				cursor = &scanStarted
				delay = 3600
			}
		default:
			return errors.New("unknown Stripe checkout state")
		}
	}
	if state == "expired" || state == "ended" {
		delay = 86400
	}
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, org); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE billing_orders SET session_id=COALESCE(session_id,NULLIF($3,'')),subscription_id=COALESCE(subscription_id,NULLIF($4,'')),subscription_item_id=COALESCE(subscription_item_id,NULLIF($7,'')),state=CASE WHEN $5='open' AND state IN ('active','paid') THEN state ELSE $5 END,checked_at=now(),check_failures=0,invoice_cursor=$8,subscription_status=NULLIF($9,''),cancel_at=$10,checkout_consent_observed_at=COALESCE(checkout_consent_observed_at,CASE WHEN $11 THEN now() END),next_check_at=CASE WHEN $5 IN ('expired','ended','paid') OR (state='paid' AND $5='open') THEN 'infinity'::timestamptz ELSE now()+$6*interval '1 second' END,lease=NULL,lease_until=NULL WHERE id=$1 AND lease=$2`, o.ID, j.lease, o.Session, o.Subscription, state, delay, o.Item, cursor, subStatus, cancelAt, termsAccepted)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE resolved_at IS NULL AND dedupe_key=$1`, "billing-read:"+j.id); err != nil {
			return err
		}
		if state == "review" {
			if _, err := tx.Exec(ctx, `INSERT INTO alerts(org_id,kind,severity,dedupe_key,message) VALUES($1,'billing_order_review','warning',$2,'A checkout needs review before service can be granted.') ON CONFLICT(dedupe_key) WHERE resolved_at IS NULL DO NOTHING`, org, "billing-order:"+j.id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, "billing-order:"+j.id); err != nil {
			return err
		}
		if o.Customer != "" {
			tag, err = tx.Exec(ctx, `UPDATE billing_customers SET customer_id=$2 WHERE org_id=$1 AND (customer_id IS NULL OR customer_id=$2)`, org, o.Customer)
			if err != nil || tag.RowsAffected() != 1 {
				return errors.New("customer reconciliation conflict")
			}
		}
		for _, id := range invoices {
			if _, err := tx.Exec(ctx, `INSERT INTO billing_invoices(id,org_id,order_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, org, o.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
