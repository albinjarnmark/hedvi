// Package testenv creates isolated PostgreSQL databases and wired components
// for integration tests. Each test gets a database cloned from a migrated
// template, so tests can run in parallel without sharing state.
package testenv

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/authmail"
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

var template = func() string {
	if name := os.Getenv("HEDVI_TEST_TEMPLATE"); name != "" {
		return name
	}
	if os.Getenv("HEDVI_TEST_VARIANT") == "billing" {
		return "hedvi_billing_test_template"
	}
	return "hedvi_test_template"
}()

func adminURL() string {
	if v := os.Getenv("HEDVI_TEST_ADMIN_URL"); v != "" {
		return v
	}
	return "postgres://hedvi_owner:hedvi_owner_dev@127.0.0.1:55432/postgres?sslmode=disable"
}

func withDB(raw, name, user, password string) string {
	u, _ := url.Parse(raw)
	u.Path = "/" + name
	u.User = url.UserPassword(user, password)
	return u.String()
}

var (
	templateOnce sync.Once
	templateErr  error
)

func prepareTemplate(ctx context.Context) error {
	if !regexp.MustCompile(`^hedvi_([a-z0-9_]{1,32}_)?test_template$`).MatchString(template) {
		return fmt.Errorf("HEDVI_TEST_TEMPLATE must be a Hedvi-specific test template name")
	}
	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock(424242)`); err != nil {
		return err
	}
	defer admin.Exec(ctx, `SELECT pg_advisory_unlock(424242)`) //nolint:errcheck
	// Rebuild the template every run so it always matches the migrations.
	_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+template+` WITH (FORCE)`)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+template); err != nil {
		return err
	}
	pool, err := db.Open(ctx, withDB(adminURL(), template, "hedvi_owner", "hedvi_owner_dev"), 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := db.Migrate(ctx, pool); err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE SCHEMA localmock AUTHORIZATION hedvi_worker`); err != nil {
		return err
	}
	return nil
}

// Env is a fully wired Hedvi instance on a private database.
type Env struct {
	AuthMail   *authmail.Worker
	T          testing.TB
	Ctx        context.Context
	APIPool    *pgxpool.Pool
	WorkerPool *pgxpool.Pool
	Blobs      blob.Store
	Queues     queue.Set
	Sim        *provider.Simulator
	Fault      string
	Faults     *faults.Injector
	Hasher     *secure.Hasher
	Sealer     *secure.Sealer
	Accept     *accept.Service
	Domains    *domains.Service
	Dispatcher *worker.Dispatcher
	Sender     *worker.Sender
	Events     *worker.Events
	Webhooks   *webhooks.Deliverer
	Reaper     *worker.Reaper
	Retention  *worker.Retention
	Risk       *worker.Risk
	Console    *console.Server
	Handler    http.Handler
	mu         sync.Mutex
}

// New returns an Env or skips the test when PostgreSQL is unavailable
// (set HEDVI_REQUIRE_DB=1 in CI to fail instead).
func New(t testing.TB) *Env {
	t.Helper()
	ctx := context.Background()
	templateOnce.Do(func() { templateErr = prepareTemplate(ctx) })
	if templateErr != nil {
		if os.Getenv("HEDVI_REQUIRE_DB") == "1" {
			t.Fatalf("test database: %v", templateErr)
		}
		t.Skipf("PostgreSQL not available (run `make up`): %v", templateErr)
	}
	name := "hedvi_t_" + strings.ReplaceAll(uuid.NewString()[:13], "-", "")
	admin, err := pgx.Connect(ctx, adminURL())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+name+` TEMPLATE `+template); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), adminURL())
		if err == nil {
			_, _ = c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
			c.Close(context.Background())
		}
	})
	e := &Env{T: t, Ctx: ctx, Faults: faults.New()}
	if e.APIPool, err = db.Open(ctx, withDB(adminURL(), name, "hedvi_api", "hedvi_api_dev"), 15); err != nil {
		t.Fatal(err)
	}
	if e.WorkerPool, err = db.Open(ctx, withDB(adminURL(), name, "hedvi_worker", "hedvi_worker_dev"), 15); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.APIPool.Close(); e.WorkerPool.Close() })
	if err := queue.EnsureLocalSchema(ctx, e.WorkerPool); err != nil {
		t.Fatal(err)
	}
	if err := provider.EnsureSimulatorSchema(ctx, e.WorkerPool); err != nil {
		t.Fatal(err)
	}
	e.Sealer, _ = secure.NewSealer(bytes.Repeat([]byte{9}, 32))
	e.Hasher = secure.NewHasher([]byte("test-pepper-test-pepper"))
	e.Blobs = blob.Encrypted{Inner: blob.FS{Root: t.TempDir()}, Sealer: e.Sealer}
	e.Queues = queue.NewLocalSet(e.WorkerPool)
	e.Sim = &provider.Simulator{Pool: e.WorkerPool, Events: e.Queues.Events, Fault: func(provider.SendInput) string {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.Fault
	}}
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))
	e.Domains = &domains.Service{TenantEnv: config.EnvTest, Pool: e.APIPool, Provider: e.Sim, Resolver: domains.NewResolver(""), MockDNS: true,
		MailFromMX: "feedback-smtp.eu-north-1.amazonses.com", Region: "eu-north-1", Log: log}
	e.Accept = &accept.Service{Pool: e.APIPool, Blobs: e.Blobs, Limits: message.DefaultLimits, Faults: e.Faults,
		MaxQueueTime: 72 * time.Hour, Log: log, LoopThreshold: 30, LoopWindow: 10 * time.Minute}
	e.Dispatcher = &worker.Dispatcher{Pool: e.WorkerPool, Queues: e.Queues, Faults: e.Faults, Log: log}
	e.Sender = &worker.Sender{TenantEnv: config.EnvTest, Pool: e.WorkerPool, Queue: e.Queues.Send, Provider: e.Sim, Blobs: e.Blobs, ConfigSet: "test",
		WorkerID: "test-worker", Lease: 2 * time.Minute, Timeout: 5 * time.Second, Faults: e.Faults, Log: log}
	e.AuthMail = &authmail.Worker{Faults: e.Faults, Service: &authmail.Service{Pool: e.WorkerPool, Sealer: e.Sealer, Hasher: e.Hasher}, Provider: e.Sim, From: "accounts@auth.example.test", Tenant: "hedvi-system-auth", ConfigSet: "test", AppURL: "http://localhost:3030"}
	e.Events = &worker.Events{AuthMail: e.AuthMail, Pool: e.WorkerPool, Queue: e.Queues.Events, Faults: e.Faults, Log: log}
	e.Webhooks = &webhooks.Deliverer{Pool: e.WorkerPool, Queue: e.Queues.Webhook, Sealer: e.Sealer, Faults: e.Faults, Log: log,
		Client: netsafe.NewClient(netsafe.Options{AllowPrivate: true, AllowHTTP: true, Timeout: 3 * time.Second})}
	e.Reaper = &worker.Reaper{Pool: e.WorkerPool, Log: log, StaleAfter: 5 * time.Minute, UnknownEscalation: 30 * time.Minute}
	e.Retention = &worker.Retention{Pool: e.WorkerPool, Blobs: e.Blobs, Log: log, OrphanAge: time.Hour}
	e.Risk = &worker.Risk{Pool: e.WorkerPool, Log: log}

	cfg := &config.Config{Env: config.EnvTest, AWSMode: "mock", AppURL: "http://localhost:3030", LegalOrigin: "https://legal.example.test", PublicAPIURL: "http://api.test",
		WebhookAllowPrivate: true, WebhookAllowHTTP: true}
	mux := http.NewServeMux()
	api := &httpapi.Server{Pool: e.APIPool, Hasher: e.Hasher, Accept: e.Accept, Domains: e.Domains, Blobs: e.Blobs,
		Limiter: ratelimit.NewMemory(), Log: log, KeyRate: 1000, KeyBurst: 1000}
	api.Routes(mux)
	con := &console.Server{Cfg: cfg, Pool: e.APIPool, StaffPool: e.WorkerPool, Hasher: e.Hasher, Sealer: e.Sealer, Accept: e.Accept,
		Domains: e.Domains, Provider: e.Sim, Blobs: e.Blobs, Limiter: ratelimit.NewMemory(), Log: log, API: api}
	e.Console = con
	con.Routes(mux)
	e.Handler = httpapi.Common(log, httpapi.MaxBody, mux)
	return e
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func (e *Env) SetFault(f string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Fault = f
}

// Tenant is an organization with one project and keys.
type Tenant struct {
	OrgID, ProjectID uuid.UUID
	TestKey, LiveKey string
	Test, Live       accept.Principal
	Domain           string
}

// NewTenant creates an approved project with a verified domain <slug>.test.
func (e *Env) NewTenant(plan string) *Tenant {
	e.T.Helper()
	ctx := e.Ctx
	t := &Tenant{OrgID: uuid.Must(uuid.NewV7())}
	t.Domain = "t" + strings.ReplaceAll(t.OrgID.String()[24:], "-", "") + ".test"
	err := db.Tx(ctx, e.WorkerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organizations (id, name, plan, risk_status) VALUES ($1, 'Org', $2, 'approved')`, t.OrgID, plan); err != nil {
			return err
		}
		var err error
		if t.ProjectID, err = console.NewProject(ctx, tx, t.OrgID, "P", config.EnvTest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET sending_status = 'approved', hourly_limit = 100000, daily_limit = 100000,
			test_daily_limit = 100000 WHERE id = $1`, t.ProjectID); err != nil {
			return err
		}
		tid, tk, err := console.CreateKey(ctx, tx, e.Hasher, t.OrgID, t.ProjectID, nil, "test", "test", "full_access", nil)
		if err != nil {
			return err
		}
		lid, lk, err := console.CreateKey(ctx, tx, e.Hasher, t.OrgID, t.ProjectID, nil, "live", "live", "full_access", nil)
		if err != nil {
			return err
		}
		t.TestKey, t.LiveKey = tk.Plaintext, lk.Plaintext
		t.Test = accept.Principal{KeyID: tid, OrgID: t.OrgID, ProjectID: t.ProjectID, Mode: "test", Permission: "full_access"}
		t.Live = accept.Principal{KeyID: lid, OrgID: t.OrgID, ProjectID: t.ProjectID, Mode: "live", Permission: "full_access"}
		_, err = tx.Exec(ctx, `INSERT INTO domains (org_id, project_id, name, status, verified_at) VALUES ($1, $2, $3, 'verified', now())`,
			t.OrgID, t.ProjectID, t.Domain)
		return err
	})
	if err != nil {
		e.T.Fatal(err)
	}
	return t
}

func (e *Env) Count(sql string, args ...any) int {
	e.T.Helper()
	var n int
	if err := e.WorkerPool.QueryRow(e.Ctx, sql, args...).Scan(&n); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *Env) Exec(sql string, args ...any) {
	e.T.Helper()
	if _, err := e.WorkerPool.Exec(e.Ctx, sql, args...); err != nil {
		e.T.Fatalf("%s: %v", sql, err)
	}
}

// Drain runs dispatcher, sender, events and webhooks until queues are idle.
func (e *Env) Drain() {
	e.T.Helper()
	for range 20 {
		progressed := false
		n, err := e.Dispatcher.RunOnce(e.Ctx)
		if err != nil && err != faults.ErrCrash {
			e.T.Fatal(err)
		}
		progressed = progressed || n > 0
		for _, q := range []queue.Queue{e.Queues.Send, e.Queues.Events, e.Queues.Webhook} {
			msgs, err := q.Receive(e.Ctx, 10, 0)
			if err != nil {
				e.T.Fatal(err)
			}
			for _, m := range msgs {
				progressed = true
				if err := e.handle(q, m.Body); err == nil {
					_ = q.Delete(e.Ctx, m.Receipt)
				}
			}
		}
		if !progressed {
			return
		}
	}
}

func (e *Env) handle(q queue.Queue, body []byte) error {
	switch q.Name() {
	case "send":
		var j struct {
			EmailID uuid.UUID `json:"email_id"`
		}
		_ = jsonUnmarshal(body, &j)
		return e.Sender.Handle(e.Ctx, j.EmailID)
	case "events":
		ev, err := parseEvent(body)
		if err != nil {
			return nil
		}
		return e.Events.Apply(e.Ctx, ev)
	default:
		var j struct {
			DeliveryID uuid.UUID `json:"delivery_id"`
		}
		_ = jsonUnmarshal(body, &j)
		return e.Webhooks.Handle(e.Ctx, j.DeliveryID)
	}
}

// MakeDue makes delayed work immediately visible (time travel for tests).
func (e *Env) MakeDue() {
	e.Exec(`UPDATE localmock.queue_messages SET visible_at = now()`)
	e.Exec(`UPDATE outbox SET available_at = now() WHERE published_at IS NULL`)
	e.Exec(`UPDATE emails SET next_attempt_at = now() WHERE send_state = 'retry_wait'`)
	e.Exec(`UPDATE webhook_deliveries SET next_attempt_at = now() WHERE state = 'pending'`)
}

func Body(v map[string]any) []byte {
	b, _ := jsonMarshal(v)
	return b
}

func Sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
