package provider

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/google/uuid"
	"hedvi.dev/api/internal/config"
)

type tenantTransport struct {
	t                               *testing.T
	name, environment               string
	exists                          bool
	creates, associations, requests int
}

func (m *tenantTransport) Do(req *http.Request) (*http.Response, error) {
	m.t.Helper()
	m.requests++
	var v struct {
		TenantName, ResourceArn string
		Tags                    []struct{ Key, Value string }
	}
	if json.NewDecoder(req.Body).Decode(&v) != nil || v.TenantName != m.name {
		m.t.Fatal("unexpected tenant request")
	}
	status := 200
	data := map[string]any{}
	h := http.Header{"Content-Type": []string{"application/json"}}
	switch req.URL.Path {
	case "/v2/email/tenants/get":
		if m.exists {
			data["Tenant"] = map[string]any{"TenantName": m.name, "Tags": []map[string]string{{"Key": "environment", "Value": m.environment}}}
		} else {
			status = 404
			h.Set("X-Amzn-Errortype", "NotFoundException")
		}
	case "/v2/email/tenants":
		m.creates++
		if len(v.Tags) != 1 || v.Tags[0].Key != "environment" || v.Tags[0].Value != "production" {
			m.t.Fatal("tenant created without correct create-time tag")
		}
		m.exists = true
		m.environment = v.Tags[0].Value
	case "/v2/email/tenants/resources":
		m.associations++
	default:
		m.t.Fatal("unexpected SES operation", req.URL.Path)
	}
	b, _ := json.Marshal(data)
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
}

func TestSESTenantTagAndSendBinding(t *testing.T) {
	id := uuid.New()
	name, err := config.EnvProduction.ProjectTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := config.EnvStaging.ProjectTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	tr := &tenantTransport{t: t, name: name}
	s := SES{Env: config.EnvProduction, ConfigSet: "production-customer", AuthConfigSet: "production-auth", AuthTenant: "operator-hedvi-production-auth", Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr})}
	for _, bad := range []string{legacy, "operator-hedvi-production-auth", "hedvi-production-"} {
		if err := s.EnsureTenant(t.Context(), bad); !errors.Is(err, config.ErrProjectTenant) {
			t.Fatal(err)
		}
		if _, err := s.CreateIdentity(t.Context(), "example.test", "send.example.test", bad, uuid.New()); !errors.Is(err, config.ErrProjectTenant) {
			t.Fatal(err)
		}
	}
	for _, bad := range []SendInput{
		{TenantName: legacy, ConfigurationSet: s.ConfigSet, Tags: map[string]string{"hedvi_project": id.String()}},
		{TenantName: name, ConfigurationSet: s.AuthConfigSet, Tags: map[string]string{"hedvi_project": id.String()}},
		{TenantName: s.AuthTenant, ConfigurationSet: s.ConfigSet, Tags: map[string]string{"hedvi_auth": uuid.NewString()}},
		{TenantName: name, ConfigurationSet: s.AuthConfigSet, Tags: map[string]string{"hedvi_auth": uuid.NewString()}},
		{TenantName: s.AuthTenant, ConfigurationSet: s.AuthConfigSet, Tags: map[string]string{"hedvi_project": id.String()}},
	} {
		if _, err := s.Send(t.Context(), bad); Classify(err) != Rejected {
			t.Fatalf("improper tenant accepted: %v", err)
		}
	}
	if tr.requests != 0 {
		t.Fatal("foreign/invalid tenant reached SES")
	}
	for _, env := range []string{"", "staging"} {
		tr.exists, tr.environment = true, env
		if _, err = s.CreateIdentity(t.Context(), "example.test", "send.example.test", name, uuid.New()); !errors.Is(err, config.ErrProjectTenant) {
			t.Fatal("unowned tenant reached identity provisioning", err)
		}
		if err = s.EnsureTenant(t.Context(), name); !errors.Is(err, config.ErrProjectTenant) || tr.creates != 0 || tr.associations != 0 {
			t.Fatal("untagged/foreign tenant touched", err)
		}
	}
	tr.exists = false
	for range 2 {
		if err = s.EnsureTenant(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	if tr.creates != 1 || tr.associations != 2 {
		t.Fatal("failed safe creation/retry", tr.creates, tr.associations)
	}
	if !s.authorizesSend(SendInput{TenantName: name, ConfigurationSet: s.ConfigSet, Tags: map[string]string{"hedvi_project": id.String()}}) ||
		!s.authorizesSend(SendInput{TenantName: s.AuthTenant, ConfigurationSet: s.AuthConfigSet, Tags: map[string]string{"hedvi_auth": uuid.NewString()}}) {
		t.Fatal("valid sends denied")
	}
}
