package billing

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responseTransport func(*http.Request) (*http.Response, error)

func (f responseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCustomerLocationFailureRequiresDefinitiveResponse(t *testing.T) {
	const valid = `{"error":{"type":"invalid_request_error","code":"customer_tax_location_invalid"}}`
	for _, tt := range []struct {
		name, body, path string
		status           int
		retry, editable  bool
	}{
		{"validation", valid, "/customers", 400, false, true},
		{"truncated", `{"error":{"type":"invalid_request_error","code":"customer_tax_location_invalid"}`, "/customers", 400, false, false},
		{"partial decode", `{"error":{"type":"invalid_request_error","code":"customer_tax_location_invalid"},"error":false}`, "/customers", 400, false, false},
		{"unknown type", `{"error":{"type":"api_error","code":"customer_tax_location_invalid"}}`, "/customers", 400, false, false},
		{"retry instruction", valid, "/customers", 400, true, false},
		{"server error", valid, "/customers", 500, false, false},
		{"other operation", valid, "/checkout/sessions", 400, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := responseTransport(func(*http.Request) (*http.Response, error) {
				h := make(http.Header)
				if tt.retry {
					h.Set("Stripe-Should-Retry", "true")
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader(tt.body)), Header: h}, nil
			})
			s := &Service{base: "https://api.stripe.com/v1", http: &http.Client{Transport: transport}}
			err := s.call(context.Background(), http.MethodPost, tt.path, nil, "attempt", new(any))
			if errors.Is(err, errCustomerLocation) != tt.editable {
				t.Fatalf("editable=%v, error=%v", tt.editable, err)
			}
		})
	}
}
