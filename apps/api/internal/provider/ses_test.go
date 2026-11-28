package provider

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/google/uuid"

	"hedvi.dev/api/internal/config"
)

func apiErr(code string, status int) error {
	return &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err: &smithy.GenericAPIError{Code: code, Message: code}}
}

func TestClassifySESErrors(t *testing.T) {
	cases := map[string]struct {
		err  error
		want Outcome
	}{
		"credential retrieval":                {&v4.SigningError{Err: errors.New("identity credentials unavailable")}, Retryable},
		"credential expiry":                   {apiErr("ExpiredToken", 403), Retryable},
		"access denied":                       {apiErr("AccessDeniedException", 403), Retryable},
		"signature":                           {apiErr("SignatureDoesNotMatch", 403), Retryable},
		"operator configuration missing":      {apiErr("NotFoundException", 404), Retryable},
		"unclassified authorization response": {&smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 403}}, Err: errors.New("access denied")}, Retryable},
		"request timeout response":            {apiErr("RequestTimeout", 408), Unknown},
		"throttle":                            {apiErr("TooManyRequestsException", 429), Retryable},
		"rejected":                            {apiErr("MessageRejected", 400), Rejected},
		"internal failure":                    {apiErr("InternalFailure", 500), Unknown},
		"unavailable":                         {apiErr("ServiceUnavailable", 503), Retryable},
		"timeout":                             {context.DeadlineExceeded, Unknown},
		"dial":                                {&net.OpError{Op: "dial", Err: errors.New("refused")}, Retryable},
		"read reset":                          {&net.OpError{Op: "read", Err: errors.New("reset")}, Unknown},
		"paused":                              {apiErr("SendingPausedException", 400), Retryable},
	}
	for name, c := range cases {
		if got := Classify(classifySES(c.err)); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

type sesHTTPFunc func(*http.Request) (*http.Response, error)

func (f sesHTTPFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func sesRefusal(code string, status int) *http.Response {
	h := make(http.Header)
	h.Set("X-Amzn-Errortype", code)
	h.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(`{"message":"synthetic refusal"}`))}
}

func TestSESSendIssuesOneRequest(t *testing.T) {
	projectID := uuid.New()
	tenant, err := config.EnvProduction.ProjectTenant(projectID)
	if err != nil {
		t.Fatal(err)
	}
	in := SendInput{TenantName: tenant, ConfigurationSet: "customer", From: "owner@example.test", Recipients: []string{"recipient@example.test"}, Raw: []byte("test"), Tags: map[string]string{"hedvi_project": projectID.String()}}
	for _, tc := range []struct {
		name string
		want Outcome
	}{{"access denied", Retryable}, {"lost response", Unknown}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			tr := sesHTTPFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Path != "/v2/email/outbound-emails" {
					t.Fatalf("unexpected send operation %s", req.URL.Path)
				}
				if tc.want == Unknown {
					return nil, errors.New("synthetic lost response")
				}
				return sesRefusal("AccessDeniedException", 403), nil
			})
			s := SES{Env: config.EnvProduction, ConfigSet: in.ConfigurationSet, Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr, Retryer: aws.NopRetryer{}})}
			_, err := s.Send(t.Context(), in)
			if err == nil || Classify(err) != tc.want || calls != 1 {
				t.Fatalf("SendEmail calls=%d, err=%v", calls, err)
			}
		})
	}
}

func TestSESFreshIdentityPropagation(t *testing.T) {
	const domain = "example.test"
	for _, tc := range []struct {
		name                         string
		existing, race, loseClaim    bool
		mailFromErrors, assocErrors  int
		code                         string
		status                       int
		timeout                      time.Duration
		wantMail, wantAssoc, wantGet int
		wantElapsed                  time.Duration
		wantOK                       bool
	}{
		{name: "both writes recover", mailFromErrors: 1, assocErrors: 1, code: "AccessDeniedException", status: 403, wantMail: 2, wantAssoc: 2, wantGet: 4, wantElapsed: 2 * time.Second, wantOK: true},
		{name: "permanent denial survives", mailFromErrors: 10, code: "AccessDenied", status: 403, wantMail: 4, wantGet: 5, wantElapsed: 7 * time.Second},
		{name: "writes share the eight second window", mailFromErrors: 3, assocErrors: 10, code: "AccessDenied", status: 403, wantMail: 4, wantAssoc: 1, wantGet: 5, wantElapsed: 7 * time.Second},
		{name: "preexisting owner", existing: true, mailFromErrors: 1, code: "AccessDeniedException", status: 403, wantMail: 1, wantGet: 1},
		{name: "already exists is not a fresh create", race: true, mailFromErrors: 1, code: "AccessDeniedException", status: 403, wantMail: 1, wantGet: 2},
		{name: "changed claim stops retry", loseClaim: true, mailFromErrors: 1, code: "AccessDeniedException", status: 403, wantMail: 1, wantGet: 3, wantElapsed: time.Second},
		{name: "expired credentials", mailFromErrors: 1, code: "ExpiredToken", status: 403, wantMail: 1, wantGet: 2},
		{name: "lost association response", assocErrors: 1, wantMail: 1, wantAssoc: 1, wantGet: 2},
		{name: "server failure", mailFromErrors: 1, code: "InternalFailure", status: 500, wantMail: 1, wantGet: 2},
		{name: "deadline interrupts backoff", mailFromErrors: 10, code: "AccessDenied", status: 403, timeout: 250 * time.Millisecond, wantMail: 1, wantGet: 2, wantElapsed: 250 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				claimID := uuid.New()
				tenant, err := config.EnvProduction.ProjectTenant(uuid.New())
				if err != nil {
					t.Fatal(err)
				}
				base := &sesIdentityTransport{t: t, exists: tc.existing, owner: claimID.String(), env: "production"}
				var mail, assoc, get, create int
				lost := errors.New("synthetic lost response")
				tr := sesHTTPFunc(func(req *http.Request) (*http.Response, error) {
					switch {
					case req.Method == http.MethodGet:
						get++
					case req.URL.Path == "/v2/email/identities":
						create++
						if tc.race {
							base.exists = true
							return sesRefusal("AlreadyExistsException", 400), nil
						}
					case strings.HasSuffix(req.URL.Path, "/mail-from"):
						mail++
						if mail <= tc.mailFromErrors {
							if tc.loseClaim {
								base.owner = uuid.NewString()
							}
							return sesRefusal(tc.code, tc.status), nil
						}
					case req.URL.Path == "/v2/email/tenants/resources":
						assoc++
						if assoc <= tc.assocErrors {
							if tc.status == 0 {
								return nil, lost
							}
							return sesRefusal(tc.code, tc.status), nil
						}
					}
					return base.Do(req)
				})
				s := SES{Env: config.EnvProduction, Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr, Retryer: aws.NopRetryer{}})}
				ctx := t.Context()
				if tc.timeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.timeout)
					defer cancel()
				}
				start := time.Now()
				id, err := s.CreateIdentity(ctx, domain, "send."+domain, tenant, claimID)
				if (err == nil) != tc.wantOK || (len(id.DKIMTokens) == 3) != tc.wantOK {
					t.Fatalf("unexpected provisioning result: %+v %v", id, err)
				}
				switch {
				case tc.loseClaim:
					if !errors.Is(err, ErrIdentityOwnership) {
						t.Fatal(err)
					}
				case tc.timeout > 0:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
				case tc.status == 0:
					if !errors.Is(err, lost) {
						t.Fatal(err)
					}
				case !tc.wantOK:
					var refusal smithy.APIError
					if !errors.As(err, &refusal) || refusal.ErrorCode() != tc.code {
						t.Fatalf("original refusal was lost: %v", err)
					}
				}
				if mail != tc.wantMail || assoc != tc.wantAssoc || get != tc.wantGet || (!tc.existing && create != 1) || (tc.existing && create != 0) || time.Since(start) != tc.wantElapsed {
					t.Fatalf("writes=%d associations=%d ownership reads=%d creates=%d elapsed=%s", mail, assoc, get, create, time.Since(start))
				}
			})
		})
	}
}
