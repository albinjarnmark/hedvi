package billing

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v87"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
)

// Evidence contains only identifiers, money and service dates. Stripe names,
// addresses, tax IDs and payment method details never enter the delivery ledger.
type evidence struct {
	state, reason, payment, charge, line string
	total                                int64
	created                              *time.Time
	paid, start, end                     time.Time
}

// Only one catalog item and its exclusive tax may contribute to the captured total.
func catalogTotal(i *stripe.Invoice, net int64) bool {
	if i.Total < net || i.TotalExcludingTax != net || i.SubtotalExcludingTax != net || i.AmountDue != i.Total || i.AmountOverpaid != 0 || i.AmountShipping != 0 || i.StartingBalance != 0 || i.EndingBalance != 0 || len(i.TotalDiscountAmounts) != 0 || len(i.Discounts) != 0 {
		return false
	}
	remaining := i.Total - net
	for _, tax := range i.TotalTaxes {
		if tax == nil || tax.TaxBehavior != "exclusive" || tax.Amount < 0 || tax.Amount > remaining {
			return false
		}
		remaining -= tax.Amount
	}
	return remaining == 0
}

func (s *Service) inspectInvoice(ctx context.Context, id string, o Order) (e evidence, err error) {
	e.state, e.reason = "review", "invoice_mismatch"
	catalog, ok := s.C.Prices[o.SKU+"_"+o.Currency]
	if !ok || catalog.ID != o.PriceID || catalog.Amount != o.Amount {
		return
	}
	var inv stripe.Invoice
	if err = s.get(ctx, "/invoices/"+id, nil, &inv); err != nil {
		return
	}
	if inv.ID != id || inv.Customer == nil || inv.Customer.ID != o.Customer || inv.Livemode != s.C.Live || string(inv.Currency) != o.Currency {
		return
	}
	if inv.Created > 0 {
		created := time.Unix(inv.Created, 0).UTC()
		e.created = &created
	}
	if inv.Status != "paid" {
		e.reason = "invoice_unpaid"
		if inv.Status == "open" || inv.Status == "draft" {
			e.state = "pending"
		}
		return
	}
	// Checkout's one-time post-payment invoices use send_invoice even though
	// their payment was captured in Checkout. Flex still has to prove that
	// exact session, invoice, PaymentIntent and captured charge below. Pro
	// remains limited to automatically collected subscription invoices.
	collectionOK := inv.CollectionMethod == "charge_automatically" || o.SKU == "flex" && inv.CollectionMethod == "send_invoice"
	if !catalogTotal(&inv, o.Amount) || inv.AmountRemaining != 0 || inv.AmountPaid != inv.Total || inv.AmountPaidOffStripe != 0 || inv.Subtotal != o.Amount || !collectionOK || inv.AutomaticTax == nil || !inv.AutomaticTax.Enabled || inv.AutomaticTax.Status != "complete" || inv.StatusTransitions == nil || inv.StatusTransitions.PaidAt <= 0 || inv.CustomerAddress == nil || inv.CustomerAddress.Country != o.Country {
		return
	}
	var lines page[stripe.InvoiceLineItem]
	if err = s.get(ctx, "/invoices/"+id+"/lines", url.Values{"limit": {"2"}}, &lines); err != nil {
		return
	}
	if len(lines.Data) != 1 || lines.More {
		return
	}
	l := lines.Data[0]
	if l.ID == "" || l.Pricing == nil || l.Pricing.PriceDetails == nil || l.Pricing.PriceDetails.Price == nil || l.Pricing.PriceDetails.Price.ID != o.PriceID || l.Quantity != 1 || l.Amount != o.Amount || string(l.Currency) != o.Currency || l.Livemode != s.C.Live || len(l.DiscountAmounts) != 0 {
		return
	}
	var flexIntent string
	if o.SKU == "flex" {
		var cs stripe.CheckoutSession
		if err = s.get(ctx, "/checkout/sessions/"+o.Session, nil, &cs); err != nil {
			return
		}
		if cs.Consent == nil || cs.Consent.TermsOfService != "accepted" || cs.ID != o.Session || cs.Metadata["hedvi_order"] != o.ID.String() || cs.ClientReferenceID != o.ID.String() || cs.Customer == nil || cs.Customer.ID != o.Customer || cs.Mode != "payment" || cs.Status != "complete" || cs.PaymentStatus != "paid" || cs.Invoice == nil || cs.Invoice.ID != id || cs.PaymentIntent == nil || cs.PaymentIntent.ID == "" || cs.Livemode != s.C.Live || cs.AmountTotal != inv.Total {
			return
		}
		flexIntent = cs.PaymentIntent.ID
	} else {
		if inv.BillingReason != "subscription_create" && inv.BillingReason != "subscription_cycle" || inv.Parent == nil || inv.Parent.SubscriptionDetails == nil || inv.Parent.SubscriptionDetails.Subscription == nil || inv.Parent.SubscriptionDetails.Subscription.ID != o.Subscription || inv.Parent.SubscriptionDetails.Metadata["hedvi_order"] != o.ID.String() {
			return
		}
		if l.Parent == nil || l.Parent.SubscriptionItemDetails == nil || l.Parent.SubscriptionItemDetails.Proration || l.Parent.SubscriptionItemDetails.Subscription != o.Subscription || o.Item == "" || l.Parent.SubscriptionItemDetails.SubscriptionItem != o.Item || l.Period == nil {
			return
		}
		e.start = time.Unix(l.Period.Start, 0).UTC()
		e.end = time.Unix(l.Period.End, 0).UTC()
		if days := e.end.Sub(e.start); days < 27*24*time.Hour || days > 32*24*time.Hour {
			return
		}
	}
	var payments page[stripe.InvoicePayment]
	if err = s.get(ctx, "/invoice_payments", url.Values{"invoice": {id}, "status": {"paid"}, "limit": {"2"}}, &payments); err != nil {
		return
	}
	if len(payments.Data) != 1 || payments.More {
		e.reason = "payment_evidence_missing"
		return
	}
	payment := payments.Data[0]
	if payment.Status != "paid" || payment.Livemode != s.C.Live || payment.Invoice == nil || payment.Invoice.ID != id || payment.AmountPaid != inv.Total || string(payment.Currency) != o.Currency || payment.Payment == nil || payment.Payment.Type != "payment_intent" || payment.Payment.PaymentIntent == nil {
		return
	}
	e.payment = payment.Payment.PaymentIntent.ID
	if o.SKU == "flex" && flexIntent != e.payment {
		return
	}
	var pi stripe.PaymentIntent
	if err = s.get(ctx, "/payment_intents/"+e.payment, nil, &pi); err != nil {
		return
	}
	if pi.ID != e.payment || pi.Livemode != s.C.Live || pi.Status != "succeeded" || pi.Customer == nil || pi.Customer.ID != o.Customer || pi.AmountReceived != inv.Total || pi.LatestCharge == nil || string(pi.Currency) != o.Currency {
		return
	}
	e.charge = pi.LatestCharge.ID
	var ch stripe.Charge
	if err = s.get(ctx, "/charges/"+e.charge, nil, &ch); err != nil {
		return
	}
	if ch.ID != e.charge || ch.Customer == nil || ch.Customer.ID != o.Customer || ch.PaymentIntent == nil || ch.PaymentIntent.ID != pi.ID || ch.Livemode != s.C.Live || !ch.Paid || !ch.Captured || ch.AmountCaptured != inv.Total || string(ch.Currency) != o.Currency || ch.PaymentMethodDetails == nil || ch.PaymentMethodDetails.Type != "card" {
		return
	}
	e.total = inv.Total
	e.line = l.ID
	e.paid = time.Unix(inv.StatusTransitions.PaidAt, 0).UTC()
	e.state, e.reason = "granted", "verified_payment"
	if ch.Refunded || ch.AmountRefunded >= inv.Total {
		e.state, e.reason = "revoked", "full_refund"
		return
	}
	if ch.AmountRefunded > 0 || inv.PostPaymentCreditNotesAmount > 0 || inv.PrePaymentCreditNotesAmount > 0 {
		e.state, e.reason = "held", "partial_refund_or_credit_note"
		return
	}
	// Pending refunds need review even while the captured charge shows no refunded amount.
	err = scanPages(ctx, s, "/refunds", url.Values{"charge": {ch.ID}}, func(v stripe.Refund) string { return v.ID }, func(v stripe.Refund) error {
		if v.Charge == nil || v.Charge.ID != ch.ID {
			return errors.New("Stripe refund ownership mismatch")
		}
		if v.Status != "failed" && v.Status != "canceled" {
			e.state, e.reason = "held", "refund_review"
		}
		return nil
	})
	if err != nil {
		return
	}
	if ch.Disputed {
		found := false
		err = scanPages(ctx, s, "/disputes", url.Values{"charge": {ch.ID}}, func(v stripe.Dispute) string { return v.ID }, func(v stripe.Dispute) error {
			if v.Charge == nil || v.Charge.ID != ch.ID || v.Livemode != s.C.Live {
				return errors.New("Stripe dispute ownership mismatch")
			}
			found = true
			switch v.Status {
			case "won", "warning_closed":
			case "lost":
				e.state, e.reason = "revoked", "lost_dispute"
			default:
				if e.state != "revoked" {
					e.state, e.reason = "held", "dispute_review"
				}
			}
			return nil
		})
		if !found && err == nil {
			e.state, e.reason = "held", "dispute_evidence_missing"
		}
	}
	return
}

// Reversal delivery and provider reads can arrive out of order. A signed
// indication received before we knew the charge still needs direct corroboration
// before any allocation is usable. Check the event IDs again at commit.
const reversalKinds = "('charge.refunded','refund.created','refund.updated','charge.dispute.created','charge.dispute.updated','charge.dispute.closed','credit_note.created','credit_note.updated')"

func (s *Service) corroborate(ctx context.Context, invoice string, e *evidence) ([]string, error) {
	type signal struct{ ID, Kind, Object string }
	rows, err := s.Pool.Query(ctx, `SELECT id,kind,object_id FROM billing_events WHERE (invoice_id=$1 OR charge_id=$2) AND kind IN `+reversalKinds+` ORDER BY id LIMIT 257`, invoice, e.charge)
	if err != nil {
		return nil, err
	}
	signals, err := pgx.CollectRows(rows, pgx.RowToStructByPos[signal])
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(signals))
	if len(signals) > 256 {
		e.state, e.reason = "held", "reversal_evidence_limit"
		return ids, nil
	}
	verified := map[string]bool{}
	for _, sig := range signals {
		ids = append(ids, sig.ID)
		if verified[sig.Object] {
			continue
		}
		switch {
		case strings.HasPrefix(sig.Kind, "refund."):
			var r stripe.Refund
			if err = s.get(ctx, "/refunds/"+sig.Object, nil, &r); err != nil {
				return nil, err
			}
			if r.ID != sig.Object || r.Charge == nil || r.Charge.ID != e.charge || r.Status != "failed" && r.Status != "canceled" {
				e.state, e.reason = "held", "refund_signal_pending"
			}
		case strings.HasPrefix(sig.Kind, "charge.dispute."):
			var d stripe.Dispute
			if err = s.get(ctx, "/disputes/"+sig.Object, nil, &d); err != nil {
				return nil, err
			}
			if d.ID != sig.Object || d.Charge == nil || d.Charge.ID != e.charge || d.Livemode != s.C.Live || d.Status != "won" && d.Status != "warning_closed" {
				e.state, e.reason = "held", "dispute_signal_pending"
			}
			if d.ID == sig.Object && d.Charge != nil && d.Charge.ID == e.charge && d.Livemode == s.C.Live && d.Status == "lost" {
				e.state, e.reason = "revoked", "lost_dispute"
			}
		case strings.HasPrefix(sig.Kind, "credit_note."):
			var n stripe.CreditNote
			if err = s.get(ctx, "/credit_notes/"+sig.Object, nil, &n); err != nil {
				return nil, err
			}
			if n.ID != sig.Object || n.Invoice == nil || n.Invoice.ID != invoice || n.Livemode != s.C.Live || n.Status != "void" {
				e.state, e.reason = "held", "credit_note_signal_pending"
			}
		default:
			// charge.refunded denotes a successful full or partial refund. A
			// charge snapshot with a zero refunded amount cannot clear it.
			e.state, e.reason = "held", "refund_signal_pending"
		}
		if e.state != "granted" {
			return ids, nil
		}
		verified[sig.Object] = true
	}
	return ids, nil
}

func (s *Service) checkInvoice(ctx context.Context, j job) error {
	var o Order
	var org uuid.UUID
	var corroborated []string
	err := s.Pool.QueryRow(ctx, `SELECT o.org_id,o.id,o.sku,o.currency,o.country,o.price_id,o.amount,c.customer_id,coalesce(o.session_id,''),coalesce(o.subscription_id,''),coalesce(o.subscription_item_id,'') FROM billing_invoices i JOIN billing_orders o ON o.org_id=i.org_id AND o.id=i.order_id JOIN billing_customers c ON c.org_id=o.org_id WHERE i.id=$1`, j.id).Scan(&org, &o.ID, &o.SKU, &o.Currency, &o.Country, &o.PriceID, &o.Amount, &o.Customer, &o.Session, &o.Subscription, &o.Item)
	if err != nil {
		return err
	}
	e, err := s.inspectInvoice(ctx, j.id, o)
	if err != nil {
		return err
	}
	if e.state == "granted" {
		corroborated, err = s.corroborate(ctx, j.id, &e)
		if err != nil {
			return err
		}
	}
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT hedvi_bill_lock_refs($1,$2)`, j.id, e.charge); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, org); err != nil {
			return err
		}
		var old, oldReason, oldPay, oldCharge, oldLine string
		err := tx.QueryRow(ctx, `SELECT state,reason,coalesce(payment_id,''),coalesce(charge_id,''),coalesce(line_id,'') FROM billing_invoices WHERE id=$1 AND lease=$2 FOR UPDATE`, j.id, j.lease).Scan(&old, &oldReason, &oldPay, &oldCharge, &oldLine)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if oldPay != "" && e.payment != "" && oldPay != e.payment || oldCharge != "" && e.charge != "" && oldCharge != e.charge || oldLine != "" && e.line != "" && oldLine != e.line {
			e.state, e.reason = "held", "payment_binding_changed"
			e.payment, e.charge, e.line = "", "", ""
		}
		if e.state == "granted" {
			var changed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_events WHERE (invoice_id=$1 OR charge_id=$2) AND kind IN `+reversalKinds+` AND NOT (id=ANY($3::text[])))`, j.id, e.charge, corroborated).Scan(&changed); err != nil {
				return err
			}
			if changed {
				e.state, e.reason = "held", "payment_change_pending"
			}
		}
		if old == "revoked" {
			e.state, e.reason = old, oldReason
		}
		if e.state == "granted" && o.SKU != "flex" {
			var overlap bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usage_periods p JOIN billing_invoices i ON i.id=p.stripe_invoice WHERE p.org_id=$1 AND i.state<>'revoked' AND p.stripe_invoice<>$2 AND p.period_start<$4 AND $3<p.period_end)`, org, j.id, e.start, e.end).Scan(&overlap); err != nil {
				return err
			}
			if overlap {
				e.state, e.reason = "review", "overlapping_paid_cycle"
			}
		}
		active := e.state == "granted"
		if active {
			if o.SKU == "flex" {
				var lot uuid.UUID
				err := tx.QueryRow(ctx, `SELECT id FROM credit_lots WHERE org_id=$1 AND stripe_invoice=$2 FOR UPDATE`, org, j.id).Scan(&lot)
				if errors.Is(err, pgx.ErrNoRows) {
					lot, err = quota.GrantCredits(ctx, tx, org, "purchase", "stripe:"+j.id, plans.FlexPackRecipients, plans.FlexPackDataBytes, plans.FlexPackValidity, e.paid)
					if err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `UPDATE credit_lots SET stripe_invoice=$2 WHERE id=$1`, lot, j.id); err != nil {
						return err
					}
					if _, err = tx.Exec(ctx, `UPDATE organizations SET plan='flex' WHERE id=$1 AND plan='free' AND NOT EXISTS(SELECT 1 FROM credit_lots WHERE org_id=$1 AND stripe_invoice IS NOT NULL AND stripe_invoice<>$2)`, org, j.id); err != nil {
						return err
					}
				} else if err != nil {
					return err
				}

				if _, err = tx.Exec(ctx, `UPDATE billing_orders SET state='paid',next_check_at='infinity' WHERE id=$1`, o.ID); err != nil {
					return err
				}
			} else {
				p, err := plans.Get(o.SKU)
				if err != nil {
					return err
				}
				tag, err := tx.Exec(ctx, `INSERT INTO usage_periods(org_id,period_start,period_end,plan,included,data_included,stripe_invoice) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(stripe_invoice) DO NOTHING`, org, e.start, e.end, o.SKU, p.IncludedRecipients, p.IncludedDataBytes, j.id)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 1 {
					if _, err = tx.Exec(ctx, `INSERT INTO credit_ledger(org_id,usage_period_id,kind,source,quantity,data_bytes,note) SELECT org_id,id,'grant','period',included,data_included,'Paid Stripe subscription cycle' FROM usage_periods WHERE stripe_invoice=$1`, j.id); err != nil {
						return err
					}
				}
				if _, err = tx.Exec(ctx, `UPDATE billing_orders SET state='active' WHERE id=$1 AND state NOT IN ('ended','expired')`, o.ID); err != nil {
					return err
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE usage_periods SET billing_usable=$2 WHERE stripe_invoice=$1 AND billing_usable<>$2`, j.id, active); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE credit_lots SET billing_usable=$2 WHERE stripe_invoice=$1 AND billing_usable<>$2`, j.id, active); err != nil {
			return err
		}
		if old != e.state || oldReason != e.reason {
			if _, err := tx.Exec(ctx, `INSERT INTO billing_adjustments(org_id,invoice_id,prior_state,new_state,reason) VALUES($1,$2,$3,$4,$5)`, org, j.id, old, e.state, e.reason); err != nil {
				return err
			}
		}
		if e.state == "review" || e.state == "held" {
			if _, err := tx.Exec(ctx, `INSERT INTO alerts(org_id,kind,severity,dedupe_key,message) VALUES($1,'billing_review','warning',$2,'A payment needs review. Check the billing order before retrying.') ON CONFLICT (dedupe_key) WHERE resolved_at IS NULL DO NOTHING`, org, "billing-invoice:"+j.id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, "billing-invoice:"+j.id); err != nil {
			return err
		}
		var paid *time.Time
		if !e.paid.IsZero() {
			paid = &e.paid
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE resolved_at IS NULL AND dedupe_key=$1`, "billing-read:"+j.id); err != nil {
			return err
		}
		var until *time.Time
		if active {
			end := e.end
			if o.SKU == "flex" {
				end = e.paid.Add(plans.FlexPackValidity)
			}
			until = &end
		}
		_, err = tx.Exec(ctx, `UPDATE billing_invoices SET state=$2,reason=$3,payment_id=COALESCE(payment_id,NULLIF($4,'')),charge_id=COALESCE(charge_id,NULLIF($5,'')),line_id=COALESCE(line_id,NULLIF($6,'')),paid_at=COALESCE(paid_at,$7),amount_total=COALESCE(amount_total,NULLIF($8::bigint,0)),stripe_created_at=COALESCE(stripe_created_at,$10),checked_at=now(),check_failures=0,lease=NULL,lease_until=NULL,next_check_at=CASE WHEN $2='revoked' OR $9::timestamptz<=now() THEN 'infinity'::timestamptz WHEN $3='payment_change_pending' THEN now()+interval '1 second' WHEN $2 IN ('review','pending','held') AND created_at<now()-interval '90 days' AND NOT EXISTS(SELECT 1 FROM credit_lots WHERE stripe_invoice=$1 AND expires_at>now()) AND NOT EXISTS(SELECT 1 FROM usage_periods WHERE stripe_invoice=$1 AND period_end>now()) THEN 'infinity'::timestamptz WHEN $2='review' AND created_at>now()-interval '1 day' THEN now()+interval '15 minutes' WHEN $2='pending' THEN now()+interval '6 hours' ELSE now()+interval '1 day' END WHERE id=$1`, j.id, e.state, e.reason, e.payment, e.charge, e.line, paid, e.total, until, e.created)
		return err
	})
}
