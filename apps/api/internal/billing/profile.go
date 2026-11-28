package billing

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stripe/stripe-go/v87"
	"hedvi.dev/api/internal/apierr"
)

// Profile is the immutable invoice address selected before a Checkout URL can
// exist. Name seeds Customer.name; Checkout's existing tax-ID flow may replace
// that name with the legal business name, but may never replace this address.
type Profile struct {
	Name       string `json:"name"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	PostalCode string `json:"postal_code"`
	State      string `json:"state"`
	Country    string `json:"country"`
}

func (s *Service) normalizeProfile(p *Profile) (Profile, error) {
	invalid := apierr.New(422, "billing_address", "Provide your company's billing name, street address, city, postal code and an available billing country.")
	if p == nil {
		return Profile{}, invalid
	}
	v := *p
	for _, f := range []*string{&v.Name, &v.Line1, &v.Line2, &v.City, &v.PostalCode, &v.State, &v.Country} {
		*f = strings.TrimSpace(*f)
		if !utf8.ValidString(*f) || utf8.RuneCountInString(*f) > 200 || strings.IndexFunc(*f, unicode.IsControl) >= 0 {
			return Profile{}, invalid
		}
	}
	v.Country = strings.ToUpper(v.Country)
	if v.Name == "" || v.Line1 == "" || v.City == "" || v.PostalCode == "" || utf8.RuneCountInString(v.PostalCode) > 20 || !countryCode.MatchString(v.Country) || !s.C.Countries[v.Country] {
		return Profile{}, invalid
	}
	return v, nil
}

func (p Profile) customerParams(local string) url.Values {
	return url.Values{
		"metadata[hedvi_customer]": {local}, "name": {p.Name},
		"address[line1]": {p.Line1}, "address[line2]": {p.Line2},
		"address[city]": {p.City}, "address[postal_code]": {p.PostalCode},
		"address[state]": {p.State}, "address[country]": {p.Country},
		"tax[validate_location]": {"immediately"},
	}
}

func (p Profile) matchesCustomer(c *stripe.Customer) bool {
	a := c.Address
	// Even a same-country shipping address has precedence over the invoice
	// address in Stripe Tax. Do not silently clear unexpected external data.
	return c.Shipping == nil && a != nil && a.Line1 == p.Line1 && a.Line2 == p.Line2 && a.City == p.City && a.PostalCode == p.PostalCode && a.State == p.State && a.Country == p.Country
}
