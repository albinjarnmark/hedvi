package config

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func productionEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"HEDVI_ENV": "production", "HEDVI_AWS_MODE": "aws", "HEDVI_MOCK_DNS": "false", "HEDVI_COOKIE_SECURE": "true",
		"HEDVI_APP_URL": "https://app.example.com", "HEDVI_PUBLIC_API_URL": "https://api.example.com", "HEDVI_ALLOWED_ORIGINS": "https://app.example.com",
		"HEDVI_DATA_KEY": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "HEDVI_API_KEY_PEPPER": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		"HEDVI_S3_BUCKET": "example-payloads", "HEDVI_S3_KMS_KEY_ID": "example-key", "HEDVI_SQS_SEND_QUEUE_URL": "https://sqs.example.com/send", "HEDVI_SQS_EVENTS_QUEUE_URL": "https://sqs.example.com/events", "HEDVI_SQS_WEBHOOK_QUEUE_URL": "https://sqs.example.com/webhooks",
		"HEDVI_AUTH_MAIL_FROM": "accounts@auth.example.com", "HEDVI_AUTH_MAIL_CONFIGURATION_SET": "operator-auth-events", "HEDVI_AUTH_MAIL_TENANT": "operator-auth",
		"HEDVI_AWS_ACCOUNT_ID": "123456789012", "HEDVI_SES_CONFIGURATION_SET": "sending-events",
		"HEDVI_DATABASE_URL_API":    "postgres://hedvi_api:example-test-password@database.example.com/hedvi?sslmode=verify-full",
		"HEDVI_DATABASE_URL_WORKER": "", "HEDVI_DATABASE_URL_MIGRATE": "",
		"HEDVI_VALKEY_URL": "rediss://default:example-test-password@cache.example.com:6379/0",
	} {
		t.Setenv(name, value)
	}
}
func TestDeployedConfigurationRejectsPublicSecretsAndUnsafeOrigins(t *testing.T) {
	productionEnv(t)
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, value string }{
		{"HEDVI_DATA_KEY", devDataKey}, {"HEDVI_API_KEY_PEPPER", devPepper},
		{"HEDVI_DATA_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 33))},
		{"HEDVI_API_KEY_PEPPER", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))},
		{"HEDVI_APP_URL", "http://app.example.com"}, {"HEDVI_PUBLIC_API_URL", "https://user:secret@api.example.com"},
		{"HEDVI_ALLOWED_ORIGINS", "https://app.example.com/path"}, {"HEDVI_ALLOWED_ORIGINS", "*"},
		{"HEDVI_AWS_ACCOUNT_ID", "bad"}, {"HEDVI_SES_CONFIGURATION_SET", "hedvi-local"},
		{"HEDVI_MAX_QUEUE_TIME", "0s"}, {"HEDVI_OPS_ALERT_WEBHOOK_URL", ":bad-url"},
		{"HEDVI_TRUSTED_PROXIES", "0.0.0.0/0"}, {"HEDVI_TRUSTED_PROXIES", "garbage"},
		{"HEDVI_DATABASE_URL_API", "postgres://hedvi_api:hedvi_api_dev@database.example.com/hedvi?sslmode=verify-full"},
		{"HEDVI_DATABASE_URL_API", "postgres://hedvi_api:example-test-password@database.example.com/hedvi?sslmode=disable"},
		{"HEDVI_VALKEY_URL", "rediss://cache.example.com:6379/0"},
		{"HEDVI_VALKEY_URL", "redis://default:example-test-password@cache.example.com:6379/0"},
		{"HEDVI_PROXY_ATTESTATION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))},
	}
	for _, c := range cases {
		t.Run(c.name+"/rejected", func(t *testing.T) {
			t.Setenv(c.name, c.value)
			if _, err := Load(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestRuntimeCredentialsAreIsolatedByCommand(t *testing.T) {
	productionEnv(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateForCommand("api"); err != nil {
		t.Fatal(err)
	}
	c.DatabaseURLWorker = "postgres://worker:secret@example.com/db?sslmode=verify-full"
	if err := c.ValidateForCommand("api"); err == nil {
		t.Fatal("API accepted worker credentials")
	}
	c.DatabaseURLAPI = ""
	if err := c.ValidateForCommand("worker"); err != nil {
		t.Fatal(err)
	}
	c.DatabaseURLMigrate = "postgres://owner:secret@example.com/db?sslmode=verify-full"
	if err := c.ValidateForCommand("worker"); err == nil {
		t.Fatal("worker accepted owner credentials")
	}
	c.DatabaseURLWorker = ""
	c.ValkeyURL = ""
	if err := c.ValidateForCommand("migrate"); err != nil {
		t.Fatal(err)
	}
	c.DatabaseURLMigrate = ""
	c.DatabaseURLAPI = "postgres://api:secret@example.com/db?sslmode=verify-full"
	if err := c.ValidateForCommand("api"); err == nil {
		t.Fatal("API started without a configured cache")
	}
}

func TestPrivateTransportExceptionsCannotApplyToPublicHosts(t *testing.T) {
	productionEnv(t)
	t.Setenv("HEDVI_DATABASE_ALLOW_PRIVATE_CA", "true")
	t.Setenv("HEDVI_VALKEY_ALLOW_PRIVATE_PLAINTEXT", "true")
	t.Setenv("HEDVI_DATABASE_URL_API", "postgres://hedvi_api:example-test-password@postgres.railway.internal/hedvi?sslmode=require")
	t.Setenv("HEDVI_VALKEY_URL", "redis://default:example-test-password@valkey.railway.internal:6379/0")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, value string }{
		{"HEDVI_DATABASE_URL_API", "postgres://hedvi_api:example-test-password@database.example.com/hedvi?sslmode=require"},
		{"HEDVI_DATABASE_URL_API", "postgres://hedvi_api:example-test-password@postgres.railway.internal/hedvi?sslmode=disable"},
		{"HEDVI_VALKEY_URL", "redis://default:example-test-password@cache.example.com:6379/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := Load(); err == nil {
				t.Fatal("private exception accepted unsafe public/insecure URL")
			}
		})
	}
}

func TestAWSCannotUseImplicitLocalSecurityDefaults(t *testing.T) {
	t.Setenv("HEDVI_ENV", "local")
	t.Setenv("HEDVI_AWS_MODE", "aws")
	if _, err := Load(); err == nil {
		t.Fatal("real AWS accepted public development defaults")
	}
}
func TestWorkerIdentityUniqueAcrossReplicasAndRestarts(t *testing.T) {
	t.Setenv("HEDVI_ENV", "local")
	t.Setenv("HEDVI_AWS_MODE", "mock")
	t.Setenv("HEDVI_WORKER_ID", "replica")
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkerID == second.WorkerID {
		t.Fatal("different processes reused one lease identity")
	}
}
