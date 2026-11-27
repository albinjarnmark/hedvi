package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/app"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/webhooks"
	"hedvi.dev/api/internal/worker"
)

const adminHelp = `hedvi admin commands (worker database role, every change is audited):

  approve-project -project <id> [-hourly 25] [-daily 100] -note <text>
  pause-project   -project <id> -reason <text>
  set-plan        -org <id> -plan free|flex|pro_50k|pro_100k|custom
  billing-reconcile -org <id> -ref <order UUID or Stripe invoice ID> -note <reason>
  grant-credits   -org <id> -recipients 10000 -ref <text>   (Flex lot, 12 months)
  make-staff      -user <id> -email <address> -identity-verified -note <independent identity check> (requires MFA)
  resolve-unknown -attempt <id> -outcome accepted|not_sent -note <text>
  auth-mail-link -email <address>  print the latest simulated account link (local only)
  seed-local      create a local demo account with an approved project (local only)
  contract-fixture  print JSON with fresh keys, a verified domain and a signed webhook sample (local only)`

func admin(ctx context.Context, cfg *config.Config, log *slog.Logger, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		fmt.Println(adminHelp)
		return nil
	}
	a, err := app.NewForCommand(ctx, cfg, log, "admin")
	if err != nil {
		return err
	}
	defer a.Close()
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	project := fs.String("project", "", "project id")
	org := fs.String("org", "", "organization id")
	note := fs.String("note", "", "note for the audit log")
	reason := fs.String("reason", "", "reason")
	hourly := fs.Int("hourly", 25, "recipients per hour")
	daily := fs.Int("daily", 100, "recipients per day")
	plan := fs.String("plan", "", "plan id")
	recipients := fs.Int64("recipients", plans.FlexPackRecipients, "credit units")
	ref := fs.String("ref", "", "external reference")
	email := fs.String("email", "", "user email")
	user := fs.String("user", "", "exact user id confirmed through an independent trusted channel")
	identityVerified := fs.Bool("identity-verified", false, "operator has independently verified the human and exact account ID")
	attempt := fs.String("attempt", "", "send attempt id")
	outcome := fs.String("outcome", "", "accepted or not_sent")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	auditSystem := func(tx pgx.Tx, orgID *uuid.UUID, action, tt, tid string, meta map[string]any) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_kind, action, target_type, target_id, metadata)
			VALUES ($1, 'staff', $2, $3, $4, $5)`, orgID, action, tt, tid, meta)
		return err
	}
	pool := a.WorkerPool
	switch args[0] {
	case "approve-project":
		pid, err := uuid.Parse(*project)
		if err != nil || *note == "" {
			return errors.New("-project and -note are required")
		}
		var tenant string
		var orgID uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT ses_tenant_name, org_id FROM projects WHERE id = $1`, pid).Scan(&tenant, &orgID); err != nil {
			return err
		}
		if err := cfg.Env.CheckProjectTenant(pid, tenant); err != nil {
			return err
		}
		if err := a.Provider.EnsureTenant(ctx, tenant); err != nil {
			return err
		}
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE projects SET sending_status = 'approved', status_reason = $2, status_changed_at = now(),
				hourly_limit = $3, daily_limit = $4 WHERE id = $1`, pid, *note, *hourly, *daily); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE organizations SET risk_status = 'approved' WHERE id = $1 AND risk_status = 'new'`, orgID); err != nil {
				return err
			}
			return auditSystem(tx, &orgID, "project.approved", "project", pid.String(), map[string]any{"note": *note, "hourly": *hourly, "daily": *daily})
		})
	case "pause-project":
		pid, err := uuid.Parse(*project)
		if err != nil || *reason == "" {
			return errors.New("-project and -reason are required")
		}
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			var orgID uuid.UUID
			if err := tx.QueryRow(ctx, `UPDATE projects SET sending_status = 'paused', status_reason = $2, status_changed_at = now()
				WHERE id = $1 RETURNING org_id`, pid, *reason).Scan(&orgID); err != nil {
				return err
			}
			return auditSystem(tx, &orgID, "project.paused", "project", pid.String(), map[string]any{"reason": *reason})
		})
	case "billing-reconcile":
		oid, err := uuid.Parse(*org)
		if err != nil || len(*ref) > 255 || len(strings.TrimSpace(*note)) < 10 {
			return errors.New("supply an organization, invoice/order reference and review note of at least ten characters")
		}
		if a.BillingWorker == nil {
			return errors.New("billing reconciliation is disabled")
		}
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			table := "billing_invoices"
			if _, err := uuid.Parse(*ref); err == nil {
				table = "billing_orders"
			} else if !strings.HasPrefix(*ref, "in_") {
				return errors.New("invalid invoice reference")
			}
			reset := "lease=NULL,lease_until=NULL,next_check_at=now()"
			if table == "billing_orders" {
				reset += ",invoice_cursor=NULL"
			}
			tag, err := tx.Exec(ctx, `UPDATE `+table+` SET `+reset+` WHERE id=$1 AND org_id=$2`, *ref, oid)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errors.New("resource not found in this organization")
			}
			return auditSystem(tx, &oid, "billing.reconcile_requested", table, *ref, map[string]any{"note": *note})
		})
	case "set-plan":
		oid, err := uuid.Parse(*org)
		if err != nil {
			return errors.New("-org is required")
		}
		if _, err := plans.Get(*plan); err != nil {
			return err
		}
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE organizations SET plan = $2 WHERE id = $1`, oid, *plan); err != nil {
				return err
			}
			// The baseline selects its existing calendar bucket; paid cycles remain immutable.
			return auditSystem(tx, &oid, "organization.plan_changed", "organization", oid.String(), map[string]any{"plan": *plan})
		})
	case "grant-credits":
		oid, err := uuid.Parse(*org)
		if err != nil || *ref == "" {
			return errors.New("-org and -ref are required")
		}
		packs := (*recipients + plans.FlexPackRecipients - 1) / plans.FlexPackRecipients
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			id, err := quota.GrantCredits(ctx, tx, oid, "grant", *ref, *recipients, packs*plans.FlexPackDataBytes, plans.FlexPackValidity, timeNow())
			if err != nil {
				return err
			}
			fmt.Println("credit lot", id)
			return auditSystem(tx, &oid, "credits.granted", "credit_lot", id.String(), map[string]any{"recipients": *recipients, "ref": *ref})
		})
	case "make-staff":
		uid, err := uuid.Parse(*user)
		if err != nil {
			return errors.New("-user must be the exact independently verified account UUID; an email alone is insufficient")
		}
		if err := console.GrantStaff(ctx, pool, uid, *email, *note, *identityVerified); err != nil {
			return err
		}
		fmt.Println("staff granted and all existing sessions revoked; sign in again with MFA")
		return nil
	case "resolve-unknown":
		aid, err := uuid.Parse(*attempt)
		if err != nil || *note == "" || (*outcome != "accepted" && *outcome != "not_sent") {
			return errors.New("-attempt, -outcome accepted|not_sent and -note are required")
		}
		return db.Tx(ctx, pool, func(tx pgx.Tx) error {
			var emailID, orgID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT a.email_id, e.org_id FROM send_attempts a JOIN emails e ON e.id = a.email_id
				WHERE a.id = $1 AND a.state = 'unknown'`, aid).Scan(&emailID, &orgID); err != nil {
				return err
			}
			if err := worker.ResolveUnknown(ctx, tx, emailID, aid, *outcome, "", "cli: "+*note, uuid.Nil); err != nil {
				return err
			}
			return auditSystem(tx, &orgID, "send_attempt.resolved", "send_attempt", aid.String(), map[string]any{"outcome": *outcome, "note": *note})
		})
	case "contract-fixture":
		if cfg.IsDeployed() {
			return errors.New("contract-fixture is for local development only")
		}
		return contractFixture(ctx, a)
	case "auth-mail-link":
		if cfg.IsDeployed() || cfg.AWSMode != "mock" {
			return errors.New("auth-mail-link is for local simulation only")
		}
		addr, err := address.Parse(*email)
		if err != nil || addr.Name != "" {
			return errors.New("-email must be a bare address")
		}
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT raw FROM localmock.ses_outbound WHERE $1=ANY(recipients) AND tags ? 'hedvi_auth' ORDER BY created_at DESC LIMIT 1`, addr.Normalized).Scan(&raw); err != nil {
			return errors.New("no simulated account message; request a link and run the local worker first")
		}
		return printLocalAuthLink(raw, cfg.AppURL)
	case "seed-local":
		if cfg.IsDeployed() {
			return errors.New("seed-local is for local development only")
		}
		return seedLocal(ctx, a)
	}
	return fmt.Errorf("unknown admin command %q\n%s", args[0], adminHelp)
}

// seedLocal creates demo@hedvi.test / hedvi-local-demo with an approved
// project and a verified *.test domain (mock DNS), and prints API keys.
func seedLocal(ctx context.Context, a *app.App) error {
	const email, password = "demo@hedvi.test", "hedvi-local-demo"
	var exists bool
	if err := a.WorkerPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1)`, email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		fmt.Println("demo account already exists:", email, "/", password)
		return nil
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	userID, orgID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	var projectID uuid.UUID
	var testKey, liveKey string
	err = db.Tx(ctx, a.WorkerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, email, name, password_hash,email_verified_at) VALUES ($1, $2, 'Demo', $3,now())`, userID, email, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO organizations (id, name, risk_status) VALUES ($1, 'Demo AB', 'approved')`, orgID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
			return err
		}
		if projectID, err = console.NewProject(ctx, tx, orgID, "Demo app", a.Cfg.Env); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET sending_status = 'approved', status_reason = 'local seed', hourly_limit = 1000, daily_limit = 5000
			WHERE id = $1`, projectID); err != nil {
			return err
		}
		_, tk, err := console.CreateKey(ctx, tx, a.Hasher, orgID, projectID, &userID, "Local test key", "test", "full_access", nil)
		if err != nil {
			return err
		}
		_, lk, err := console.CreateKey(ctx, tx, a.Hasher, orgID, projectID, &userID, "Local live key (simulator)", "live", "full_access", nil)
		testKey, liveKey = tk.Plaintext, lk.Plaintext
		return err
	})
	if err != nil {
		return err
	}
	// Use the API pool path via Create, which runs a first DNS check (mock DNS verifies .test).
	if _, err := a.Domains.Create(ctx, orgID, projectID, "demo.hedvi.test", "send"); err != nil {
		return fmt.Errorf("seed domain: %w", err)
	}
	fmt.Printf("Local demo account\n  sign in:  %s / %s\n  project:  %s\n  domain:   demo.hedvi.test (mock DNS, verified)\n  test key: %s\n  live key: %s (local SES simulator, nothing leaves your machine)\n",
		email, password, projectID, testKey, liveKey)
	return nil
}

// contractFixture creates an isolated approved tenant for SDK contract tests.
func contractFixture(ctx context.Context, a *app.App) error {
	orgID := uuid.Must(uuid.NewV7())
	domain := "c" + strings.ReplaceAll(orgID.String()[24:], "-", "") + ".hedvi.test"
	var projectID uuid.UUID
	var testKey, liveKey string
	err := db.Tx(ctx, a.WorkerPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO organizations (id, name, risk_status) VALUES ($1, 'Contract tests', 'approved')`, orgID); err != nil {
			return err
		}
		var err error
		if projectID, err = console.NewProject(ctx, tx, orgID, "Contracts", a.Cfg.Env); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET sending_status = 'approved', hourly_limit = 10000, daily_limit = 10000 WHERE id = $1`, projectID); err != nil {
			return err
		}
		_, tk, err := console.CreateKey(ctx, tx, a.Hasher, orgID, projectID, nil, "contract test", "test", "full_access", nil)
		if err != nil {
			return err
		}
		_, lk, err := console.CreateKey(ctx, tx, a.Hasher, orgID, projectID, nil, "contract live", "live", "full_access", nil)
		testKey, liveKey = tk.Plaintext, lk.Plaintext
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO domains (org_id, project_id, name, status, verified_at) VALUES ($1, $2, $3, 'verified', now())`, orgID, projectID, domain)
		return err
	})
	if err != nil {
		return err
	}
	secret := webhooks.NewSecret()
	id := webhooks.NewEventID()
	now := time.Now()
	body := `{"type":"email.delivered","created_at":"` + now.UTC().Format(time.RFC3339) + `","data":{"email_id":"x","to":["a@example.com"]}}`
	sig, err := webhooks.Sign(secret, id, now, []byte(body))
	if err != nil {
		return err
	}
	out, _ := json.Marshal(map[string]any{"test_key": testKey, "live_key": liveKey, "domain": domain, "project_id": projectID,
		"webhook": map[string]string{"secret": secret, "id": id, "timestamp": strconv.FormatInt(now.Unix(), 10), "signature": sig, "body": body}})
	fmt.Println(string(out))
	return nil
}
