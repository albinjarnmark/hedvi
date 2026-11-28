package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/queue"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/webhooks"
)

// Sender claims accepted emails and hands them to the provider.
//
// Uncertain-send policy (docs/architecture/uncertain-sends.md):
//  1. A send attempt row is committed before the provider call.
//  2. Only outcomes that prove SES did not accept the message are retried.
//  3. Ambiguous outcomes become delivery_unknown: no automatic retry, an
//     alert is raised, and SES events tagged with the attempt id reconcile it.
//  4. A deliberate resend is a new email with its own quota and audit trail.
type Sender struct {
	TenantEnv            config.Env
	ReservedSenderDomain string
	Pool                 *pgxpool.Pool
	Queue                queue.Queue
	Provider             provider.Provider
	Blobs                blob.Store
	ConfigSet            string
	WorkerID             string
	Lease                time.Duration
	Timeout              time.Duration
	Faults               *faults.Injector
	Log                  *slog.Logger
}

type sendJob struct {
	EmailID string `json:"email_id"`
}

func (s *Sender) Run(ctx context.Context) {
	queue.Consume(ctx, s.Queue, s.Log, 4, func(ctx context.Context, m queue.Message) error {
		var job sendJob
		if err := json.Unmarshal(m.Body, &job); err != nil {
			return nil
		}
		id, err := uuid.Parse(job.EmailID)
		if err != nil {
			return nil
		}
		return s.Handle(ctx, id)
	})
}

type claimed struct {
	orgID, projectID    uuid.UUID
	domainID            uuid.UUID
	payloadRef          *string
	fromDomain          string
	deadline            time.Time
	attempts            int
	createdAt           time.Time
	tenant              string
	sendingStatus       string
	riskStatus          string
	domainValid         bool
	domainRevision      int64
	domainReconfiguring bool
}

// Handle processes one send job. A nil error means the job message can be
// deleted (including duplicates and jobs that are not due).
func (s *Sender) Handle(ctx context.Context, emailID uuid.UUID) error {
	// A fresh fence prevents an overdue handler from committing under a later
	// claim by a different goroutine in this same worker process.
	claim := *s
	claim.WorkerID = s.WorkerID + ":" + uuid.NewString()
	return claim.handle(ctx, emailID)
}

func (s *Sender) handle(ctx context.Context, emailID uuid.UUID) error {
	var c claimed
	deferred := false
	err := db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT org_id FROM emails WHERE id=$1 AND mode='live' AND send_state IN ('queued','retry_wait')
			AND next_attempt_at<=now()+interval '2 seconds'`, emailID).Scan(&c.orgID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,614))`, c.orgID.String()); err != nil {
			return err
		}
		var full bool
		if err := tx.QueryRow(ctx, `SELECT count(*)>=2 FROM emails WHERE org_id=$1 AND send_state='sending' AND lease_expires_at>now()`, c.orgID).Scan(&full); err != nil {
			return err
		}
		if full {
			var next time.Time
			if err := tx.QueryRow(ctx, `UPDATE emails SET next_attempt_at=now()+interval '5 seconds'
				WHERE id=$1 AND send_state IN ('queued','retry_wait') AND next_attempt_at<=now()+interval '2 seconds'
				RETURNING next_attempt_at`, emailID).Scan(&next); err != nil {
				return err
			}
			deferred = true
			return sendOutbox(ctx, tx, c.orgID, emailID, next)
		}
		err = tx.QueryRow(ctx, `UPDATE emails e SET send_state = 'sending', lease_owner = $2,
				lease_expires_at = now() + $3 * interval '1 millisecond', attempt_count = attempt_count + 1
			FROM projects p, organizations o
			WHERE e.id = $1 AND p.id = e.project_id AND o.id = e.org_id AND e.mode = 'live'
			  AND e.send_state IN ('queued', 'retry_wait') AND e.next_attempt_at <= now() + interval '2 seconds'
			RETURNING e.org_id, e.project_id, coalesce(e.domain_id,'00000000-0000-0000-0000-000000000000'::uuid), e.payload_ref, e.from_domain, e.queue_deadline_at, e.attempt_count, e.created_at,
				p.ses_tenant_name, p.sending_status, o.risk_status,
 EXISTS (SELECT 1 FROM domains d WHERE d.id = e.domain_id AND d.project_id = e.project_id AND d.status = 'verified' AND d.deleted_at IS NULL),
 coalesce((SELECT d.mail_from_revision FROM domains d WHERE d.id=e.domain_id AND d.project_id=e.project_id),0),
 EXISTS (SELECT 1 FROM domains d WHERE d.id=e.domain_id AND d.project_id=e.project_id AND d.mail_from_reconfiguring AND d.provider_state<>'deleting' AND d.deleted_at IS NULL)`,
			emailID, s.WorkerID, s.Lease.Milliseconds()).Scan(&c.orgID, &c.projectID, &c.domainID, &c.payloadRef, &c.fromDomain, &c.deadline,
			&c.attempts, &c.createdAt, &c.tenant, &c.sendingStatus, &c.riskStatus, &c.domainValid, &c.domainRevision, &c.domainReconfiguring)
		return err
	})
	if db.IsNoRows(err) || (deferred && err == nil) {
		return nil // duplicate, not due, or already handled
	}
	if err != nil {
		return err
	}
	if err := s.Faults.Check(faults.SendAfterClaim); err != nil {
		return err
	}

	// Pauses stop queued jobs before the next attempt.
	if c.sendingStatus != "approved" || (c.riskStatus != "new" && c.riskStatus != "approved") {
		return s.hold(ctx, emailID)
	}
	if time.Now().After(c.deadline) {
		return s.fail(ctx, emailID, "queue_deadline", "The email waited longer than the maximum queue time and was not sent.")
	}

	if s.ReservedSenderDomain != "" && (c.fromDomain == s.ReservedSenderDomain || strings.HasSuffix(c.fromDomain, "."+s.ReservedSenderDomain)) {
		return s.fail(ctx, emailID, "reserved_sender", "This sender domain is reserved for account security mail.")
	}
	if err := s.TenantEnv.CheckProjectTenant(c.projectID, c.tenant); err != nil {
		return s.fail(ctx, emailID, "sender_tenant_unavailable", "The sending project configuration requires operator review.")
	}
	if c.domainReconfiguring {
		return s.waitForDomain(ctx, emailID)
	}
	if !c.domainValid {
		return s.fail(ctx, emailID, "sender_domain_unavailable", "The sending domain was removed or is no longer verified.")
	}

	// Suppression may have changed since acceptance.
	var recipients []string
	err = s.finishKeepLease(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		ids, addrs, err := recipientAddresses(ctx, tx, emailID, "queued")
		if err != nil {
			return err
		}
		var suppressedIDs []uuid.UUID
		for i, a := range addrs {
			var hit bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM suppressions WHERE org_id = $1 AND normalized = lower($2)
				AND removed_at IS NULL AND (expires_at IS NULL OR expires_at > now()))`, c.orgID, a).Scan(&hit); err != nil {
				return err
			}
			if hit {
				suppressedIDs = append(suppressedIDs, ids[i])
				if err := emit(ctx, tx, e, webhooks.EmailSuppressed, []string{a}, time.Now(), map[string]any{
					"suppressed": map[string]string{"message": "Suppressed after acceptance.", "type": "OnOrganizationSuppressionList"}}); err != nil {
					return err
				}
				continue
			}
			recipients = append(recipients, a)
		}
		if len(suppressedIDs) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE recipients SET status = 'suppressed', status_at = now() WHERE id = ANY($1)`, suppressedIDs); err != nil {
				return err
			}
			if _, err := quota.Release(ctx, tx, c.orgID, suppressedIDs); err != nil {
				return err
			}
		}
		if len(recipients) == 0 {
			_, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'skipped', last_event = 'suppressed', last_event_at = now(),
				lease_owner = NULL, lease_expires_at = NULL WHERE id = $1`, emailID)
			return err
		}
		return nil
	})
	if err != nil || len(recipients) == 0 {
		return err
	}
	if c.payloadRef == nil {
		return s.fail(ctx, emailID, "content_missing", "The email content was deleted before it could be sent.")
	}
	raw, err := s.Blobs.Get(ctx, *c.payloadRef)
	if errors.Is(err, blob.ErrNotFound) {
		return s.fail(ctx, emailID, "content_missing", "The email content is missing.")
	}
	if err != nil {
		return s.releaseLease(ctx, emailID, err)
	}
	msg, err := message.Load(raw)
	if err != nil {
		return s.fail(ctx, emailID, "content_invalid", "The stored email could not be read.")
	}
	mime, err := message.BuildMIME(msg, c.createdAt)
	if err != nil {
		return s.fail(ctx, emailID, "mime_build", "The email could not be encoded.")
	}

	// Commit the attempt before calling the provider.
	attemptID := uuid.Must(uuid.NewV7())
	err = db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := checkAttemptPolicy(ctx, tx, c); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE emails SET lease_expires_at = now() + $3 * interval '1 millisecond'
			WHERE id = $1 AND lease_owner = $2 AND send_state = 'sending'`, emailID, s.WorkerID, s.Lease.Milliseconds())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errLeaseLost
		}
		routeID, err := bindProviderSendRoute(ctx, tx, c, s.ConfigSet)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO send_attempts (id, org_id, email_id, worker_id, state, provider_route_id) VALUES ($1, $2, $3, $4, 'started', $5)`,
			attemptID, c.orgID, emailID, s.WorkerID, routeID)
		return err
	})
	if errors.Is(err, errLeaseLost) {
		return nil
	}
	if errors.Is(err, errDomainReconfiguring) {
		return s.waitForDomain(ctx, emailID)
	}
	if errors.Is(err, errDomainUnavailable) {
		return s.fail(ctx, emailID, "sender_domain_unavailable", "The sending domain was removed or is no longer verified.")
	}
	if errors.Is(err, errSendingHeld) {
		return s.hold(ctx, emailID)
	}
	if errors.Is(err, errTenantChanged) {
		return s.fail(ctx, emailID, "sender_tenant_unavailable", "The sending project configuration requires operator review.")
	}
	if errors.Is(err, errSendDeadline) {
		return s.fail(ctx, emailID, "queue_deadline", "The email waited longer than the maximum queue time and was not sent.")
	}
	if err != nil {
		return s.releaseLease(ctx, emailID, err)
	}
	if err := s.Faults.Check(faults.SendAfterAttemptStart); err != nil {
		return err
	}

	callCtx, cancel := context.WithTimeout(ctx, s.Timeout)
	messageID, sendErr := s.Provider.Send(callCtx, provider.SendInput{
		From: msg.FromAddr.Email, Recipients: recipients, Raw: mime, TenantName: c.tenant, ConfigurationSet: s.ConfigSet,
		Tags: map[string]string{"hedvi_email": emailID.String(), "hedvi_attempt": attemptID.String(), "hedvi_project": c.projectID.String()},
	})
	cancel()
	if err := s.Faults.Check(faults.SendAfterProviderCall); err != nil {
		return err
	}
	outcome := provider.Classify(sendErr)
	metrics.SendOutcomes.WithLabelValues(outcome.String()).Inc()
	// Record the outcome even if the worker is shutting down.
	rctx := context.WithoutCancel(ctx)
	switch outcome {
	case provider.Accepted:
		return RecordAccepted(rctx, s.Pool, emailID, attemptID, messageID)
	case provider.Rejected:
		var se *provider.SendError
		errors.As(sendErr, &se)
		return s.recordRejected(rctx, emailID, attemptID, se)
	case provider.Retryable:
		var se *provider.SendError
		errors.As(sendErr, &se)
		return s.recordRetryable(rctx, emailID, attemptID, c, se)
	default:
		return s.recordUnknown(rctx, emailID, attemptID, sendErr)
	}
}

var (
	errLeaseLost           = errors.New("lease lost")
	errDomainReconfiguring = errors.New("sender domain is being reconfigured")
	errDomainUnavailable   = errors.New("sender domain is unavailable")
	errSendingHeld         = errors.New("sending permission is paused")
	errTenantChanged       = errors.New("sending tenant changed")
	errSendDeadline        = errors.New("send queue deadline passed")
)

// The committed started-attempt is the send authorization boundary. Coordinate
// with organization/project restrictions and MAIL FROM changes in the same
// org -> project -> domain order as acceptance. No lock spans provider I/O.
func checkAttemptPolicy(ctx context.Context, tx pgx.Tx, c claimed) error {
	var risk, status, tenant string
	if err := tx.QueryRow(ctx, `SELECT risk_status FROM organizations WHERE id=$1 FOR SHARE`, c.orgID).Scan(&risk); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT sending_status,ses_tenant_name FROM projects WHERE id=$1 AND org_id=$2 FOR SHARE`, c.projectID, c.orgID).Scan(&status, &tenant); err != nil {
		return err
	}
	if status != "approved" || (risk != "new" && risk != "approved") {
		return errSendingHeld
	}
	if tenant != c.tenant {
		return errTenantChanged
	}
	var revision int64
	var reconfiguring bool
	var providerState string
	err := tx.QueryRow(ctx, `SELECT status,provider_state,mail_from_revision,mail_from_reconfiguring FROM domains
		WHERE id=$1 AND org_id=$2 AND project_id=$3 AND deleted_at IS NULL FOR SHARE`, c.domainID, c.orgID, c.projectID).Scan(&status, &providerState, &revision, &reconfiguring)
	if db.IsNoRows(err) || (err == nil && providerState == "deleting") {
		return errDomainUnavailable
	}
	if err != nil {
		return err
	}
	if reconfiguring || revision != c.domainRevision {
		return errDomainReconfiguring
	}
	if status != "verified" {
		return errDomainUnavailable
	}
	if time.Now().After(c.deadline) {
		return errSendDeadline
	}
	return nil
}

func (s *Sender) hold(ctx context.Context, emailID uuid.UUID) error {
	return s.finish(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		_, err := tx.Exec(ctx, `UPDATE emails SET send_state='held',lease_owner=NULL,lease_expires_at=NULL WHERE id=$1`, emailID)
		return err
	})
}

// Waiting for customer DNS is not a provider attempt. Keep the existing quota
// reservation and deadline, and do not increase the provider retry backoff.
func (s *Sender) waitForDomain(ctx context.Context, emailID uuid.UUID) error {
	return s.finish(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		var next time.Time
		if err := tx.QueryRow(ctx, `UPDATE emails SET send_state='retry_wait',next_attempt_at=LEAST(now()+interval '30 seconds',queue_deadline_at),
			attempt_count=greatest(attempt_count-1,0),lease_owner=NULL,lease_expires_at=NULL WHERE id=$1 RETURNING next_attempt_at`, emailID).Scan(&next); err != nil {
			return err
		}
		return sendOutbox(ctx, tx, e.OrgID, emailID, next)
	})
}

// finishKeepLease runs fn with the email locked if this worker holds the lease.
func (s *Sender) finishKeepLease(ctx context.Context, emailID uuid.UUID, fn func(pgx.Tx, *emailInfo) error) error {
	err := db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		var owner *string
		if err := tx.QueryRow(ctx, `SELECT lease_owner FROM emails WHERE id = $1 AND send_state = 'sending' FOR UPDATE`, emailID).Scan(&owner); err != nil {
			if db.IsNoRows(err) {
				return errLeaseLost
			}
			return err
		}
		if owner == nil || *owner != s.WorkerID {
			return errLeaseLost
		}
		e, err := loadEmailInfo(ctx, tx, emailID, false)
		if err != nil {
			return err
		}
		return fn(tx, e)
	})
	if errors.Is(err, errLeaseLost) {
		return nil
	}
	return err
}

func (s *Sender) finish(ctx context.Context, emailID uuid.UUID, fn func(pgx.Tx, *emailInfo) error) error {
	return s.finishKeepLease(ctx, emailID, fn)
}

func (s *Sender) releaseLease(ctx context.Context, emailID uuid.UUID, cause error) error {
	_ = s.finish(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		_, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'retry_wait', next_attempt_at = now() + interval '30 seconds',
			lease_owner = NULL, lease_expires_at = NULL WHERE id = $1`, emailID)
		if err != nil {
			return err
		}
		return sendOutbox(ctx, tx, e.OrgID, emailID, time.Now().Add(30*time.Second))
	})
	return fmt.Errorf("transient: %w", cause)
}

// fail marks an unsent email as failed and releases its quota.
func (s *Sender) fail(ctx context.Context, emailID uuid.UUID, code, reason string) error {
	return s.finish(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		return failEmail(ctx, tx, e, code, reason)
	})
}

func failEmail(ctx context.Context, tx pgx.Tx, e *emailInfo, code, reason string) error {
	ids, addrs, err := recipientAddresses(ctx, tx, e.ID, "queued")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipients SET status = 'failed', status_detail = $2, status_at = now() WHERE id = ANY($1)`, ids, code+": "+reason); err != nil {
		return err
	}
	if _, err := quota.Release(ctx, tx, e.OrgID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'failed', last_event = 'failed', last_event_at = now(),
		lease_owner = NULL, lease_expires_at = NULL WHERE id = $1`, e.ID); err != nil {
		return err
	}
	if len(addrs) == 0 {
		return nil
	}
	return emit(ctx, tx, e, webhooks.EmailFailed, addrs, time.Now(), map[string]any{"failed": map[string]string{"reason": reason, "code": code}})
}

// RecordAccepted settles a direct successful provider response idempotently.
// SES events and manual reconciliation use recordAcceptedTx instead.
func RecordAccepted(ctx context.Context, pool *pgxpool.Pool, emailID, attemptID uuid.UUID, messageID string) error {
	return db.Tx(ctx, pool, func(tx pgx.Tx) error {
		if err := recordAcceptedTx(ctx, tx, emailID, attemptID, messageID, ""); err != nil {
			return err
		}
		// Only the direct, confirmed SendEmail response establishes recovery.
		// Manual decisions and delayed SES event reconciliation use the lower
		// level settlement helper without changing provider incident health.
		if messageID == "" {
			return nil
		}
		return recordProviderSendResult(ctx, tx, emailID, attemptID, "", true)
	})
}

func recordAcceptedTx(ctx context.Context, tx pgx.Tx, emailID, attemptID uuid.UUID, messageID, note string) error {
	newState := "accepted"
	if note != "" {
		newState = "resolved_accepted"
	}
	tag, err := tx.Exec(ctx, `UPDATE send_attempts SET state = $3, provider_message_id = NULLIF($4,''), finished_at = now(),
			resolution_note = $5
		WHERE id = $1 AND email_id = $2 AND state IN ('started', 'unknown')`, attemptID, emailID, newState, messageID, note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil // already settled
	}
	e, err := loadEmailInfo(ctx, tx, emailID, true)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'sent', provider_message_id = NULLIF($2,''),
			last_event = CASE WHEN last_event IN ('queued', 'delivery_unknown') THEN 'sent' ELSE last_event END,
			last_event_at = CASE WHEN last_event IN ('queued', 'delivery_unknown') THEN now() ELSE last_event_at END,
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1`, emailID, messageID); err != nil {
		return err
	}
	ids, addrs, err := recipientAddresses(ctx, tx, emailID, "queued", "delivery_unknown")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipients SET status = 'sent', status_detail = '', status_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return err
	}
	if _, err := quota.Consume(ctx, tx, e.OrgID, ids); err != nil {
		return err
	}
	if messageID != "" {
		e.ProviderMessageID = &messageID
	}
	if len(addrs) == 0 {
		return nil
	}
	return emit(ctx, tx, e, webhooks.EmailSent, addrs, time.Now(), nil)
}

func (s *Sender) recordRejected(ctx context.Context, emailID, attemptID uuid.UUID, se *provider.SendError) error {
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE send_attempts SET state = 'rejected', error_code = $2, error_message = $3, finished_at = now()
			WHERE id = $1 AND state = 'started'`, attemptID, se.Code, truncate(se.Message, 500))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		e, err := loadEmailInfo(ctx, tx, emailID, true)
		if err != nil {
			return err
		}
		return failEmail(ctx, tx, e, "provider_rejected", "The sending provider rejected the email.")
	})
}

func retryBackoff(attempt int) time.Duration {
	steps := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	if attempt-1 < len(steps) {
		return steps[max(attempt-1, 0)]
	}
	return time.Hour
}

func (s *Sender) recordRetryable(ctx context.Context, emailID, attemptID uuid.UUID, c claimed, se *provider.SendError) error {
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE send_attempts SET state = 'retryable', error_code = $2, error_message = $3, finished_at = now()
			WHERE id = $1 AND state = 'started'`, attemptID, se.Code, truncate(se.Message, 500))
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		e, err := loadEmailInfo(ctx, tx, emailID, true)
		if err != nil {
			return err
		}
		next := time.Now().Add(retryBackoff(c.attempts))
		if next.After(c.deadline) {
			if err := failEmail(ctx, tx, e, "retries_exhausted", "The provider was unavailable until the maximum queue time passed."); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'retry_wait', next_attempt_at = $2, lease_owner = NULL, lease_expires_at = NULL
				WHERE id = $1`, emailID, next); err != nil {
				return err
			}
			if err := sendOutbox(ctx, tx, e.OrgID, emailID, next); err != nil {
				return err
			}
		}
		// Accepted and exhausted attempts must both settle quota before locking
		// the route, so concurrent results cannot invert their shared lock order.
		if se.Alert {
			return recordProviderSendResult(ctx, tx, emailID, attemptID, se.Code, false)
		}
		return nil
	})
}

func (s *Sender) recordUnknown(ctx context.Context, emailID, attemptID uuid.UUID, cause error) error {
	code, msg := "unknown", "no response"
	var se *provider.SendError
	if errors.As(cause, &se) {
		code, msg = se.Code, se.Message
	}
	return db.Tx(ctx, s.Pool, func(tx pgx.Tx) error {
		return markUnknown(ctx, tx, emailID, attemptID, code, truncate(msg, 500))
	})
}

// markUnknown records an attempt whose outcome cannot be known. Quota stays
// reserved until reconciliation.
func markUnknown(ctx context.Context, tx pgx.Tx, emailID, attemptID uuid.UUID, code, msg string) error {
	tag, err := tx.Exec(ctx, `UPDATE send_attempts SET state = 'unknown', error_code = $2, error_message = $3, finished_at = now()
		WHERE id = $1 AND state = 'started'`, attemptID, code, msg)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	e, err := loadEmailInfo(ctx, tx, emailID, true)
	if err != nil {
		return err
	}
	ids, addrs, err := recipientAddresses(ctx, tx, emailID, "queued")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipients SET status = 'delivery_unknown', status_detail = $2, status_at = now() WHERE id = ANY($1)`,
		ids, "Sending outcome unknown ("+code+"). Hedvi does not retry automatically to avoid duplicates."); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'unknown', last_event = 'delivery_unknown', last_event_at = now(),
		lease_owner = NULL, lease_expires_at = NULL WHERE id = $1`, emailID); err != nil {
		return err
	}
	org := e.OrgID
	proj := e.ProjectID
	if err := raiseAlert(ctx, tx, &org, &proj, "delivery_unknown", "warning", "unknown:"+emailID.String(),
		"Send outcome unknown for email "+emailID.String()+". Waiting for SES events to reconcile; no automatic retry.",
		map[string]any{"email_id": emailID, "attempt_id": attemptID, "code": code}); err != nil {
		return err
	}
	return emit(ctx, tx, e, webhooks.EmailDeliveryUnknown, addrs, time.Now(), map[string]any{
		"delivery_unknown": map[string]string{"attempt_id": attemptID.String(), "message": "The sending provider did not confirm whether it accepted the email. Hedvi will not retry automatically."}})
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.ToValidUTF8(s, "�"), "\x00", "�")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ResolveUnknown settles an unknown attempt manually after investigation.
func ResolveUnknown(ctx context.Context, tx pgx.Tx, emailID, attemptID uuid.UUID, outcome, providerMessageID, note string, byUser uuid.UUID) error {
	var by *uuid.UUID
	if byUser != uuid.Nil {
		by = &byUser
	}
	if outcome == "accepted" {
		if err := recordAcceptedTx(ctx, tx, emailID, attemptID, providerMessageID, note); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE send_attempts SET resolved_by = $2 WHERE id = $1`, attemptID, by)
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE send_attempts SET state = 'resolved_not_sent', resolution_note = $2, resolved_by = $3
		WHERE id = $1 AND state = 'unknown'`, attemptID, note, by)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	e, err := loadEmailInfo(ctx, tx, emailID, true)
	if err != nil {
		return err
	}
	ids, addrs, err := recipientAddresses(ctx, tx, emailID, "delivery_unknown")
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE recipients SET status = 'failed', status_detail = 'Resolved as not sent after investigation', status_at = now()
		WHERE id = ANY($1)`, ids); err != nil {
		return err
	}
	if _, err := quota.Release(ctx, tx, e.OrgID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE emails SET send_state = 'failed', last_event = 'failed', last_event_at = now() WHERE id = $1`, emailID); err != nil {
		return err
	}
	return emit(ctx, tx, e, webhooks.EmailFailed, addrs, time.Now(), map[string]any{"failed": map[string]string{"reason": "Resolved as not sent after investigation."}})
}
