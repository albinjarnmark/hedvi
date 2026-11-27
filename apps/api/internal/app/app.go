// Package app wires configuration into running components.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/billing"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/httpapi"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/netsafe"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/queue"
	"hedvi.dev/api/internal/ratelimit"
	"hedvi.dev/api/internal/secure"
	"hedvi.dev/api/internal/webhooks"
	"hedvi.dev/api/internal/worker"
)

type App struct {
	BillingAPI, BillingWorker *billing.Service
	Cfg                       *config.Config
	Log                       *slog.Logger
	APIPool                   *pgxpool.Pool
	WorkerPool                *pgxpool.Pool
	Blobs                     blob.Store
	Queues                    queue.Set
	Provider                  provider.Provider
	Sealer                    *secure.Sealer
	Hasher                    *secure.Hasher
	Limiter                   ratelimit.Limiter
	Domains                   *domains.Service
	Accept                    *accept.Service
	Faults                    *faults.Injector
	Alerts                    worker.AlertPublisher
	closers                   []func()
}

func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}

// New wires the local combined process. Deployed callers must select a command.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	if cfg.IsDeployed() {
		return nil, fmt.Errorf("deployed processes must select their isolated command")
	}
	return NewForCommand(ctx, cfg, log, "dev")
}

func NewForCommand(ctx context.Context, cfg *config.Config, log *slog.Logger, command string) (*App, error) {
	if err := cfg.ValidateForCommand(command); err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Log: log}
	var err error
	if a.Sealer, err = secure.NewSealer(cfg.DataKey); err != nil {
		return nil, err
	}
	a.Hasher = secure.NewHasher(cfg.APIKeyPepper)
	if !cfg.IsDeployed() || command == "api" {
		if a.APIPool, err = db.Open(ctx, cfg.DatabaseURLAPI, 20); err != nil {
			return nil, fmt.Errorf("api database: %w", err)
		}
		a.closers = append(a.closers, a.APIPool.Close)
		if err := CheckAPIRole(ctx, a.APIPool); err != nil {
			a.Close()
			return nil, err
		}
	}
	if !cfg.IsDeployed() || command == "worker" || command == "admin" {
		if a.WorkerPool, err = db.Open(ctx, cfg.DatabaseURLWorker, 20); err != nil {
			a.Close()
			return nil, fmt.Errorf("worker database: %w", err)
		}
		a.closers = append(a.closers, a.WorkerPool.Close)
	}

	if cfg.ValkeyURL == "" {
		a.Limiter = ratelimit.NewMemory()
	} else if vk, err := ratelimit.NewValkey(cfg.ValkeyURL, log); err == nil {
		a.Limiter = vk
		a.closers = append(a.closers, vk.Close)
	} else {
		log.Warn("valkey unavailable; using in-process rate limits", "err", err)
		a.Limiter = ratelimit.NewMemory()
	}

	switch cfg.AWSMode {
	case "mock":
		a.Blobs = blob.Encrypted{Inner: blob.FS{Root: cfg.BlobDir}, Sealer: a.Sealer}
		if err := queue.EnsureLocalSchema(ctx, a.WorkerPool); err != nil {
			a.Close()
			return nil, err
		}
		if err := provider.EnsureSimulatorSchema(ctx, a.WorkerPool); err != nil {
			a.Close()
			return nil, err
		}
		a.Queues = queue.NewLocalSet(a.WorkerPool)
		sim := &provider.Simulator{Pool: a.WorkerPool, Events: a.Queues.Events, EventDelay: 2 * time.Second}
		if cfg.MockSESFault != "" {
			fault := cfg.MockSESFault
			sim.Fault = func(provider.SendInput) string { return fault }
		}
		a.Provider = sim
	case "aws":
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			a.Close()
			return nil, fmt.Errorf("aws config: %w", err)
		}
		a.Blobs = blob.Encrypted{Inner: blob.S3{Client: s3.NewFromConfig(awsCfg), Bucket: cfg.S3Bucket, KMSKeyID: cfg.S3KMSKeyID}, Sealer: a.Sealer}
		sq := sqs.NewFromConfig(awsCfg)
		a.Queues = queue.Set{
			Send:    queue.SQS{Client: sq, URL: cfg.SendQueueURL, Label: "send"},
			Events:  queue.SQS{Client: sq, URL: cfg.EventsQueueURL, Label: "events"},
			Webhook: queue.SQS{Client: sq, URL: cfg.WebhookQueueURL, Label: "webhook"},
		}
		// Retries are disabled: an automatic SDK retry of SendEmail could
		// create a duplicate the database does not know about.
		ses := sesv2.NewFromConfig(awsCfg, func(o *sesv2.Options) { o.Retryer = aws.NopRetryer{} })
		a.Provider = &provider.SES{Env: cfg.Env, AuthTenant: cfg.AuthMailTenant, AuthConfigSet: cfg.AuthMailConfigSet, Client: ses, Region: cfg.AWSRegion, AccountID: cfg.AWSAccountID, ConfigSet: cfg.SESConfigSet}
		if cfg.OpsAlertSNSTopicARN != "" {
			a.Alerts = &worker.SNSAlertPublisher{Client: sns.NewFromConfig(awsCfg), TopicARN: cfg.OpsAlertSNSTopicARN}
		}
	}

	domainPool := a.APIPool
	if domainPool == nil {
		domainPool = a.WorkerPool
	}
	a.Domains = &domains.Service{TenantEnv: cfg.Env, Pool: domainPool, Provider: a.Provider, Resolver: domains.NewResolver(cfg.DNSServer), MockDNS: cfg.MockDNS, ReservedDomain: cfg.AuthDomain(),
		MailFromMX: cfg.SESMailFromMX, Region: cfg.AWSRegion, Log: log}
	a.Accept = &accept.Service{Pool: a.APIPool, Blobs: a.Blobs, Limits: message.DefaultLimits, Faults: a.Faults,
		MaxQueueTime: cfg.MaxQueueTime, ReservedSenderDomain: cfg.AuthDomain(), Log: log, LoopThreshold: 30, LoopWindow: 10 * time.Minute}
	bc, err := billing.Load(command, string(cfg.Env), cfg.AppURL)
	if err != nil {
		a.Close()
		return nil, err
	}
	if bc != nil {
		if command == "worker" || command == "admin" || command == "dev" {
			a.BillingWorker = billing.New(*bc, a.WorkerPool, true)
		}
		if command == "api" || command == "dev" {
			a.BillingAPI = billing.New(*bc, a.APIPool, false)
		}
	}
	return a, nil
}

// Handler serves the public API and the console API.
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	api := &httpapi.Server{Pool: a.APIPool, Hasher: a.Hasher, Accept: a.Accept, Domains: a.Domains, Blobs: a.Blobs,
		Limiter: a.Limiter, Log: a.Log, KeyRate: 10, KeyBurst: 20, TrustedProxies: a.Cfg.TrustedProxies}
	api.Routes(mux)
	if a.BillingAPI != nil {
		mux.HandleFunc("POST /billing/stripe/webhook", a.BillingAPI.Webhook)
	}
	con := &console.Server{Billing: a.BillingAPI, Cfg: a.Cfg, Pool: a.APIPool, StaffPool: a.WorkerPool, Hasher: a.Hasher, Sealer: a.Sealer, Accept: a.Accept,
		Domains: a.Domains, Provider: a.Provider, Blobs: a.Blobs, Limiter: a.Limiter, Log: a.Log, API: api}
	con.Routes(mux)
	return httpapi.Common(a.Log, httpapi.MaxBody, mux)
}

// Workers returns the background components sharing the worker pool.
type Workers struct {
	AuthMail   *authmail.Worker
	Dispatcher *worker.Dispatcher
	Sender     *worker.Sender
	Events     *worker.Events
	Webhooks   *webhooks.Deliverer
	Reaper     *worker.Reaper
	Domains    *worker.DomainChecker
	Retention  *worker.Retention
	Risk       *worker.Risk
	Monitor    *worker.Monitor
}

func (a *App) Workers() *Workers {
	cfg := a.Cfg
	workerDomains := *a.Domains
	workerDomains.Pool = a.WorkerPool
	am := &authmail.Worker{Faults: a.Faults, Service: &authmail.Service{Pool: a.WorkerPool, Hasher: a.Hasher, Sealer: a.Sealer}, Provider: a.Provider, From: cfg.AuthMailFrom, Tenant: cfg.AuthMailTenant, ConfigSet: cfg.AuthMailConfigSet, AppURL: cfg.AppURL}
	return &Workers{
		AuthMail:   am,
		Dispatcher: &worker.Dispatcher{Pool: a.WorkerPool, Queues: a.Queues, Faults: a.Faults, Log: a.Log},
		Sender: &worker.Sender{TenantEnv: cfg.Env, ReservedSenderDomain: cfg.AuthDomain(), Pool: a.WorkerPool, Queue: a.Queues.Send, Provider: a.Provider, Blobs: a.Blobs, ConfigSet: cfg.SESConfigSet,
			WorkerID: cfg.WorkerID, Lease: 2 * time.Minute, Timeout: 30 * time.Second, Faults: a.Faults, Log: a.Log},
		Events: &worker.Events{AuthMail: am, Pool: a.WorkerPool, Queue: a.Queues.Events, Faults: a.Faults, Log: a.Log},
		Webhooks: &webhooks.Deliverer{Pool: a.WorkerPool, Queue: a.Queues.Webhook, Sealer: a.Sealer, Faults: a.Faults, Log: a.Log,
			Client: netsafe.NewClient(netsafe.Options{AllowPrivate: cfg.WebhookAllowPrivate, AllowHTTP: cfg.WebhookAllowHTTP})},
		Reaper:    &worker.Reaper{Pool: a.WorkerPool, Log: a.Log, StaleAfter: 5 * time.Minute, UnknownEscalation: 30 * time.Minute},
		Domains:   &worker.DomainChecker{Pool: a.WorkerPool, Domains: &workerDomains, Log: a.Log},
		Retention: &worker.Retention{Pool: a.WorkerPool, Blobs: a.Blobs, Log: a.Log, OrphanAge: time.Hour},
		Risk:      &worker.Risk{Pool: a.WorkerPool, Log: a.Log},
		Monitor:   &worker.Monitor{Pool: a.WorkerPool, Log: a.Log, Publisher: a.Alerts, AlertURL: cfg.OpsAlertWebhookURL, HTTPClient: &http.Client{Timeout: 10 * time.Second}},
	}
}
