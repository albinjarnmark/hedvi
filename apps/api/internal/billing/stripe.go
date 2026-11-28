// Package billing reconciles Stripe payments with durable Hedvi entitlements.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stripe/stripe-go/v87"
	"hedvi.dev/api/internal/plans"
)

const APIVersion = stripe.APIVersion

var errCustomerLocation = errors.New("Stripe rejected the new customer tax location")

var stripeID = regexp.MustCompile(`^[a-zA-Z0-9_]{5,255}$`)
var countryCode = regexp.MustCompile(`^[A-Z]{2}$`)

type Price struct {
	SKU, Currency, ID, Product string
	Amount                     int64
}
type Config struct {
	Account, Portal, APIKey, ReaderKey, Secret, PreviousSecret, AppURL string
	Live                                                               bool
	RequestVersion                                                     int
	Prices                                                             map[string]Price
	Countries                                                          map[string]bool
}

func Load(command, env, appURL string) (*Config, error) {
	flag := os.Getenv("HEDVI_BILLING_ENABLED")
	if flag != "" && flag != "false" && flag != "true" {
		return nil, errors.New("HEDVI_BILLING_ENABLED must be true or false")
	}
	if flag != "true" || command == "migrate" {
		return nil, nil
	}
	c := &Config{Account: os.Getenv("HEDVI_STRIPE_ACCOUNT_ID"), Portal: os.Getenv("HEDVI_STRIPE_PORTAL_CONFIGURATION"), APIKey: os.Getenv("HEDVI_STRIPE_API_KEY"), ReaderKey: os.Getenv("HEDVI_STRIPE_RECONCILE_KEY"), Secret: os.Getenv("HEDVI_STRIPE_WEBHOOK_SECRET"), PreviousSecret: os.Getenv("HEDVI_STRIPE_PREVIOUS_WEBHOOK_SECRET"), AppURL: strings.TrimRight(appURL, "/"), Prices: map[string]Price{}, Countries: map[string]bool{}}
	c.RequestVersion = 1 // Upgrade writers only after every API replica understands both formats.
	if v := os.Getenv("HEDVI_STRIPE_REQUEST_VERSION"); v != "" {
		version, err := strconv.Atoi(v)
		if err != nil || (version != 1 && version != 2) {
			return nil, errors.New("HEDVI_STRIPE_REQUEST_VERSION must be 1 or 2")
		}
		c.RequestVersion = version
	}
	mode := os.Getenv("HEDVI_STRIPE_MODE")
	c.Live = mode == "live"
	if (mode != "live" && mode != "test") || c.Live != (env == "production") || !strings.HasPrefix(c.Account, "acct_") || !stripeID.MatchString(c.Account) || !strings.HasPrefix(c.Portal, "bpc_") || !stripeID.MatchString(c.Portal) {
		return nil, errors.New("invalid Stripe account, environment or portal configuration")
	}
	for _, country := range strings.Split(os.Getenv("HEDVI_STRIPE_COUNTRIES"), ",") {
		country = strings.ToUpper(strings.TrimSpace(country))
		if !countryCode.MatchString(country) {
			return nil, errors.New("provide the authorized ISO billing countries")
		}
		c.Countries[country] = true
	}
	ids := map[string]bool{}
	products := map[string]bool{}
	for _, sku := range []string{"flex", "pro_50k", "pro_100k"} {
		product := os.Getenv("HEDVI_STRIPE_PRODUCT_" + strings.ToUpper(sku))
		if products[product] || !strings.HasPrefix(product, "prod_") || !stripeID.MatchString(product) {
			return nil, errors.New("three distinct Stripe catalog products are required")
		}
		products[product] = true
		usd, sek := int64(plans.FlexPackUSDCents), int64(plans.FlexPackSEKOre)
		if sku != "flex" {
			p, _ := plans.Get(sku)
			usd, sek = p.PriceUSDCents, p.PriceSEKOre
		}
		for currency, amount := range map[string]int64{"usd": usd, "sek": sek} {
			id := os.Getenv("HEDVI_STRIPE_PRICE_" + strings.ToUpper(sku+"_"+currency))
			if !strings.HasPrefix(id, "price_") || !stripeID.MatchString(id) || ids[id] {
				return nil, errors.New("six distinct Stripe prices are required")
			}
			ids[id] = true
			c.Prices[sku+"_"+currency] = Price{SKU: sku, Currency: currency, ID: id, Product: product, Amount: amount}
		}
	}
	prefix := "rk_" + mode + "_"
	if command == "api" || command == "dev" {
		if !strings.HasPrefix(c.APIKey, prefix) || !strings.HasPrefix(c.Secret, "whsec_") || len(c.Secret) < 25 || (c.PreviousSecret != "" && (!strings.HasPrefix(c.PreviousSecret, "whsec_") || len(c.PreviousSecret) < 25)) {
			return nil, errors.New("Stripe API requires the matching restricted key and endpoint secret")
		}
		if command == "api" && c.ReaderKey != "" {
			return nil, errors.New("keep the reconciliation key on the worker")
		}
	}
	if command == "worker" || command == "dev" || command == "admin" {
		if !strings.HasPrefix(c.ReaderKey, prefix) {
			return nil, errors.New("Stripe reconciliation requires its restricted read key")
		}
		if command != "dev" && (c.APIKey != "" || c.Secret != "" || c.PreviousSecret != "") {
			return nil, errors.New("Stripe creation and signing secrets belong only to the API")
		}
	}
	return c, nil
}

type Service struct {
	C              Config
	Pool           *pgxpool.Pool
	http           *http.Client
	base, key      string
	checkoutSlots  chan struct{}
	reader         bool
	validationMu   sync.Mutex
	nextValidation time.Time
	validationErr  error
}

func New(c Config, pool *pgxpool.Pool, reader bool) *Service {
	s := &Service{reader: reader, checkoutSlots: make(chan struct{}, 4), C: c, Pool: pool, base: "https://api.stripe.com/v1", key: c.APIKey, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if reader {
		s.key = c.ReaderKey
	}
	return s
}

// Validate isolates a temporarily unavailable payment provider from email and
// durable signed refund admission. Each billing process checks its own key.
func (s *Service) Validate(ctx context.Context) error {
	s.validationMu.Lock()
	defer s.validationMu.Unlock()
	if time.Now().Before(s.nextValidation) {
		return s.validationErr
	}
	s.validationErr = s.validateCatalog(ctx)
	ttl := time.Hour
	if s.validationErr != nil {
		ttl = 30 * time.Second
	}
	s.nextValidation = time.Now().Add(ttl)
	return s.validationErr
}
func (s *Service) validateCatalog(ctx context.Context) error {
	c := s.C
	var acct struct{ ID string }
	if err := s.get(ctx, "/account", nil, &acct); err != nil {
		return err
	}
	if acct.ID != c.Account {
		return errors.New("Stripe key belongs to a different merchant")
	}
	for _, p := range c.Prices {
		var price stripe.Price
		if err := s.get(ctx, "/prices/"+p.ID, url.Values{"expand[]": {"product"}}, &price); err != nil {
			return err
		}
		ok := price.ID == p.ID && price.Active && price.Livemode == c.Live && string(price.Currency) == p.Currency && price.BillingScheme == "per_unit" && price.UnitAmount == p.Amount && price.UnitAmountDecimal.String() == strconv.FormatInt(p.Amount, 10) && price.TaxBehavior == "exclusive" && price.Product != nil && price.Product.ID == p.Product && price.Product.Active && price.Product.Livemode == c.Live && price.Product.TaxCode != nil && price.Product.TaxCode.ID == "txcd_10103001"
		if p.SKU == "flex" {
			ok = ok && price.Type == "one_time" && price.Recurring == nil
		} else {
			ok = ok && price.Type == "recurring" && price.Recurring != nil && price.Recurring.Interval == "month" && price.Recurring.IntervalCount == 1 && price.Recurring.UsageType == "licensed"
		}
		if !ok {
			return fmt.Errorf("Stripe price does not match catalog for %s %s", p.SKU, p.Currency)
		}
	}
	if s.reader {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO billing_installation(singleton,account,live) VALUES(true,$1,$2) ON CONFLICT DO NOTHING`, c.Account, c.Live); err != nil {
			return err
		}
	}
	return s.bound(ctx)
}
func (s *Service) bound(ctx context.Context) error {
	var match bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_installation WHERE account=$1 AND live=$2)`, s.C.Account, s.C.Live).Scan(&match)
	if err != nil {
		return err
	}
	if !match {
		return errors.New("Stripe installation is not initialized for this account and mode; start the matching billing worker first")
	}
	return nil
}
func (s *Service) get(ctx context.Context, path string, v url.Values, out any) error {
	return s.call(ctx, http.MethodGet, path, v, "", out)
}
func (s *Service) call(ctx context.Context, method, path string, v url.Values, key string, out any) error {
	u := s.base + path
	var body io.Reader
	if method == http.MethodGet {
		if len(v) > 0 {
			u += "?" + v.Encode()
		}
	} else {
		body = strings.NewReader(v.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(s.key, "")
	req.Header.Set("Stripe-Version", APIVersion)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return errors.New("Stripe request could not be confirmed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if method == http.MethodPost && path == "/customers" && resp.StatusCode == 400 {
			var failure struct {
				Error struct {
					Code string `json:"code"`
					Type string `json:"type"`
				} `json:"error"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 32<<10)).Decode(&failure)
			if decodeErr == nil && failure.Error.Type == "invalid_request_error" && failure.Error.Code == "customer_tax_location_invalid" && resp.Header.Get("Stripe-Should-Retry") != "true" {
				return errCustomerLocation
			}
		}
		return fmt.Errorf("Stripe returned HTTP %d (request %s)", resp.StatusCode, resp.Header.Get("Request-Id"))
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(b) > 4<<20 {
		return errors.New("Stripe response unavailable")
	}
	return json.Unmarshal(b, out)
}
func (p Price) mode() string {
	if p.SKU == "flex" {
		return "payment"
	}
	return "subscription"
}
