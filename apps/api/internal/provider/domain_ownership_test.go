package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/google/uuid"

	"hedvi.dev/api/internal/config"
)

type sesIdentityTransport struct {
	t      *testing.T
	owner  string
	env    string
	exists bool
	calls  []string
}

func (s *sesIdentityTransport) Do(req *http.Request) (*http.Response, error) {
	method, path := req.Method, req.URL.Path
	s.calls = append(s.calls, method+" "+path)
	status := http.StatusOK
	body := "{}"
	headers := http.Header{"Content-Type": []string{"application/json"}}
	switch {
	case method == "POST" && path == "/v2/email/tenants/get":
		var v struct{ TenantName string }
		if err := json.NewDecoder(req.Body).Decode(&v); err != nil {
			s.t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"Tenant": map[string]any{"TenantName": v.TenantName, "Tags": []map[string]string{{"Key": "environment", "Value": "production"}}}})
		body = string(b)
	case method == "GET":
		if !s.exists {
			status = http.StatusNotFound
			headers.Set("X-Amzn-Errortype", "NotFoundException")
			body = `{"message":"not found"}`
		} else {
			data, _ := json.Marshal(map[string]any{"Tags": []map[string]string{{"Key": DomainClaimTag, "Value": s.owner}, {"Key": "environment", "Value": s.env}}, "DkimAttributes": map[string]any{"Status": "SUCCESS", "Tokens": []string{"one", "two", "three"}}, "VerifiedForSendingStatus": true})
			body = string(data)
		}
	case method == "POST" && path == "/v2/email/identities":
		var v struct{ Tags []struct{ Key, Value string } }
		if err := json.NewDecoder(req.Body).Decode(&v); err != nil || len(v.Tags) != 2 || v.Tags[0].Key != DomainClaimTag || v.Tags[1].Key != "environment" || v.Tags[1].Value != "production" {
			s.t.Fatalf("missing ownership tag in CreateEmailIdentity: %v", err)
		}
		s.owner = v.Tags[0].Value
		s.env = v.Tags[1].Value
		s.exists = true
	case method == "DELETE":
		s.exists = false
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestSESDomainClaimOwnership(t *testing.T) {
	const domain = "example.test"
	const read = "GET /v2/email/identities/example.test"
	claimID := uuid.New()
	tenant, err := config.EnvProduction.ProjectTenant(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ owner, env string }{{"", "production"}, {uuid.NewString(), "production"}, {claimID.String(), "staging"}, {claimID.String(), ""}} {
		t.Run("existing_"+tc.owner+"_"+tc.env, func(t *testing.T) {
			client := &sesIdentityTransport{t: t, exists: true, owner: tc.owner, env: tc.env}
			ses := SES{Env: config.EnvProduction, Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: client})}
			_, err := ses.CreateIdentity(t.Context(), domain, "send."+domain, tenant, claimID)
			if !errors.Is(err, ErrIdentityOwnership) {
				t.Fatal(err)
			}
			_, err = ses.GetIdentity(t.Context(), domain, claimID)
			if !errors.Is(err, ErrIdentityOwnership) {
				t.Fatal(err)
			}
			if err = ses.DeleteIdentity(t.Context(), domain, claimID); !errors.Is(err, ErrIdentityOwnership) {
				t.Fatal(err)
			}
			if !slices.Equal(client.calls, []string{"POST /v2/email/tenants/get", read, read, read}) {
				t.Fatalf("unexpected requests: %v", client.calls)
			}
		})
	}
	client := &sesIdentityTransport{t: t}
	ses := SES{Env: config.EnvProduction, Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: client})}
	ident, err := ses.CreateIdentity(t.Context(), domain, "send."+domain, tenant, claimID)
	if err != nil || len(ident.DKIMTokens) != 3 || client.owner != claimID.String() {
		t.Fatalf("create own identity: %+v %v", ident, err)
	}
	client.calls = nil
	if _, err := ses.CreateIdentity(t.Context(), domain, "send."+domain, tenant, claimID); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(client.calls, "POST /v2/email/identities") {
		t.Fatal("a retry recreated an owned identity")
	}
	if err := ses.DeleteIdentity(context.Background(), domain, claimID); err != nil || client.exists {
		t.Fatalf("delete: %v", err)
	}
	if err := ses.DeleteIdentity(t.Context(), domain, claimID); err != nil {
		t.Fatalf("delete retry: %v", err)
	}
}
