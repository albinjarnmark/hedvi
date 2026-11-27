// Package config loads process configuration from the environment.
//
// Every value has a safe local default so `make dev` works without a .env
// file. Production refuses to start with local defaults (see Validate).
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/secure"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Env string

const (
	EnvLocal      Env = "local"
	EnvTest       Env = "test"
	EnvStaging    Env = "staging"
	EnvProduction Env = "production"
)

type Config struct {
	Env Env

	APIAddr      string
	MetricsAddr  string
	PublicAPIURL string
	AppURL       string
	LegalOrigin  string
	// AllowedOrigins for console requests; defaults to AppURL.
	AllowedOrigins      []string
	TrustedProxies      []netip.Prefix
	ProxyAttestationKey []byte

	DatabaseURLAPI              string
	DatabaseURLWorker           string
	DatabaseURLMigrate          string
	ValkeyURL                   string
	DatabaseAllowPrivateCA      bool
	ValkeyAllowPrivatePlaintext bool

	AWSMode           string // "mock" or "aws"
	AWSRegion         string
	AWSAccountID      string
	S3Bucket          string
	S3KMSKeyID        string
	SendQueueURL      string
	EventsQueueURL    string
	WebhookQueueURL   string
	AuthMailFrom      string
	AuthMailTenant    string
	AuthMailConfigSet string
	SESConfigSet      string
	SESMailFromMX     string
	BlobDir           string
	MockDNS           bool
	MockSESFault      string
	DNSServer         string

	// DataKey encrypts payloads, webhook secrets and TOTP secrets (AES-256-GCM).
	DataKey []byte
	// APIKeyPepper is the HMAC key for API key and session token hashes.
	APIKeyPepper []byte

	CookieSecure        bool
	WebhookAllowPrivate bool
	WebhookAllowHTTP    bool
	OpsAlertWebhookURL  string
	OpsAlertSNSTopicARN string
	WorkerID            string

	MaxQueueTime time.Duration
}

// Local development defaults. These are public and must never be used outside
// local/test; Validate enforces that.
const (
	devDataKey = "aGVkdmktbG9jYWwtZGV2LWRhdGEta2V5LTMyLWJ5dGU=" // "hedvi-local-dev-data-key-32-byte"
	devPepper  = "aGVkdmktbG9jYWwtZGV2LXBlcHBlcg=="             // "hedvi-local-dev-pepper"
)

func Load() (*Config, error) {
	c := &Config{
		Env:                         Env(get("HEDVI_ENV", "local")),
		APIAddr:                     get("HEDVI_API_ADDR", "127.0.0.1:8780"),
		MetricsAddr:                 get("HEDVI_METRICS_ADDR", ""),
		PublicAPIURL:                get("HEDVI_PUBLIC_API_URL", "http://127.0.0.1:8780"),
		AppURL:                      get("HEDVI_APP_URL", "http://localhost:3030"),
		DatabaseURLAPI:              get("HEDVI_DATABASE_URL_API", "postgres://hedvi_api:hedvi_api_dev@127.0.0.1:55432/hedvi?sslmode=disable"),
		DatabaseURLWorker:           get("HEDVI_DATABASE_URL_WORKER", "postgres://hedvi_worker:hedvi_worker_dev@127.0.0.1:55432/hedvi?sslmode=disable"),
		DatabaseURLMigrate:          get("HEDVI_DATABASE_URL_MIGRATE", "postgres://hedvi_owner:hedvi_owner_dev@127.0.0.1:55432/hedvi?sslmode=disable"),
		ValkeyURL:                   get("HEDVI_VALKEY_URL", "redis://127.0.0.1:56379/0"),
		DatabaseAllowPrivateCA:      getBool("HEDVI_DATABASE_ALLOW_PRIVATE_CA", false),
		ValkeyAllowPrivatePlaintext: getBool("HEDVI_VALKEY_ALLOW_PRIVATE_PLAINTEXT", false),
		AWSMode:                     get("HEDVI_AWS_MODE", "mock"),
		AWSRegion:                   get("HEDVI_AWS_REGION", "eu-north-1"),
		AWSAccountID:                get("HEDVI_AWS_ACCOUNT_ID", ""),
		S3Bucket:                    get("HEDVI_S3_BUCKET", ""),
		S3KMSKeyID:                  get("HEDVI_S3_KMS_KEY_ID", ""),
		SendQueueURL:                get("HEDVI_SQS_SEND_QUEUE_URL", ""),
		EventsQueueURL:              get("HEDVI_SQS_EVENTS_QUEUE_URL", ""),
		WebhookQueueURL:             get("HEDVI_SQS_WEBHOOK_QUEUE_URL", ""),
		AuthMailFrom:                get("HEDVI_AUTH_MAIL_FROM", "accounts@auth.example.test"),
		AuthMailTenant:              get("HEDVI_AUTH_MAIL_TENANT", "hedvi-system-auth"),
		AuthMailConfigSet:           get("HEDVI_AUTH_MAIL_CONFIGURATION_SET", "hedvi-local-auth"),
		SESConfigSet:                get("HEDVI_SES_CONFIGURATION_SET", "hedvi-local"),
		SESMailFromMX:               get("HEDVI_SES_MAIL_FROM_MX", "feedback-smtp.eu-north-1.amazonses.com"),
		BlobDir:                     get("HEDVI_BLOB_DIR", ".data/blobs"),
		MockDNS:                     getBool("HEDVI_MOCK_DNS", true),
		MockSESFault:                get("HEDVI_MOCK_SES_FAULT", ""),
		DNSServer:                   get("HEDVI_DNS_SERVER", ""),
		CookieSecure:                getBool("HEDVI_COOKIE_SECURE", false),
		WebhookAllowPrivate:         getBool("HEDVI_WEBHOOK_ALLOW_PRIVATE", false),
		WebhookAllowHTTP:            getBool("HEDVI_WEBHOOK_ALLOW_HTTP", false),
		OpsAlertWebhookURL:          get("HEDVI_OPS_ALERT_WEBHOOK_URL", ""),
		OpsAlertSNSTopicARN:         get("HEDVI_OPS_ALERT_SNS_TOPIC_ARN", ""),
		WorkerID:                    get("HEDVI_WORKER_ID", hostnameOr("worker")) + "-" + secure.RandomToken(10),
	}
	if c.IsDeployed() {
		// Each process receives only the database credential its command needs.
		c.DatabaseURLAPI = strings.TrimSpace(os.Getenv("HEDVI_DATABASE_URL_API"))
		c.DatabaseURLWorker = strings.TrimSpace(os.Getenv("HEDVI_DATABASE_URL_WORKER"))
		c.DatabaseURLMigrate = strings.TrimSpace(os.Getenv("HEDVI_DATABASE_URL_MIGRATE"))
		c.ValkeyURL = strings.TrimSpace(os.Getenv("HEDVI_VALKEY_URL"))
		c.AuthMailFrom = strings.TrimSpace(os.Getenv("HEDVI_AUTH_MAIL_FROM"))
		c.AuthMailTenant = strings.TrimSpace(os.Getenv("HEDVI_AUTH_MAIL_TENANT"))
		c.AuthMailConfigSet = strings.TrimSpace(os.Getenv("HEDVI_AUTH_MAIL_CONFIGURATION_SET"))
	}
	for _, raw := range strings.Split(get("HEDVI_TRUSTED_PROXIES", ""), ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, errors.New("HEDVI_TRUSTED_PROXIES must contain explicit non-universal IPv4/IPv6 CIDRs")
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}
	var err error
	if raw := get("HEDVI_PROXY_ATTESTATION_KEY", ""); raw != "" {
		c.ProxyAttestationKey, err = base64.StdEncoding.DecodeString(raw)
		if err != nil || len(c.ProxyAttestationKey) != 32 {
			return nil, errors.New("HEDVI_PROXY_ATTESTATION_KEY must be base64 encoding of a fresh 32-byte secret")
		}
	}
	if c.DataKey, err = decodeKey("HEDVI_DATA_KEY", devDataKey, 32); err != nil {
		return nil, err
	}
	if c.APIKeyPepper, err = decodeKey("HEDVI_API_KEY_PEPPER", devPepper, 16); err != nil {
		return nil, err
	}
	if c.MaxQueueTime, err = time.ParseDuration(get("HEDVI_MAX_QUEUE_TIME", "72h")); err != nil {
		return nil, fmt.Errorf("HEDVI_MAX_QUEUE_TIME: %w", err)
	}
	if prefix, set := os.LookupEnv("HEDVI_SES_TENANT_PREFIX"); set && prefix != c.Env.CustomerTenantPrefix() {
		return nil, errors.New("HEDVI_SES_TENANT_PREFIX must match the customer namespace for HEDVI_ENV")
	}
	c.LegalOrigin = strings.TrimRight(get("HEDVI_LEGAL_ORIGIN", c.AppURL), "/")
	for _, o := range strings.Split(get("HEDVI_ALLOWED_ORIGINS", c.AppURL), ",") {
		if o = strings.TrimSuffix(strings.TrimSpace(o), "/"); o != "" {
			c.AllowedOrigins = append(c.AllowedOrigins, o)
		}
	}
	return c, c.Validate()
}

// Validate refuses unsafe combinations outside local development.
func (c *Config) Validate() error {
	switch c.Env {
	case EnvLocal, EnvTest, EnvStaging, EnvProduction:
	default:
		return fmt.Errorf("HEDVI_ENV must be local, test, staging or production, got %q", c.Env)
	}
	if c.AWSMode != "mock" && c.AWSMode != "aws" {
		return fmt.Errorf("HEDVI_AWS_MODE must be mock or aws, got %q", c.AWSMode)
	}
	if !c.IsDeployed() {
		if c.AWSMode == "aws" {
			return errors.New("real AWS access requires HEDVI_ENV=staging or production and deployment security settings")
		}
		return nil
	}
	var errs []error
	if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(c.AWSAccountID) {
		errs = append(errs, errors.New("HEDVI_AWS_ACCOUNT_ID must be a 12-digit account identifier"))
	}
	if c.SESConfigSet == "" || c.SESConfigSet == "hedvi-local" {
		errs = append(errs, errors.New("HEDVI_SES_CONFIGURATION_SET must name the deployed configuration set"))
	}
	if c.MaxQueueTime <= 0 || c.MaxQueueTime > 7*24*time.Hour {
		errs = append(errs, errors.New("HEDVI_MAX_QUEUE_TIME must be positive and at most 168h"))
	}
	for name, raw := range map[string]string{"HEDVI_DATABASE_URL_API": c.DatabaseURLAPI, "HEDVI_DATABASE_URL_WORKER": c.DatabaseURLWorker, "HEDVI_DATABASE_URL_MIGRATE": c.DatabaseURLMigrate} {
		if raw != "" {
			if err := c.validateDatabaseURL(raw); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
	}
	if c.ValkeyURL != "" {
		if err := c.validateValkeyURL(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.OpsAlertWebhookURL != "" {
		u, err := url.Parse(c.OpsAlertWebhookURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			errs = append(errs, errors.New("HEDVI_OPS_ALERT_WEBHOOK_URL must be an HTTPS URL without credentials or fragment"))
		}
	}
	if c.AWSMode != "aws" {
		errs = append(errs, errors.New("deployed environments require HEDVI_AWS_MODE=aws"))
	}
	if c.MockDNS {
		errs = append(errs, errors.New("deployed environments require HEDVI_MOCK_DNS=false"))
	}
	if os.Getenv("HEDVI_DATA_KEY") == "" || os.Getenv("HEDVI_API_KEY_PEPPER") == "" {
		errs = append(errs, errors.New("HEDVI_DATA_KEY and HEDVI_API_KEY_PEPPER must be set explicitly"))
	}
	localData, _ := base64.StdEncoding.DecodeString(devDataKey)
	localPepper, _ := base64.StdEncoding.DecodeString(devPepper)
	if len(c.DataKey) != 32 || bytes.Equal(c.DataKey, localData) || len(c.APIKeyPepper) < 32 || bytes.Equal(c.APIKeyPepper, localPepper) || bytes.Equal(c.DataKey, c.APIKeyPepper) {
		errs = append(errs, errors.New("deployed encryption and pepper keys must be distinct, fresh 32-byte secrets; development keys are forbidden"))
	}
	if len(c.ProxyAttestationKey) > 0 && (len(c.ProxyAttestationKey) != 32 || bytes.Equal(c.ProxyAttestationKey, c.DataKey) || bytes.Equal(c.ProxyAttestationKey, c.APIKeyPepper) || bytes.Equal(c.ProxyAttestationKey, make([]byte, 32))) {
		errs = append(errs, errors.New("proxy attestation key must be a distinct fresh 32-byte secret"))
	}
	for name, raw := range map[string]string{"HEDVI_APP_URL": c.AppURL, "HEDVI_PUBLIC_API_URL": c.PublicAPIURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			errs = append(errs, fmt.Errorf("%s must be a public HTTPS URL without credentials", name))
		}
	}
	for _, origin := range c.AllowedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, errors.New("deployed console origins must be explicit HTTPS origins"))
		}
	}
	if !c.CookieSecure {
		errs = append(errs, errors.New("deployed environments require HEDVI_COOKIE_SECURE=true"))
	}
	if c.WebhookAllowPrivate || c.WebhookAllowHTTP {
		errs = append(errs, errors.New("webhook SSRF relaxations are only allowed locally"))
	}
	for name, v := range map[string]string{
		"HEDVI_S3_BUCKET": c.S3Bucket, "HEDVI_S3_KMS_KEY_ID": c.S3KMSKeyID,
		"HEDVI_SQS_SEND_QUEUE_URL": c.SendQueueURL, "HEDVI_SQS_EVENTS_QUEUE_URL": c.EventsQueueURL,
		"HEDVI_SQS_WEBHOOK_QUEUE_URL": c.WebhookQueueURL,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	return errors.Join(errs...)
}

func railwayPrivateHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), ".railway.internal") && len(host) > len(".railway.internal")
}

func (c *Config) validateDatabaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.User == nil || u.User.Username() == "" {
		return errors.New("must be a PostgreSQL URL with explicit credentials")
	}
	password, ok := u.User.Password()
	if !ok || password == "" || strings.HasSuffix(password, "_dev") {
		return errors.New("must use a non-development database password")
	}
	query := u.Query()
	for _, override := range []string{"host", "hostaddr", "port", "user", "password", "dbname", "service", "servicefile"} {
		if query.Has(override) {
			return errors.New("database destination and credentials must not be overridden in URL query parameters")
		}
	}
	if len(query["sslmode"]) != 1 {
		return errors.New("database URL must set exactly one sslmode")
	}
	if u.Query().Get("sslmode") != "verify-full" {
		if !c.DatabaseAllowPrivateCA || !railwayPrivateHost(u.Hostname()) || u.Query().Get("sslmode") != "require" {
			return errors.New("require sslmode=verify-full; private Railway CA exception needs explicit opt-in and sslmode=require")
		}
	}
	return nil
}

func (c *Config) validateValkeyURL() error {
	u, err := url.Parse(c.ValkeyURL)
	if err != nil || u.Hostname() == "" || u.User == nil {
		return errors.New("HEDVI_VALKEY_URL requires an authenticated TLS URL")
	}
	password, ok := u.User.Password()
	if !ok || password == "" || strings.HasSuffix(password, "_dev") {
		return errors.New("HEDVI_VALKEY_URL requires a non-development password")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("HEDVI_VALKEY_URL must not contain transport overrides or fragments")
	}
	if u.Scheme != "rediss" && (!c.ValkeyAllowPrivatePlaintext || u.Scheme != "redis" || !railwayPrivateHost(u.Hostname())) {
		return errors.New("HEDVI_VALKEY_URL requires rediss; private Railway transport requires explicit opt-in")
	}
	return nil
}

// AuthDomain is reserved for operator mail, including all child domains.
func (c *Config) AuthDomain() string {
	a, err := address.Parse(c.AuthMailFrom)
	if err != nil {
		return ""
	}
	return a.Domain
}
func (c *Config) LegalURLOrigin() string {
	if c.LegalOrigin != "" {
		return strings.TrimRight(c.LegalOrigin, "/")
	}
	return strings.TrimRight(c.AppURL, "/")
}

func (c *Config) validateAuthMail() error {
	a, err := address.Parse(c.AuthMailFrom)
	if err != nil || a.Name != "" || a.Normalized != c.AuthMailFrom || strings.Count(a.Domain, ".") < 2 || strings.HasSuffix(a.Domain, ".test") || strings.HasSuffix(a.Domain, ".invalid") {
		return errors.New("HEDVI_AUTH_MAIL_FROM must be one normalized bare address on a dedicated operator-owned subdomain")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{2,63}$`).MatchString(c.AuthMailTenant) || strings.HasPrefix(c.AuthMailTenant, "hedvi-") {
		return errors.New("HEDVI_AUTH_MAIL_TENANT must name a dedicated operator tenant, never a generated customer project tenant")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(c.AuthMailConfigSet) || c.AuthMailConfigSet == c.SESConfigSet || strings.HasPrefix(c.AuthMailConfigSet, "hedvi-local") {
		return errors.New("HEDVI_AUTH_MAIL_CONFIGURATION_SET must be a dedicated deployed configuration set with provider bounce/complaint suppression")
	}
	for name, origin := range map[string]string{"HEDVI_APP_URL": c.AppURL, "HEDVI_LEGAL_ORIGIN": c.LegalURLOrigin()} {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("%s must be an exact HTTPS origin without credentials, path, query or fragment", name)
		}
	}
	return nil
}

// ValidateForCommand prevents accidentally provisioning powerful credentials to
// the public API. Local mock mode needs both pools for its in-process simulator.
func (c *Config) ValidateForCommand(command string) error {
	if !c.IsDeployed() {
		return nil
	}
	if command == "api" || command == "worker" {
		if err := c.validateAuthMail(); err != nil {
			return err
		}
	}
	var required string
	switch command {
	case "api":
		required = c.DatabaseURLAPI
		if c.DatabaseURLWorker != "" || c.DatabaseURLMigrate != "" {
			return errors.New("API process must not receive worker or migration database credentials")
		}
	case "worker", "admin":
		required = c.DatabaseURLWorker
		if c.DatabaseURLAPI != "" || c.DatabaseURLMigrate != "" {
			return errors.New("worker/admin process must receive only the worker database credential")
		}
	case "migrate":
		required = c.DatabaseURLMigrate
		if c.DatabaseURLAPI != "" || c.DatabaseURLWorker != "" {
			return errors.New("migration process must receive only the migration database credential")
		}
	default:
		return nil
	}
	if required == "" {
		return fmt.Errorf("%s command requires its dedicated database URL", command)
	}
	if (command == "api" || command == "worker") && c.ValkeyURL == "" {
		return errors.New("API and worker processes require HEDVI_VALKEY_URL")
	}
	return nil
}

func (c *Config) IsDeployed() bool { return c.Env == EnvStaging || c.Env == EnvProduction }

func get(name, def string) string {
	if v, ok := os.LookupEnv(name); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func getBool(name string, def bool) bool {
	v, err := strconv.ParseBool(get(name, strconv.FormatBool(def)))
	if err != nil {
		return def
	}
	return v
}

func decodeKey(name, def string, minLen int) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(get(name, def))
	if err != nil {
		return nil, fmt.Errorf("%s must be base64: %w", name, err)
	}
	if len(raw) < minLen {
		return nil, fmt.Errorf("%s must decode to at least %d bytes", name, minLen)
	}
	return raw, nil
}

func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}
