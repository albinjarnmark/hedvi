package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/google/uuid"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/provider"
)

// Run before promoting an API/worker build, using that service's own restricted
// principal. A fixed environment-specific probe avoids accumulating SES tenants.
// A worker can additionally exercise the customer RAW permission branch against
// the SES success simulator using an already verified operator-owned domain.
// No customer recipient or application database is touched.
func checkProvider(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	if !cfg.IsDeployed() || cfg.AWSMode != "aws" {
		return errors.New("provider-check requires a deployed AWS environment")
	}
	canary, err := parseSenderCanary(args)
	if err != nil {
		return err
	}
	if canary != nil && (cfg.DatabaseURLWorker == "" || cfg.DatabaseURLAPI != "" || cfg.DatabaseURLMigrate != "") {
		return errors.New("sender canary requires the worker service environment")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id := uuid.NewSHA1(uuid.NameSpaceDNS, []byte("hedvi.dev/release-provider-check/"+string(cfg.Env)))
	tenant, err := cfg.Env.ProjectTenant(id)
	if err != nil {
		return err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return err
	}
	client := sesv2.NewFromConfig(awsCfg, func(o *sesv2.Options) { o.Retryer = aws.NopRetryer{} })
	ses := &provider.SES{Env: cfg.Env, Client: client, Region: cfg.AWSRegion, AccountID: cfg.AWSAccountID, ConfigSet: cfg.SESConfigSet}
	if err := ses.EnsureTenant(ctx, tenant); err != nil {
		return fmt.Errorf("runtime SES provisioning check failed; keep previous deployment: %w", err)
	}
	// Existing-tenant retries must pass with the exact same permissions as creates.
	if err := ses.EnsureTenant(ctx, tenant); err != nil {
		return fmt.Errorf("runtime SES retry check failed; keep previous deployment: %w", err)
	}
	if canary != nil {
		st, err := ses.GetIdentity(ctx, canary.domain, canary.claim)
		if err != nil {
			return fmt.Errorf("sender canary ownership check: %w", err)
		}
		if !st.VerifiedForSending || st.DKIMStatus != "SUCCESS" || st.MailFromStatus != "SUCCESS" {
			return errors.New("sender canary domain must have verified ownership, DKIM and MAIL FROM")
		}
		sendTenant, err := cfg.Env.ProjectTenant(canary.project)
		if err != nil {
			return err
		}
		const recipient = "success@simulator.amazonses.com"
		raw := []byte("From: " + canary.from + "\r\nTo: " + recipient + "\r\nSubject: Hedvi deployment sender check\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nSynthetic deployment permission check.\r\n")
		messageID, err := ses.Send(ctx, provider.SendInput{From: canary.from, Recipients: []string{recipient}, Raw: raw,
			TenantName: sendTenant, ConfigurationSet: cfg.SESConfigSet,
			Tags: map[string]string{"hedvi_project": canary.project.String(), "hedvi_canary": "deployment"}})
		if err != nil {
			// Never retry an uncertain send. A failed canary keeps the old image live.
			return fmt.Errorf("runtime customer RAW send check failed; keep previous deployment: %w", err)
		}
		log.Info("customer RAW send check accepted", "environment", cfg.Env, "provider_message_id", messageID)
	}
	log.Info("provider release check passed", "environment", cfg.Env, "tenant", tenant)
	return nil
}

type senderCanary struct {
	project, claim uuid.UUID
	from, domain   string
}

func parseSenderCanary(args []string) (*senderCanary, error) {
	fs := flag.NewFlagSet("provider-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.String("sender-project", "", "existing pilot project UUID")
	claim := fs.String("sender-domain-claim", "", "existing pilot domain claim UUID")
	from := fs.String("sender-from", "", "verified pilot sender")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, errors.New("unexpected provider-check arguments")
	}
	if *project == "" && *claim == "" && *from == "" {
		return nil, nil
	}
	p, errP := uuid.Parse(*project)
	c, errC := uuid.Parse(*claim)
	a, errA := mail.ParseAddress(*from)
	if errP != nil || errC != nil || p == uuid.Nil || c == uuid.Nil || errA != nil || a.Address != *from || strings.ContainsAny(*from, "\r\n") {
		return nil, errors.New("sender check requires a project UUID, domain claim UUID and bare sender address")
	}
	parts := strings.Split(*from, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, errors.New("invalid canary sender")
	}
	return &senderCanary{project: p, claim: c, from: *from, domain: parts[1]}, nil
}
