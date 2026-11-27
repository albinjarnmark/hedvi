package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/queue"
	"hedvi.dev/api/internal/sesevent"
	"hedvi.dev/api/internal/webhooks"
)

// HardBounceSuppression is how long a permanent bounce suppresses an
// address before it may be retried. Complaints do not expire.
const HardBounceSuppression = 90 * 24 * time.Hour

// Events applies SES events idempotently. Events can arrive twice, late and
// out of order; precedence ranks keep the most significant status.
type Events struct {
	AuthMail *authmail.Worker
	Pool     *pgxpool.Pool
	Queue    queue.Queue
	Faults   *faults.Injector
	Log      *slog.Logger
}

func (a *Events) Run(ctx context.Context) {
	for ctx.Err() == nil {
		msgs, err := a.Queue.Receive(ctx, 10, 10*time.Second)
		if err != nil {
			if ctx.Err() == nil {
				a.Log.Error("receive events", "err", err)
				time.Sleep(time.Second)
			}
			continue
		}
		for _, m := range msgs {
			ev, err := sesevent.Parse(m.Body)
			if err != nil {
				if !errors.Is(err, sesevent.ErrIgnored) {
					a.Log.Error("unparseable provider event", "message_id", m.ID, "err", err)
				}
				metrics.EventsApplied.WithLabelValues("unknown", "ignored").Inc()
				_ = a.Queue.Delete(ctx, m.Receipt)
				continue
			}
			if err := a.Apply(ctx, ev); err != nil {
				a.Log.Error("apply provider event", "type", ev.Type(), "err", err)
				continue // redelivered later; repeated failures go to the DLQ
			}
			_ = a.Queue.Delete(ctx, m.Receipt)
		}
	}
}

func dedupeKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])
}

type recipientUpdate struct {
	address     string
	status      string
	detail      string
	at          time.Time
	webhook     string
	extra       map[string]any
	suppress    string // reason, if the address must be suppressed
	expires     *time.Time
	dedupe      string
	eventType   string
	eventDetail map[string]any
}

// Apply records one SES event. It returns nil for events that do not belong
// to a known email (for example after metadata retention).
func (a *Events) Apply(ctx context.Context, ev sesevent.Event) error {
	if _, ok := ev.Mail.Tags["hedvi_auth"]; ok {
		if a.AuthMail != nil {
			return a.AuthMail.ApplyEvent(ctx, ev)
		}
		return nil
	}
	emailID, emailErr := uuid.Parse(ev.Tag("hedvi_email"))
	projectID, projectErr := uuid.Parse(ev.Tag("hedvi_project"))
	attemptID, attemptErr := uuid.Parse(ev.Tag("hedvi_attempt"))
	if emailErr != nil || projectErr != nil || attemptErr != nil || ev.Mail.MessageID == "" {
		metrics.EventsApplied.WithLabelValues(ev.Type(), "unmatched").Inc()
		return nil
	}
	matched := false
	err := db.Tx(ctx, a.Pool, func(tx pgx.Tx) error {
		// Lock attempts before emails, the same order as provider outcome settlement.
		var state string
		var attemptMessageID *string
		err := tx.QueryRow(ctx, `SELECT a.state,a.provider_message_id FROM send_attempts a JOIN emails e ON e.id=a.email_id
			WHERE a.id=$1 AND e.id=$2 AND e.project_id=$3 AND e.mode='live'
			FOR UPDATE OF a`, attemptID, emailID, projectID).Scan(&state, &attemptMessageID)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if state != "started" && state != "unknown" && state != "accepted" && state != "resolved_accepted" {
			return nil
		}
		e, err := loadEmailInfo(ctx, tx, emailID, true)
		if err != nil {
			return err
		}
		if (e.ProviderMessageID != nil && *e.ProviderMessageID != ev.Mail.MessageID) || (attemptMessageID != nil && *attemptMessageID != ev.Mail.MessageID) {
			return nil
		}
		if state == "started" || state == "unknown" {
			if err := recordAcceptedTx(ctx, tx, emailID, attemptID, ev.Mail.MessageID, "reconciled by SES "+ev.Type()+" event"); err != nil {
				return err
			}
			e.ProviderMessageID = &ev.Mail.MessageID
		}
		if state == "resolved_accepted" && e.ProviderMessageID == nil && attemptMessageID == nil {
			if _, err := tx.Exec(ctx, `UPDATE send_attempts SET provider_message_id=$2 WHERE id=$1`, attemptID, ev.Mail.MessageID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE emails SET provider_message_id=$2 WHERE id=$1`, emailID, ev.Mail.MessageID); err != nil {
				return err
			}
			e.ProviderMessageID = &ev.Mail.MessageID
		}
		matched = true
		for _, u := range updatesFor(ev) {
			if err := a.applyRecipient(ctx, tx, e, ev, u); err != nil {
				return err
			}
		}
		return a.Faults.Check(faults.EventsBeforeCommit)
	})
	if err != nil {
		return err
	}
	if matched {
		metrics.EventsApplied.WithLabelValues(ev.Type(), "applied").Inc()
	} else {
		metrics.EventsApplied.WithLabelValues(ev.Type(), "unmatched").Inc()
	}
	return nil
}

func updatesFor(ev sesevent.Event) []recipientUpdate {
	mid := ev.Mail.MessageID
	var out []recipientUpdate
	switch ev.Type() {
	case "Send":
		out = append(out, recipientUpdate{eventType: "send", at: ev.Mail.Timestamp, dedupe: dedupeKey("send", mid)})
	case "Delivery":
		d := ev.Delivery
		if d == nil {
			return nil
		}
		for _, r := range d.Recipients {
			out = append(out, recipientUpdate{address: r, status: "delivered", detail: truncate(d.SMTPResponse, 300), at: d.Timestamp,
				webhook: webhooks.EmailDelivered, eventType: "delivered", dedupe: dedupeKey("delivery", mid, strings.ToLower(r), d.Timestamp.String()),
				eventDetail: map[string]any{"smtp_response": truncate(d.SMTPResponse, 300)}})
		}
	case "Bounce":
		b := ev.Bounce
		if b == nil {
			return nil
		}
		for _, r := range b.BouncedRecipients {
			detail := fmt.Sprintf("%s: %s: %s", b.BounceType, b.BounceSubType, truncate(r.DiagnosticCode, 300))
			u := recipientUpdate{address: r.EmailAddress, status: "bounced", detail: detail, at: b.Timestamp,
				webhook: webhooks.EmailBounced, eventType: "bounced",
				dedupe: dedupeKey("bounce", mid, strings.ToLower(r.EmailAddress), b.FeedbackID),
				extra: map[string]any{"bounce": map[string]any{"type": b.BounceType, "subType": b.BounceSubType,
					"message": truncate(r.DiagnosticCode, 300), "diagnosticCode": []string{truncate(r.DiagnosticCode, 300)}}},
				eventDetail: map[string]any{"bounce_type": b.BounceType, "bounce_subtype": b.BounceSubType, "status": r.Status,
					"diagnostic_code": truncate(r.DiagnosticCode, 300)}}
			if b.BounceType == "Permanent" {
				exp := b.Timestamp.Add(HardBounceSuppression)
				u.suppress, u.expires = "hard_bounce", &exp
			}
			out = append(out, u)
		}
	case "Complaint":
		c := ev.Complaint
		if c == nil {
			return nil
		}
		for _, r := range c.ComplainedRecipients {
			out = append(out, recipientUpdate{address: r.EmailAddress, status: "complained", detail: c.ComplaintFeedbackType, at: c.Timestamp,
				webhook: webhooks.EmailComplained, eventType: "complained", suppress: "complaint",
				dedupe:      dedupeKey("complaint", mid, strings.ToLower(r.EmailAddress), c.FeedbackID),
				eventDetail: map[string]any{"feedback_type": c.ComplaintFeedbackType}})
		}
	case "DeliveryDelay":
		d := ev.DeliveryDelay
		if d == nil {
			return nil
		}
		for _, r := range d.DelayedRecipients {
			out = append(out, recipientUpdate{address: r.EmailAddress, status: "delivery_delayed", detail: d.DelayType + ": " + truncate(r.DiagnosticCode, 300),
				at: d.Timestamp, webhook: webhooks.EmailDeliveryDelayed, eventType: "delivery_delayed",
				dedupe:      dedupeKey("delay", mid, strings.ToLower(r.EmailAddress), d.Timestamp.String()),
				eventDetail: map[string]any{"delay_type": d.DelayType, "diagnostic_code": truncate(r.DiagnosticCode, 300)}})
		}
	case "Reject":
		reason := "The sending provider rejected the email after acceptance."
		for _, r := range ev.Mail.Destination {
			out = append(out, recipientUpdate{address: r, status: "failed", detail: "Rejected by provider after acceptance: " + reason,
				at: ev.Mail.Timestamp, webhook: webhooks.EmailFailed, eventType: "rejected",
				dedupe: dedupeKey("reject", mid, strings.ToLower(r)),
				extra:  map[string]any{"failed": map[string]string{"reason": reason}}, eventDetail: map[string]any{"reason": reason}})
		}
	}
	return out
}

func (a *Events) applyRecipient(ctx context.Context, tx pgx.Tx, e *emailInfo, ev sesevent.Event, u recipientUpdate) error {
	var recipientID *uuid.UUID
	var current string
	if u.address != "" {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id, status FROM recipients WHERE email_id = $1 AND normalized = lower($2) FOR UPDATE`,
			e.ID, u.address).Scan(&id, &current)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		recipientID = &id
	}
	if recipientID != nil && (current == "suppressed" || current == "test_captured" || current == "canceled") {
		return nil
	}
	if u.at.IsZero() {
		u.at = time.Now()
	}
	if u.eventDetail == nil {
		u.eventDetail = map[string]any{}
	}
	newer := Rank(u.status) > Rank(current)
	// "failed" from a post-acceptance reject outranks delivery states.
	if u.status == "failed" && current != "failed" {
		newer = true
	}
	tag, err := tx.Exec(ctx, `INSERT INTO email_events (org_id, email_id, recipient_id, type, provider_message_id, dedupe_key, occurred_at, applied, detail)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (dedupe_key) DO NOTHING`,
		e.OrgID, e.ID, recipientID, u.eventType, ev.Mail.MessageID, u.dedupe, u.at, newer || u.address == "", u.eventDetail)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		metrics.EventsApplied.WithLabelValues(ev.Type(), "duplicate").Inc()
		return nil
	}
	if u.address == "" {
		return nil
	}
	if newer {
		if _, err := tx.Exec(ctx, `UPDATE recipients SET status = $2, status_detail = $3, status_at = $4 WHERE id = $1`,
			*recipientID, u.status, u.detail, u.at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE emails SET last_event = $2, last_event_at = $3 WHERE id = $1 AND (last_event IN ('queued','sent','delivery_unknown') OR last_event_at <= $3)`,
			e.ID, u.status, u.at); err != nil {
			return err
		}
	}
	if u.suppress != "" {
		if _, err := tx.Exec(ctx, `SELECT hedvi_suppress($1,lower($2),$3,'provider_event',$4,$5,'',NULL)`,
			e.OrgID, u.address, u.suppress, e.ID.String(), u.expires); err != nil {
			return err
		}
	}
	if u.status == "complained" {
		org, proj := e.OrgID, e.ProjectID
		if err := raiseAlert(ctx, tx, &org, &proj, "complaint", "warning", "complaint:"+u.dedupe,
			"Spam complaint for project "+proj.String()+". Every complaint is reviewed.", map[string]any{"email_id": e.ID}); err != nil {
			return err
		}
	}
	return emit(ctx, tx, e, u.webhook, []string{u.address}, u.at, u.extra)
}
