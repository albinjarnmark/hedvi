// Package worker contains the background processes: outbox dispatcher, send
// worker, provider event applier, webhook delivery, reaper, domain checks,
// retention, risk controls and monitoring.
package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/webhooks"
)

// Recipient status precedence. A status only moves to a higher rank, so a
// late or duplicate event never overwrites a known bounce or complaint.
var rank = map[string]int{
	"queued": 0, "delivery_unknown": 5, "sent": 10, "delivery_delayed": 20, "delivered": 30,
	"bounced": 40, "complained": 50,
	// Terminal before sending.
	"suppressed": 100, "failed": 100, "canceled": 100, "test_captured": 100,
}

func Rank(status string) int { return rank[status] }

// emailInfo is what event handlers need to build webhook payloads.
type emailInfo struct {
	ID, OrgID, ProjectID uuid.UUID
	From                 string
	Subject              string
	Tags                 []message.Tag
	CreatedAt            time.Time
	SendState            string
	ProviderMessageID    *string
	LastEventAt          time.Time
}

func loadEmailInfo(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (*emailInfo, error) {
	q := `SELECT id, org_id, project_id, from_address, coalesce(subject, ''), tags, created_at, send_state, provider_message_id, last_event_at
		FROM emails WHERE id = $1`
	if lock {
		q += " FOR UPDATE"
	}
	var e emailInfo
	var tags []byte
	if err := tx.QueryRow(ctx, q, id).Scan(&e.ID, &e.OrgID, &e.ProjectID, &e.From, &e.Subject, &tags, &e.CreatedAt,
		&e.SendState, &e.ProviderMessageID, &e.LastEventAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(tags, &e.Tags)
	return &e, nil
}

func emit(ctx context.Context, tx pgx.Tx, e *emailInfo, typ string, to []string, at time.Time, extra map[string]any) error {
	data := accept.EventData(e.ID, e.CreatedAt, e.From, to, e.Subject, e.Tags, extra)
	if e.ProviderMessageID != nil {
		data["message_id"] = *e.ProviderMessageID
	}
	return webhooks.Emit(ctx, tx, webhooks.Event{OrgID: e.OrgID, ProjectID: e.ProjectID, Type: typ, EmailID: &e.ID, CreatedAt: at, Data: data})
}

// raiseAlert opens an alert once per dedupe key.
func raiseAlert(ctx context.Context, tx pgx.Tx, orgID *uuid.UUID, projectID *uuid.UUID, kind, severity, key, msg string, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO alerts (org_id, project_id, kind, severity, dedupe_key, message, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, orgID, projectID, kind, severity, key, msg, data)
	return err
}

// bindProviderSendRoute runs in the attempt-start transaction. Recording the
// actual immutable claim, tenant and configuration before AWS I/O also makes
// deployment/configuration changes unable to resolve a different send path.
func bindProviderSendRoute(ctx context.Context, tx pgx.Tx, c claimed, configurationSet string) (string, error) {
	parts, _ := json.Marshal([]string{c.projectID.String(), c.domainID.String(), c.fromDomain, c.tenant, configurationSet})
	sum := sha256.Sum256(parts)
	id := hex.EncodeToString(sum[:])
	_, err := tx.Exec(ctx, `INSERT INTO provider_send_routes(id,org_id,project_id,domain_id,from_domain,tenant_name,configuration_set)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, id, c.orgID, c.projectID, c.domainID, c.fromDomain, c.tenant, configurationSet)
	return id, err
}

// recordProviderSendResult advances only the newest attempt's observation for
// one path. The database start time and UUID tie-breaker order attempts, not the
// arrival time of their responses. Its row lock serializes concurrent results
// and the alert transition in the enclosing email-settlement transaction.
func recordProviderSendResult(ctx context.Context, tx pgx.Tx, emailID, attemptID uuid.UUID, code string, accepted bool) error {
	state, outcome := "retryable", "failure"
	if accepted {
		state, outcome = "accepted", "accepted"
	}
	var routeID string
	var org, project, domain uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE provider_send_routes r SET last_attempt_started_at=a.started_at,
		last_attempt_id=a.id,last_outcome=$4,updated_at=now()
		FROM send_attempts a JOIN emails e ON e.id=a.email_id
		WHERE a.id=$1 AND a.email_id=$2 AND a.state=$3 AND a.provider_route_id=r.id
		AND r.org_id=e.org_id AND r.project_id=e.project_id AND r.domain_id=e.domain_id AND r.from_domain=e.from_domain
		AND (r.last_attempt_started_at IS NULL OR (r.last_attempt_started_at,r.last_attempt_id)<(a.started_at,a.id))
		RETURNING r.id,r.org_id,r.project_id,r.domain_id`, attemptID, emailID, state, outcome).Scan(&routeID, &org, &project, &domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // stale/duplicate response, legacy attempt, or retired claim
	}
	if err != nil {
		return err
	}
	key := "provider-send:" + routeID
	if accepted {
		_, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now(),data=data||$2::jsonb
			WHERE dedupe_key=$1 AND resolved_at IS NULL`, key,
			map[string]any{"recovered_by_attempt_id": attemptID, "recovery": "provider_accepted"})
		return err
	}
	return raiseAlert(ctx, tx, &org, &project, "provider_sending_paused", "critical", key,
		"SES refused this project's sending path. Queued emails retry automatically until their queue deadline. Check this domain's provider permissions, tenant and configuration set.",
		map[string]any{"domain_id": domain, "email_id": emailID, "attempt_id": attemptID, "code": truncate(code, 100)})
}

func audit(ctx context.Context, tx pgx.Tx, orgID *uuid.UUID, action, targetType, targetID string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_kind, action, target_type, target_id, metadata)
		VALUES ($1, 'system', $2, $3, $4, $5)`, orgID, action, targetType, targetID, meta)
	return err
}

// loop runs fn every interval until ctx ends, logging errors.
func loop(ctx context.Context, log *slog.Logger, name string, interval time.Duration, fn func(context.Context) error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			log.Error("worker loop", "loop", name, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func sendOutbox(ctx context.Context, tx pgx.Tx, orgID, emailID uuid.UUID, at time.Time) error {
	msg, _ := json.Marshal(map[string]string{"email_id": emailID.String()})
	_, err := tx.Exec(ctx, `INSERT INTO outbox (org_id, topic, message, available_at) VALUES ($1, 'send', $2, $3)`, orgID, msg, at)
	return err
}

func recipientAddresses(ctx context.Context, tx pgx.Tx, emailID uuid.UUID, statuses ...string) ([]uuid.UUID, []string, error) {
	rows, err := tx.Query(ctx, `SELECT id, address FROM recipients WHERE email_id = $1 AND status = ANY($2) ORDER BY id`, emailID, statuses)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	var addrs []string
	for rows.Next() {
		var id uuid.UUID
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		addrs = append(addrs, a)
	}
	return ids, addrs, rows.Err()
}
