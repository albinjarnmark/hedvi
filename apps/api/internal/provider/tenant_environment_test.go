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
	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"hedvi.dev/api/internal/config"
)

type tenantTransport struct {
	t                              *testing.T
	name, environment              string
	exists                         bool
	createAttempts, creates, reads int
	associations, requests         int
	createError, readError         string
	loseCreateResponse             bool
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
	refuse := func(code string, httpStatus int) {
		status = httpStatus
		h.Set("X-Amzn-Errortype", code)
		data["message"] = "synthetic tenant refusal"
	}
	switch req.URL.Path {
	case "/v2/email/tenants/get":
		m.reads++
		if m.readError != "" {
			refuse(m.readError, http.StatusForbidden)
		} else if m.exists {
			data["Tenant"] = map[string]any{"TenantName": m.name, "Tags": []map[string]string{{"Key": "environment", "Value": m.environment}}}
		} else {
			// SES evaluates this missing resource against "*", which the
			// deployed resource-scoped policy does not authorize.
			refuse("AccessDeniedException", http.StatusForbidden)
		}
	case "/v2/email/tenants":
		m.createAttempts++
		if len(v.Tags) != 1 || v.Tags[0].Key != "environment" || v.Tags[0].Value != "production" {
			m.t.Fatal("tenant created without correct create-time tag")
		}
		if m.createError != "" {
			refuse(m.createError, http.StatusForbidden)
		} else if m.exists {
			// Create is not an upsert: an existing foreign/untagged tenant
			// keeps its original tags even if different tags were requested.
			refuse("AlreadyExistsException", http.StatusBadRequest)
		} else {
			m.creates++
			m.exists = true
			m.environment = v.Tags[0].Value
			if m.loseCreateResponse {
				m.loseCreateResponse = false
				return nil, errors.New("tenant created but response lost")
			}
		}
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
	s := SES{Env: config.EnvProduction, ConfigSet: "production-customer", AuthConfigSet: "production-auth", AuthTenant: "operator-hedvi-production-auth", Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr, Retryer: aws.NopRetryer{}})}
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
		if err = s.EnsureTenant(t.Context(), name); !errors.Is(err, config.ErrProjectTenant) || tr.creates != 0 || tr.associations != 0 || tr.environment != env {
			t.Fatal("untagged/foreign tenant touched", err)
		}
	}
	tr.exists = false
	var denied smithy.APIError
	if err = s.checkTenant(t.Context(), name); !errors.As(err, &denied) || denied.ErrorCode() != "AccessDeniedException" {
		t.Fatal("missing tenant must reproduce the scoped-policy 403", err)
	}
	attempts, reads := tr.createAttempts, tr.reads
	for range 2 {
		if err = s.EnsureTenant(t.Context(), name); err != nil {
			t.Fatal(err)
		}
	}
	if tr.creates != 1 || tr.associations != 2 || tr.createAttempts != attempts+2 || tr.reads != reads+2 || tr.environment != "production" {
		t.Fatal("failed safe creation/AlreadyExists retry", tr.creates, tr.associations)
	}
	if !s.authorizesSend(SendInput{TenantName: name, ConfigurationSet: s.ConfigSet, Tags: map[string]string{"hedvi_project": id.String()}}) ||
		!s.authorizesSend(SendInput{TenantName: s.AuthTenant, ConfigurationSet: s.AuthConfigSet, Tags: map[string]string{"hedvi_auth": uuid.NewString()}}) {
		t.Fatal("valid sends denied")
	}
}

func TestSESTenantCreationAndReadFailuresDoNotAssociate(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		exists                 bool
		createError, readError string
		wantCreates, wantReads int
	}{
		{name: "creation_denied", createError: "AccessDeniedException"},
		{name: "existing_creation_denied", exists: true, createError: "AccessDeniedException"},
		{name: "read_denied_after_creation", readError: "AccessDeniedException", wantCreates: 1, wantReads: 1},
		{name: "existing_read_denied", exists: true, readError: "AccessDeniedException", wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, err := config.EnvProduction.ProjectTenant(uuid.New())
			if err != nil {
				t.Fatal(err)
			}
			tr := &tenantTransport{t: t, name: name, exists: tc.exists, environment: "production", createError: tc.createError, readError: tc.readError}
			s := SES{Env: config.EnvProduction, ConfigSet: "production-customer", Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr, Retryer: aws.NopRetryer{}})}
			var refusal smithy.APIError
			if err = s.EnsureTenant(t.Context(), name); !errors.As(err, &refusal) || refusal.ErrorCode() != "AccessDeniedException" {
				t.Fatal("provider refusal was not preserved", err)
			}
			if tr.createAttempts != 1 || tr.creates != tc.wantCreates || tr.reads != tc.wantReads || tr.associations != 0 {
				t.Fatalf("refused provisioning continued: attempts=%d created=%d reads=%d associations=%d", tr.createAttempts, tr.creates, tr.reads, tr.associations)
			}
		})
	}
}

func TestSESTenantLostCreateResponseRecoversWithoutRetagging(t *testing.T) {
	name, err := config.EnvProduction.ProjectTenant(uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	tr := &tenantTransport{t: t, name: name, loseCreateResponse: true}
	s := SES{Env: config.EnvProduction, ConfigSet: "production-customer", Client: sesv2.New(sesv2.Options{Credentials: aws.AnonymousCredentials{}, Region: "eu-north-1", HTTPClient: tr, Retryer: aws.NopRetryer{}})}
	if err := s.EnsureTenant(t.Context(), name); err == nil || tr.creates != 1 || tr.reads != 0 || tr.associations != 0 {
		t.Fatal("uncertain creation was treated as verified", err)
	}
	if err := s.EnsureTenant(t.Context(), name); err != nil {
		t.Fatal("AlreadyExists recovery failed", err)
	}
	if tr.createAttempts != 2 || tr.creates != 1 || tr.reads != 1 || tr.associations != 1 || tr.environment != "production" {
		t.Fatal("retry duplicated creation or skipped the ownership read")
	}
}
