// Package webhooks records, signs and delivers customer webhooks.
//
// Delivery is at least once. Each event has a stable id (msg_…) that is kept
// across automatic retries and manual resends, so receivers can deduplicate.
// Signatures follow Standard Webhooks, which is what Resend's SDK verifies.
package webhooks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/secure"
)

// Event types. The email.* and domain.* types follow Resend's names; the
// hedvi.* types are Hedvi extensions and are only sent when subscribed.
const (
	EmailSent            = "email.sent"
	EmailDelivered       = "email.delivered"
	EmailDeliveryDelayed = "email.delivery_delayed"
	EmailBounced         = "email.bounced"
	EmailComplained      = "email.complained"
	EmailFailed          = "email.failed"
	EmailSuppressed      = "email.suppressed"
	EmailDeliveryUnknown = "hedvi.email.delivery_unknown"
	QuotaWarning         = "hedvi.quota.warning"
	ProjectPaused        = "hedvi.project.paused"
	DomainUpdated        = "domain.updated"
)

var AllTypes = []string{EmailSent, EmailDelivered, EmailDeliveryDelayed, EmailBounced, EmailComplained,
	EmailFailed, EmailSuppressed, EmailDeliveryUnknown, QuotaWarning, ProjectPaused, DomainUpdated}

func ValidType(t string) bool {
	for _, a := range AllTypes {
		if a == t {
			return true
		}
	}
	return false
}

type Event struct {
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Type      string
	EmailID   *uuid.UUID
	Data      map[string]any
	CreatedAt time.Time
}

// Payload is the JSON body delivered to receivers.
type Payload struct {
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	Data      map[string]any `json:"data"`
}

func NewEventID() string { return "msg_" + secure.RandomToken(16) }

// Emit stores the event and schedules one delivery per subscribed endpoint,
// inside the caller's transaction. Without subscribers nothing is stored.
func Emit(ctx context.Context, tx pgx.Tx, ev Event) error {
	rows, err := tx.Query(ctx, `SELECT id FROM webhooks
		WHERE project_id = $1 AND org_id = $2 AND status = 'enabled' AND $3 = ANY(events)`, ev.ProjectID, ev.OrgID, ev.Type)
	if err != nil {
		return err
	}
	var hooks []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		hooks = append(hooks, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(hooks) == 0 {
		return err
	}
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = time.Now()
	}
	body, err := json.Marshal(Payload{Type: ev.Type, CreatedAt: ev.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Data: ev.Data})
	if err != nil {
		return err
	}
	id := NewEventID()
	if _, err := tx.Exec(ctx, `INSERT INTO webhook_events (id, org_id, project_id, type, email_id, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, ev.OrgID, ev.ProjectID, ev.Type, ev.EmailID, body, ev.CreatedAt); err != nil {
		return err
	}
	for _, hook := range hooks {
		if err := ScheduleDelivery(ctx, tx, ev.OrgID, hook, id, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// ScheduleDelivery creates (or re-arms) a delivery and its outbox job.
func ScheduleDelivery(ctx context.Context, tx pgx.Tx, orgID, webhookID uuid.UUID, eventID string, at time.Time) error {
	var deliveryID uuid.UUID
	err := tx.QueryRow(ctx, `INSERT INTO webhook_deliveries (org_id, webhook_id, event_id, next_attempt_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (webhook_id, event_id) DO UPDATE
		SET state = 'pending', next_attempt_at = EXCLUDED.next_attempt_at, attempt_count = 0,
		    manual_resends = webhook_deliveries.manual_resends + 1
		WHERE webhook_deliveries.state <> 'delivering'
		RETURNING id`, orgID, webhookID, eventID, at).Scan(&deliveryID)
	if db.IsNoRows(err) {
		return apierr.New(409, "conflict", "This webhook is already being delivered.")
	}
	if err != nil {
		return err
	}
	return EnqueueDelivery(ctx, tx, orgID, deliveryID, at)
}

func EnqueueDelivery(ctx context.Context, tx pgx.Tx, orgID, deliveryID uuid.UUID, at time.Time) error {
	msg, _ := json.Marshal(map[string]string{"delivery_id": deliveryID.String()})
	_, err := tx.Exec(ctx, `INSERT INTO outbox (org_id, topic, message, available_at) VALUES ($1, 'webhook', $2, $3)`, orgID, msg, at)
	return err
}
