package billing

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v87"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/db"
)

type Order struct {
	ID                                                                            uuid.UUID
	SKU, Currency, Country, PriceID, Customer, Session, Subscription, Item, State string
	Amount                                                                        int64
	Created                                                                       time.Time
}
type OrderSummary struct {
	CancelAt    *time.Time `json:"cancel_at,omitempty"`
	PaidThrough *time.Time `json:"paid_through,omitempty"`
	ID          uuid.UUID  `json:"id"`
	SKU         string     `json:"sku"`
	Currency    string     `json:"currency"`
	State       string     `json:"state"`
	Created     time.Time  `json:"created_at"`
}
type Status struct {
	Baseline    string         `json:"baseline"`
	HasCredits  bool           `json:"has_credits"`
	HasPro      bool           `json:"has_pro"`
	Enabled     bool           `json:"enabled"`
	Currency    string         `json:"currency"`
	Countries   []string       `json:"countries"`
	HasCustomer bool           `json:"has_customer"`
	Orders      []OrderSummary `json:"orders"`
}

func (s *Service) customerTx(ctx context.Context, org, user uuid.UUID, fn func(pgx.Tx) error) error {
	return db.InUser(ctx, s.Pool, user, &org, func(tx pgx.Tx) error {
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT role IN ('owner','admin','billing') FROM memberships WHERE org_id=$1 AND user_id=$2 FOR SHARE`, org, user).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !allowed {
			return apierr.ErrNotFound
		}
		if err != nil {
			return err
		}
		return fn(tx)
	})
}
func (s *Service) Status(ctx context.Context, org, user uuid.UUID) (Status, error) {
	v := Status{Enabled: true, Orders: []OrderSummary{}}
	for c := range s.C.Countries {
		v.Countries = append(v.Countries, c)
	}
	sort.Strings(v.Countries)
	err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_orders b WHERE b.org_id=o.id AND b.sku<>'flex' AND b.state NOT IN ('expired','ended')),o.plan,EXISTS(SELECT 1 FROM credit_lots l WHERE l.org_id=o.id AND billing_usable AND expires_at>now() AND expired_at IS NULL AND granted>reserved+consumed+expired_amount),COALESCE(c.currency,''),c.customer_id IS NOT NULL FROM organizations o LEFT JOIN billing_customers c ON c.org_id=o.id WHERE o.id=$1`, org).Scan(&v.HasPro, &v.Baseline, &v.HasCredits, &v.Currency, &v.HasCustomer); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,sku,currency,state,created_at,cancel_at,paid_through FROM billing_order_status WHERE org_id=$1 ORDER BY created_at DESC LIMIT 25`, org)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row OrderSummary
			if err := rows.Scan(&row.ID, &row.SKU, &row.Currency, &row.State, &row.Created, &row.CancelAt, &row.PaidThrough); err != nil {
				return err
			}
			v.Orders = append(v.Orders, row)
		}
		return rows.Err()
	})
	return v, err
}
func (s *Service) beginAction(ctx context.Context, org, user uuid.UUID) (func(), error) {
	limited := apierr.New(429, "billing_rate_limit", "Too many billing requests. Check existing orders and try again later.")
	select {
	case s.checkoutSlots <- struct{}{}:
	default:
		return nil, limited
	}
	done := func() { <-s.checkoutSlots }
	err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT hedvi_admit_billing_action($1)`, org).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return limited
		}
		return nil
	})
	if err != nil {
		done()
		return nil, err
	}
	if err := s.Validate(ctx); err != nil {
		done()
		return nil, err
	}
	return done, nil
}
func (s *Service) Checkout(ctx context.Context, org, user, project, token uuid.UUID, sku, currency, country string) (string, error) {
	p, ok := s.C.Prices[sku+"_"+currency]
	if !ok || !s.C.Countries[country] || token == uuid.Nil {
		return "", apierr.New(422, "validation_error", "Choose an available plan, currency and billing country.")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done, limitErr := s.beginAction(ctx, org, user)
	if limitErr != nil {
		return "", limitErr
	}
	defer done()
	var customerUUID uuid.UUID
	var customerCreated time.Time
	var o Order
	err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, org); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO billing_customers(org_id,currency) VALUES($1,$2) ON CONFLICT DO NOTHING`, org, currency); err != nil {
			return err
		}
		var boundCurrency string
		if err := tx.QueryRow(ctx, `SELECT id,created_at,currency,coalesce(customer_id,'') FROM billing_customers WHERE org_id=$1`, org).Scan(&customerUUID, &customerCreated, &boundCurrency, &o.Customer); err != nil {
			return err
		}
		if boundCurrency != currency {
			return apierr.New(409, "billing_currency", "This organization's billing currency is already selected.")
		}
		// A timed-out create, double-click or second browser tab reuses an open checkout.
		err := tx.QueryRow(ctx, `SELECT return_project_id,id,sku,currency,country,coalesce(session_id,''),state,created_at FROM billing_orders WHERE org_id=$1 AND (request_id=$2 OR (sku=$3 AND state IN ('creating','open','review')) OR ($3<>'flex' AND sku<>'flex' AND state NOT IN ('ended','expired'))) ORDER BY (request_id=$2) DESC,created_at LIMIT 1`, org, token, sku).Scan(&project, &o.ID, &o.SKU, &o.Currency, &o.Country, &o.Session, &o.State, &o.Created)
		if errors.Is(err, pgx.ErrNoRows) {
			return tx.QueryRow(ctx, `INSERT INTO billing_orders(org_id,request_id,sku,currency,country,price_id,amount,return_project_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,state,created_at`, org, token, sku, currency, country, p.ID, p.Amount, project).Scan(&o.ID, &o.State, &o.Created)
		}
		if err != nil {
			return err
		}
		if o.SKU != sku || o.Country != country || o.Currency != currency || o.State != "creating" && o.State != "open" {
			return apierr.New(409, "billing_order_exists", "This organization already has an order or subscription. Check billing status or use the customer portal.")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if o.Customer == "" {
		if time.Since(customerCreated) > 23*time.Hour {
			return "", apierr.New(409, "billing_review", "Customer setup requires reconciliation. Contact support.")
		}
		var customer stripe.Customer
		err = s.call(ctx, "POST", "/customers", url.Values{"metadata[hedvi_customer]": {customerUUID.String()}}, "hedvi-customer-"+customerUUID.String(), &customer)
		if err != nil {
			return "", err
		}
		if !stripeID.MatchString(customer.ID) || customer.Livemode != s.C.Live {
			return "", errors.New("unexpected Stripe customer")
		}
		o.Customer = customer.ID
		if err = s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
			tag, e := tx.Exec(ctx, `UPDATE billing_customers SET customer_id=$2 WHERE org_id=$1 AND (customer_id IS NULL OR customer_id=$2)`, org, customer.ID)
			if e == nil && tag.RowsAffected() != 1 {
				return errors.New("billing customer conflict")
			}
			return e
		}); err != nil {
			return "", err
		}
	}
	if err := s.verifyCustomer(ctx, o.Customer, customerUUID); err != nil {
		return "", err
	}
	var session stripe.CheckoutSession
	if o.Session != "" {
		err = s.get(ctx, "/checkout/sessions/"+o.Session, nil, &session)
	} else {
		if time.Since(o.Created) > 23*time.Hour {
			return "", apierr.New(409, "billing_review", "Checkout setup requires reconciliation. Contact support.")
		}
		// Stable return destinations are required when a purchase is retried from a different project.
		dest := s.C.AppURL + "/app/" + project.String() + "/settings/billing-return"
		v := url.Values{"mode": {p.mode()}, "customer": {o.Customer}, "line_items[0][price]": {p.ID}, "line_items[0][quantity]": {"1"}, "payment_method_types[0]": {"card"}, "adaptive_pricing[enabled]": {"false"}, "allow_promotion_codes": {"false"}, "consent_collection[terms_of_service]": {"required"}, "billing_address_collection": {"required"}, "customer_update[address]": {"auto"}, "customer_update[name]": {"auto"}, "tax_id_collection[enabled]": {"true"}, "automatic_tax[enabled]": {"true"}, "client_reference_id": {o.ID.String()}, "metadata[hedvi_order]": {o.ID.String()}, "success_url": {dest + "?order=" + o.ID.String()}, "cancel_url": {dest + "?order=" + o.ID.String()}}
		if sku == "flex" {
			v.Set("invoice_creation[enabled]", "true")
			v.Set("payment_intent_data[metadata][hedvi_order]", o.ID.String())
		} else {
			v.Set("subscription_data[metadata][hedvi_order]", o.ID.String())
		}
		err = s.call(ctx, "POST", "/checkout/sessions", v, "hedvi-checkout-"+o.ID.String(), &session)
	}
	if err != nil {
		return "", err
	}
	if session.Customer == nil || session.Customer.ID != o.Customer || session.ClientReferenceID != o.ID.String() || session.Mode != stripe.CheckoutSessionMode(p.mode()) || session.Livemode != s.C.Live || !stripeID.MatchString(session.ID) {
		return "", errors.New("unexpected Stripe session")
	}
	if err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE billing_orders SET session_id=$2 WHERE id=$1 AND (session_id=$2 OR session_id IS NULL)`, o.ID, session.ID)
		if e == nil && tag.RowsAffected() != 1 {
			return errors.New("checkout binding conflict")
		}
		return e
	}); err != nil {
		return "", err
	}
	if session.Status != "open" || !stripeURL(session.URL, "checkout.stripe.com") {
		return "", apierr.New(409, "billing_order_exists", "Checkout has finished. Check billing status.")
	}
	return session.URL, nil
}
func stripeURL(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Host == host
}
func (s *Service) verifyCustomer(ctx context.Context, id string, local uuid.UUID) error {
	if !stripeID.MatchString(id) || !strings.HasPrefix(id, "cus_") {
		return errors.New("customer binding missing")
	}
	var c stripe.Customer
	if err := s.get(ctx, "/customers/"+id, nil, &c); err != nil {
		return err
	}
	if c.ID != id || c.Deleted || c.Livemode != s.C.Live || c.Metadata["hedvi_customer"] != local.String() {
		return errors.New("Stripe customer binding differs from the organization")
	}
	return nil
}
func (s *Service) Portal(ctx context.Context, org, user, project uuid.UUID) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	done, limitErr := s.beginAction(ctx, org, user)
	if limitErr != nil {
		return "", limitErr
	}
	defer done()
	var customer string
	var local uuid.UUID
	err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT customer_id,id FROM billing_customers WHERE org_id=$1 AND customer_id IS NOT NULL`, org).Scan(&customer, &local)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apierr.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err := s.verifyCustomer(ctx, customer, local); err != nil {
		return "", err
	}
	var cfg stripe.BillingPortalConfiguration
	if err = s.get(ctx, "/billing_portal/configurations/"+s.C.Portal, nil, &cfg); err != nil {
		return "", err
	}
	if !cfg.Active || cfg.ID != s.C.Portal || cfg.Livemode != s.C.Live || cfg.Features == nil || cfg.Features.InvoiceHistory == nil || !cfg.Features.InvoiceHistory.Enabled || cfg.Features.PaymentMethodUpdate == nil || !cfg.Features.PaymentMethodUpdate.Enabled || cfg.Features.CustomerUpdate == nil || cfg.Features.CustomerUpdate.Enabled || cfg.Features.SubscriptionUpdate == nil || cfg.Features.SubscriptionUpdate.Enabled || cfg.Features.SubscriptionCancel == nil || !cfg.Features.SubscriptionCancel.Enabled || cfg.Features.SubscriptionCancel.Mode != "at_period_end" || cfg.Features.SubscriptionCancel.ProrationBehavior != "none" {
		return "", errors.New("the Stripe portal policy does not match the supported billing contract")
	}
	var result stripe.BillingPortalSession
	err = s.call(ctx, "POST", "/billing_portal/sessions", url.Values{"configuration": {s.C.Portal}, "customer": {customer}, "return_url": {s.C.AppURL + "/app/" + project.String() + "/settings"}}, "hedvi-portal-"+uuid.NewString(), &result)
	if err != nil {
		return "", err
	}
	if !stripeURL(result.URL, "billing.stripe.com") || result.Customer != customer || result.Configuration == nil || result.Configuration.ID != s.C.Portal || result.Livemode != s.C.Live {
		return "", errors.New("unexpected portal destination")
	}
	if err := s.customerTx(ctx, org, user, func(tx pgx.Tx) error { return nil }); err != nil {
		return "", err
	}
	return result.URL, nil
}
