package provider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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
