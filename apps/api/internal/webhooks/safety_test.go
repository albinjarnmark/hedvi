package webhooks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	"hedvi.dev/api/internal/netsafe"
)

func TestTransportErrorsOmitDestinationAndResolverDetails(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{fmt.Errorf("%w: 10.1.2.3", netsafe.ErrBlocked), "destination address is not allowed"},
		{&net.DNSError{Err: "lookup failure", Name: "sensitive.host", Server: "10.1.2.3:53"}, "destination DNS lookup failed"},
		{context.DeadlineExceeded, "connection timed out"},
		{errors.New("certificate error on confidential.example"), "request failed"},
	} {
		input := &url.Error{Op: "Post", URL: "https://secret.example/?token=fixture", Err: tc.cause}
		if got := redactErr(input).Error(); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}
