// Command hedvi runs the Hedvi API, workers, migrations and admin tasks.
//
//	hedvi migrate          apply database migrations (owner role)
//	hedvi api              public + console API
//	hedvi worker           background workers
//	hedvi dev              api and worker in one process (local only)
//	hedvi provider-check  pre-deploy SES provisioning and optional simulator send
//	hedvi admin <command>  operator tasks, see `hedvi admin help`
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hedvi.dev/api/internal/app"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/worker"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hedvi migrate|api|worker|dev|admin|provider-check")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "err", err)
		os.Exit(1)
	}
	if err := cfg.ValidateForCommand(os.Args[1]); err != nil {
		log.Error("command configuration", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "provider-check":
		err = checkProvider(ctx, cfg, log, os.Args[2:])
	case "migrate":
		err = migrate(ctx, cfg, log)
	case "api":
		err = runAPI(ctx, cfg, log)
	case "worker":
		err = runWorker(ctx, cfg, log)
	case "dev":
		if cfg.IsDeployed() {
			err = errors.New("dev mode is for local development only")
			break
		}
		err = runDev(ctx, cfg, log)
	case "admin":
		err = admin(ctx, cfg, log, os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Error("hedvi", "command", os.Args[1], "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	pool, err := db.Open(ctx, cfg.DatabaseURLMigrate, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := db.Migrate(ctx, pool)
	log.Info("migrations", "applied", applied)
	return err
}

func serveMetrics(ctx context.Context, cfg *config.Config, log *slog.Logger) {
	if cfg.MetricsAddr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler())
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener", "err", err)
		}
	}()
}

func serveHTTP(ctx context.Context, a *app.App) error {
	srv := &http.Server{
		Addr:              a.Cfg.APIAddr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	a.Log.Info("api listening", "addr", a.Cfg.APIAddr, "aws_mode", a.Cfg.AWSMode, "env", a.Cfg.Env)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func runAPI(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	a, err := app.NewForCommand(ctx, cfg, log, "api")
	if err != nil {
		return err
	}
	defer a.Close()
	serveMetrics(ctx, cfg, log)
	return serveHTTP(ctx, a)
}

func startWorkers(ctx context.Context, a *app.App) {
	w := a.Workers()
	consumers := []worker.Runnable{w.Dispatcher, w.Sender, w.Events, w.Webhooks}
	jobs := []worker.Periodic{
		{Name: "signup-notices", Interval: time.Second, Fn: w.AuthMail.SendSignupNotice},
		{Name: "auth-mail", Interval: time.Second, Fn: w.AuthMail.RunOnce},
		{Name: "reaper", Interval: 15 * time.Second, Fn: w.Reaper.RunOnce},
		{Name: "domains", Interval: 10 * time.Second, Fn: w.Domains.RunOnce},
		{Name: "retention", Interval: 5 * time.Minute, Fn: w.Retention.RunOnce},
		{Name: "orphans", Interval: 30 * time.Minute, Fn: func(ctx context.Context) error { _, err := w.Retention.SweepOrphans(ctx); return err }},
		{Name: "risk", Interval: 5 * time.Minute, Fn: w.Risk.RunOnce},
		{Name: "monitor", Interval: 30 * time.Second, Fn: w.Monitor.RunOnce},
	}
	if a.BillingWorker != nil {
		jobs = append(jobs, worker.Periodic{Name: "stripe-billing", Interval: 5 * time.Second, Fn: a.BillingWorker.RunOnce})
	}
	worker.RunAll(ctx, a.Log, consumers, jobs)
}

func runWorker(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	a, err := app.NewForCommand(ctx, cfg, log, "worker")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveMetrics(ctx, cfg, log)
	log.Info("workers started", "worker_id", cfg.WorkerID, "aws_mode", cfg.AWSMode)
	if cfg.AdminAddr == "" {
		startWorkers(ctx, a)
		return nil
	}
	listener, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.OwnerHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	ended := make(chan error, 1)
	go func() { err := srv.Serve(listener); ended <- err; cancel() }()
	startWorkers(ctx, a)
	_ = srv.Close()
	if err = <-ended; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runDev(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	if err := migrate(ctx, cfg, log); err != nil {
		return err
	}
	a, err := app.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.Close()
	serveMetrics(ctx, cfg, log)
	go startWorkers(ctx, a)
	return serveHTTP(ctx, a)
}

func timeNow() time.Time { return time.Now() }
