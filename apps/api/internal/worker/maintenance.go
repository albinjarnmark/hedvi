package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/webhooks"
)

// Reaper recovers jobs from crashed workers and enforces queue deadlines.
type Reaper struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// StaleAfter is how long a due email may sit without progress before
	// its send job is republished (for example after a lost queue message).
	StaleAfter time.Duration
	// UnknownEscalation is when an unresolved unknown attempt becomes critical.
	UnknownEscalation time.Duration
}

func (r *Reaper) RunOnce(ctx context.Context) error {
	// 1. Expired leases. An attempt that started is ambiguous: SES may have
	// accepted it. Without a started attempt the provider was never called.
	err := db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM emails WHERE send_state = 'sending' AND lease_expires_at < now()
			ORDER BY lease_expires_at LIMIT 100 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, id := range ids {
			var attemptID uuid.UUID
			err := tx.QueryRow(ctx, `SELECT id FROM send_attempts WHERE email_id = $1 AND state = 'started' ORDER BY started_at DESC LIMIT 1`, id).Scan(&attemptID)
			switch {
			case err == nil:
				if err := markUnknown(ctx, tx, id, attemptID, "worker_lost", "The worker stopped after starting the send attempt."); err != nil {
					return err
				}
			case db.IsNoRows(err):
				var orgID uuid.UUID
				if err := tx.QueryRow(ctx, `UPDATE emails SET send_state = 'queued', next_attempt_at = now(), lease_owner = NULL,
					lease_expires_at = NULL WHERE id = $1 RETURNING org_id`, id).Scan(&orgID); err != nil {
					return err
				}
				if err := sendOutbox(ctx, tx, orgID, id, time.Now()); err != nil {
					return err
				}
			default:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("expired leases: %w", err)
	}

	// 2. Due emails without progress get their job republished. Duplicates
	// are harmless because the claim is conditional.
	err = db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE emails SET next_attempt_at = now()
			WHERE id IN (SELECT id FROM emails WHERE send_state IN ('queued', 'retry_wait')
			             AND next_attempt_at < now() - $1 * interval '1 second' LIMIT 500 FOR UPDATE SKIP LOCKED)
			RETURNING id, org_id`, r.StaleAfter.Seconds())
		if err != nil {
			return err
		}
		type pair struct{ id, org uuid.UUID }
		var list []pair
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.id, &p.org); err != nil {
				rows.Close()
				return err
			}
			list = append(list, p)
		}
		rows.Close()
		for _, p := range list {
			if err := sendOutbox(ctx, tx, p.org, p.id, time.Now()); err != nil {
				return err
			}
		}
		if len(list) > 0 {
			r.Log.Warn("republished stale send jobs", "count", len(list))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("stale jobs: %w", err)
	}

	// 3. Held emails resume when the project is approved again.
	err = db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE emails e SET send_state = 'queued', next_attempt_at = now()
			WHERE e.id IN (SELECT h.id FROM emails h JOIN projects p ON p.id=h.project_id JOIN organizations o ON o.id=h.org_id
			  WHERE h.send_state='held' AND p.sending_status='approved' AND o.risk_status IN ('new','approved')
			  ORDER BY h.created_at,h.id LIMIT 100 FOR UPDATE OF h SKIP LOCKED)
			RETURNING e.id, e.org_id`)
		if err != nil {
			return err
		}
		type pair struct{ id, org uuid.UUID }
		var list []pair
		for rows.Next() {
			var p pair
			if err := rows.Scan(&p.id, &p.org); err != nil {
				rows.Close()
				return err
			}
			list = append(list, p)
		}
		rows.Close()
		for _, p := range list {
			if err := sendOutbox(ctx, tx, p.org, p.id, time.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("resume held: %w", err)
	}

	// 4. Queue deadline: accepted but never sent within the maximum queue
	// time. Quota is released and the customer is told.
	err = db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM emails WHERE send_state IN ('queued', 'retry_wait', 'held')
			AND queue_deadline_at < now() LIMIT 100 FOR UPDATE SKIP LOCKED`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			return err
		}
		for _, id := range ids {
			e, err := loadEmailInfo(ctx, tx, id, false)
			if err != nil {
				return err
			}
			if err := failEmail(ctx, tx, e, "queue_deadline", "The email waited longer than the maximum queue time and was not sent."); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("queue deadline: %w", err)
	}

	// 5. Unknown attempts that SES events have not reconciled escalate.
	return db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT a.id, a.email_id, e.org_id, e.project_id FROM send_attempts a JOIN emails e ON e.id = a.email_id
			WHERE a.state = 'unknown' AND a.finished_at < now() - $1 * interval '1 second' LIMIT 100`, r.UnknownEscalation.Seconds())
		if err != nil {
			return err
		}
		type row struct{ attempt, email, org, project uuid.UUID }
		var list []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.attempt, &x.email, &x.org, &x.project); err != nil {
				rows.Close()
				return err
			}
			list = append(list, x)
		}
		rows.Close()
		for _, x := range list {
			if err := raiseAlert(ctx, tx, &x.org, &x.project, "delivery_unknown_unresolved", "critical", "unknown-escalated:"+x.attempt.String(),
				"No SES event reconciled attempt "+x.attempt.String()+". Investigate in SES (message tags hedvi_attempt) and resolve manually. Do not resend blindly.",
				map[string]any{"email_id": x.email, "attempt_id": x.attempt}); err != nil {
				return err
			}
		}
		return nil
	})
}

// DomainChecker polls DNS for pending and verified domains.
type DomainChecker struct {
	Pool    *pgxpool.Pool
	Domains *domains.Service
	Log     *slog.Logger
}

func (d *DomainChecker) RunOnce(ctx context.Context) error {
	var due []*domains.Domain
	err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `UPDATE domains SET next_check_at = now() + interval '5 minutes'
			WHERE id IN (SELECT id FROM domains WHERE deleted_at IS NULL AND next_check_at <= now()
			             ORDER BY next_check_at LIMIT 20 FOR UPDATE SKIP LOCKED)
			RETURNING `+domains.DomainColumns)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			dom, err := domains.ScanDomain(rows)
			if err != nil {
				return err
			}
			due = append(due, dom)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	// Slow DNS for one claim should not hold up every other due claim.
	// The lease transaction above has already returned its connection.
	jobs := make(chan *domains.Domain, len(due))
	for _, dom := range due {
		jobs <- dom
	}
	close(jobs)
	var wg sync.WaitGroup
	for range min(4, len(due)) {
		wg.Go(func() {
			for dom := range jobs {
				if ctx.Err() != nil {
					return
				}
				if _, err := d.Domains.CheckNow(ctx, d.Pool, dom); err != nil {
					d.Log.Warn("domain check", "domain_id", dom.ID, "err", err)
				}
			}
		})
	}
	wg.Wait()
	return ctx.Err()
}

// Retention deletes content and metadata on schedule and sweeps orphans.
type Retention struct {
	Pool  *pgxpool.Pool
	Blobs blob.Store
	Log   *slog.Logger
	// OrphanAge protects payloads of requests that are still committing.
	OrphanAge time.Duration
}

var terminalStates = []string{"captured", "sent", "failed", "skipped", "unknown", "canceled"}

func (r *Retention) RunOnce(ctx context.Context) error {
	// Forget the content under short row locks. Immutable payloads are deleted
	// only after commit; the orphan sweep retries failed or interrupted deletions.
	for {
		var rows []struct {
			id  uuid.UUID
			ref *string
		}
		err := db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
			q, err := tx.Query(ctx, `SELECT id, payload_ref FROM emails WHERE content_deleted_at IS NULL AND content_expires_at <= now()
				AND send_state = ANY($1) ORDER BY project_id, id LIMIT 200 FOR UPDATE SKIP LOCKED`, terminalStates)
			if err != nil {
				return err
			}
			for q.Next() {
				var x struct {
					id  uuid.UUID
					ref *string
				}
				if err := q.Scan(&x.id, &x.ref); err != nil {
					q.Close()
					return err
				}
				rows = append(rows, x)
			}
			q.Close()
			if err := q.Err(); err != nil {
				return err
			}
			for _, x := range rows {
				if _, err := tx.Exec(ctx, `UPDATE emails SET payload_ref = NULL, subject = NULL, content_deleted_at = now() WHERE id = $1`, x.id); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("content retention: %w", err)
		}
		var firstDeleteErr error
		for _, x := range rows {
			if x.ref != nil {
				if err := r.Blobs.Delete(ctx, *x.ref); err != nil && firstDeleteErr == nil {
					firstDeleteErr = err
				}
			}
		}
		if firstDeleteErr != nil {
			return fmt.Errorf("delete expired payload: %w", firstDeleteErr)
		}
		if len(rows) < 200 {
			break
		}
	}

	// Metadata: emails cascade to recipients, events, attempts, reservations.
	stmts := []string{
		`DELETE FROM emails WHERE id IN (SELECT id FROM emails WHERE metadata_expires_at <= now()
			AND content_deleted_at IS NOT NULL AND send_state = ANY($1) AND send_state <> 'unknown'
			AND NOT EXISTS(SELECT 1 FROM recipients r WHERE r.email_id=emails.id AND r.billing_state='reserved')
			AND NOT EXISTS(SELECT 1 FROM send_attempts a WHERE a.email_id=emails.id AND a.state IN ('started','unknown'))
			LIMIT 1000)`,
	}
	for _, s := range stmts {
		if _, err := r.Pool.Exec(ctx, s, terminalStates); err != nil {
			return fmt.Errorf("metadata retention: %w", err)
		}
	}
	for _, s := range []string{
		`DELETE FROM idempotency_records WHERE expires_at <= now()`,
		`DELETE FROM outbox WHERE published_at < now() - interval '7 days'`,
		`DELETE FROM sessions WHERE expires_at < now() - interval '1 day' OR revoked_at < now() - interval '1 day'`,
		`DELETE FROM send_counters WHERE window_start < now() - interval '2 days'`,
		`DELETE FROM alerts WHERE resolved_at < now() - interval '90 days'`,
		`DELETE FROM audit_log WHERE created_at < now() - interval '400 days'`,
		`DELETE FROM webhook_events ev USING organizations o WHERE o.id = ev.org_id AND ev.created_at < now() - make_interval(days =>
			LEAST(coalesce(o.metadata_retention_days, 90), CASE hedvi_effective_plan(o.id,statement_timestamp()) WHEN 'free' THEN 14 WHEN 'flex' THEN 30 ELSE 90 END))`,
	} {
		if _, err := r.Pool.Exec(ctx, s); err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}
	}
	if err := db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		_, err := quota.ExpireLots(ctx, tx, time.Now())
		return err
	}); err != nil {
		return fmt.Errorf("expire credits: %w", err)
	}
	return nil
}

// SweepOrphans deletes payload objects without an accepted email. Objects
// younger than OrphanAge are skipped so in-flight acceptances are safe.
func (r *Retention) SweepOrphans(ctx context.Context) (int, error) {
	deleted := 0
	var firstDeleteErr error
	err := r.Blobs.List(ctx, blob.PayloadPrefix, time.Now().Add(-r.OrphanAge), func(key string) error {
		var exists bool
		if err := r.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM emails WHERE payload_ref = $1)`, key).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if err := r.Blobs.Delete(ctx, key); err != nil {
			if firstDeleteErr == nil {
				firstDeleteErr = err
			}
			return nil
		}
		deleted++
		return nil
	})
	if deleted > 0 {
		r.Log.Info("deleted orphan payloads", "count", deleted)
	}
	return deleted, errors.Join(err, firstDeleteErr)
}

// Risk pauses projects whose bounce or complaint rates exceed internal
// thresholds (docs/operations/risk-policy.md). These are Hedvi's own risk
// choices, not AWS guarantees.
type Risk struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

const (
	HardBounceRate           = 0.03
	HardBounceMinSample      = 200
	ComplaintRate            = 0.0005
	ComplaintMinSample       = 2000
	SmallSampleMaxComplaints = 2
)

func (r *Risk) RunOnce(ctx context.Context) error {
	return db.Tx(ctx, r.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT e.org_id, e.project_id, p.status_changed_at,
				count(*) FILTER (WHERE r.status IN ('sent', 'delivered', 'delivery_delayed', 'bounced', 'complained')),
				count(*) FILTER (WHERE r.status = 'bounced' AND r.status_detail LIKE 'Permanent:%'),
				count(*) FILTER (WHERE r.status = 'complained')
			FROM recipients r JOIN emails e ON e.id = r.email_id
			JOIN projects p ON p.id = e.project_id AND p.sending_status = 'approved'
			WHERE e.mode = 'live' AND e.created_at >= greatest(now() - interval '30 days',p.status_changed_at)
			GROUP BY 1, 2, 3 ORDER BY 1, 2`)
		if err != nil {
			return err
		}
		type stat struct {
			org, project           uuid.UUID
			reviewedAt             time.Time
			sent, hard, complaints int64
		}
		var stats []stat
		for rows.Next() {
			var s stat
			if err := rows.Scan(&s.org, &s.project, &s.reviewedAt, &s.sent, &s.hard, &s.complaints); err != nil {
				rows.Close()
				return err
			}
			stats = append(stats, s)
		}
		rows.Close()
		for _, s := range stats {
			reason := ""
			switch {
			case s.sent >= HardBounceMinSample && float64(s.hard)/float64(s.sent) > HardBounceRate:
				reason = fmt.Sprintf("Hard bounce rate %.1f%% over the last 30 days (%d of %d) exceeds 3%%.", 100*float64(s.hard)/float64(s.sent), s.hard, s.sent)
			case s.sent >= ComplaintMinSample && float64(s.complaints)/float64(s.sent) > ComplaintRate:
				reason = fmt.Sprintf("Complaint rate %.3f%% over the last 30 days (%d of %d) exceeds 0.05%%.", 100*float64(s.complaints)/float64(s.sent), s.complaints, s.sent)
			case s.sent < ComplaintMinSample && s.complaints >= SmallSampleMaxComplaints:
				reason = fmt.Sprintf("%d spam complaints from %d recipients in the last 30 days. Small samples are paused for manual review.", s.complaints, s.sent)
			}
			if reason == "" {
				continue
			}
			if err := pauseProject(ctx, tx, s.org, s.project, s.reviewedAt, reason); err != nil {
				return err
			}
			r.Log.Warn("project paused by risk policy", "project_id", s.project, "reason", reason)
		}
		return nil
	})
}

func pauseProject(ctx context.Context, tx pgx.Tx, orgID, projectID uuid.UUID, reviewedAt time.Time, reason string) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE projects SET sending_status = 'paused', status_reason = $2, status_changed_at = now()
		WHERE id = $1 AND sending_status = 'approved' AND status_changed_at=$3`, projectID, reason, reviewedAt)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := audit(ctx, tx, &orgID, "project.paused", "project", projectID.String(), map[string]any{"reason": reason}); err != nil {
		return err
	}
	if err := raiseAlert(ctx, tx, &orgID, &projectID, "project_paused", "critical", "paused:"+projectID.String()+":"+time.Now().Format("2006-01-02"),
		"Project paused: "+reason, nil); err != nil {
		return err
	}
	return webhooks.Emit(ctx, tx, webhooks.Event{OrgID: orgID, ProjectID: projectID, Type: webhooks.ProjectPaused,
		Data: map[string]any{"project_id": projectID.String(), "reason": reason}})
}

// Monitor refreshes gauges and forwards alerts to an independent channel.
type Monitor struct {
	Pool       *pgxpool.Pool
	Log        *slog.Logger
	Publisher  AlertPublisher
	AlertURL   string
	HTTPClient *http.Client
}

func (m *Monitor) RunOnce(ctx context.Context) error {
	var oldest float64
	var unknown int64
	if err := m.Pool.QueryRow(ctx, `SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0) FROM emails
		WHERE send_state IN ('queued', 'retry_wait', 'sending')`).Scan(&oldest); err != nil {
		return err
	}
	metrics.OldestQueuedSeconds.Set(oldest)
	if err := m.Pool.QueryRow(ctx, `SELECT count(*) FROM send_attempts WHERE state = 'unknown'`).Scan(&unknown); err != nil {
		return err
	}
	metrics.UnknownAttempts.Set(float64(unknown))
	rows, err := m.Pool.Query(ctx, `SELECT topic, count(*) FROM outbox WHERE published_at IS NULL AND available_at <= now() GROUP BY topic`)
	if err != nil {
		return err
	}
	metrics.OutboxBacklog.Reset()
	for rows.Next() {
		var topic string
		var n float64
		if err := rows.Scan(&topic, &n); err != nil {
			rows.Close()
			return err
		}
		metrics.OutboxBacklog.WithLabelValues(topic).Set(n)
	}
	rows.Close()
	sev, err := m.Pool.Query(ctx, `SELECT severity, count(*) FROM alerts WHERE resolved_at IS NULL GROUP BY severity`)
	if err != nil {
		return err
	}
	metrics.OpenAlerts.Reset()
	for sev.Next() {
		var s string
		var n float64
		if err := sev.Scan(&s, &n); err != nil {
			sev.Close()
			return err
		}
		metrics.OpenAlerts.WithLabelValues(s).Set(n)
	}
	sev.Close()

	if oldest > 300 {
		_ = db.Tx(ctx, m.Pool, func(tx pgx.Tx) error {
			return raiseAlert(ctx, tx, nil, nil, "queue_age", "critical", "queue-age:"+time.Now().Format("2006-01-02T15"),
				fmt.Sprintf("Oldest queued email has waited %.0f seconds.", oldest), nil)
		})
	}
	return m.forward(ctx)
}

// forward sends new warning/critical alerts to SNS or an ops webhook. Failed
// publishes remain pending for retry. Without either channel, alerts are logged.
func (m *Monitor) forward(ctx context.Context) error {
	rows, err := m.Pool.Query(ctx, `SELECT id, kind, severity, message, org_id, project_id, created_at FROM alerts
		WHERE notified_at IS NULL AND resolved_at IS NULL AND severity IN ('warning', 'critical') ORDER BY created_at LIMIT 50`)
	if err != nil {
		return err
	}
	type alert struct {
		ID        uuid.UUID  `json:"id"`
		Kind      string     `json:"kind"`
		Severity  string     `json:"severity"`
		Message   string     `json:"message"`
		OrgID     *uuid.UUID `json:"org_id"`
		ProjectID *uuid.UUID `json:"project_id"`
		CreatedAt time.Time  `json:"created_at"`
	}
	var list []alert
	for rows.Next() {
		var a alert
		if err := rows.Scan(&a.ID, &a.Kind, &a.Severity, &a.Message, &a.OrgID, &a.ProjectID, &a.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		list = append(list, a)
	}
	rows.Close()
	for _, a := range list {
		if m.Publisher != nil {
			body, _ := json.Marshal(a)
			if err := m.Publisher.PublishAlert(ctx, string(body)); err != nil {
				return fmt.Errorf("forward alert: %w", err)
			}
		} else if m.AlertURL == "" {
			m.Log.Error("ALERT", "kind", a.Kind, "severity", a.Severity, "message", a.Message, "alert_id", a.ID)
		} else {
			body, _ := json.Marshal(map[string]any{"text": fmt.Sprintf("[%s] %s: %s", a.Severity, a.Kind, a.Message), "alert": a})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.AlertURL, bytes.NewReader(body))
			if err != nil {
				return errors.New("invalid operations alert webhook URL")
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := m.HTTPClient.Do(req)
			if err != nil {
				return fmt.Errorf("forward alert: %w", err)
			}
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				return fmt.Errorf("forward alert: HTTP %d", resp.StatusCode)
			}
		}
		if _, err := m.Pool.Exec(ctx, `UPDATE alerts SET notified_at = now() WHERE id = $1`, a.ID); err != nil {
			return err
		}
	}
	return nil
}

var _ = plans.MB
