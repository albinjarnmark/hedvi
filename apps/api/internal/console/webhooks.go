package console

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/netsafe"
	"hedvi.dev/api/internal/webhooks"
)

func (s *Server) netOptions() netsafe.Options {
	return netsafe.Options{AllowPrivate: s.Cfg.WebhookAllowPrivate, AllowHTTP: s.Cfg.WebhookAllowHTTP}
}

func validEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return []string{webhooks.EmailSent, webhooks.EmailDelivered, webhooks.EmailDeliveryDelayed, webhooks.EmailBounced,
			webhooks.EmailComplained, webhooks.EmailFailed, webhooks.EmailSuppressed}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if !webhooks.ValidType(e) {
			return nil, apierr.New(422, "validation_error", "Unknown event type %q.", e)
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Server) listWebhooks(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT w.id, w.url, w.events, w.status, w.created_at,
				count(d.*) FILTER (WHERE d.state = 'failed' AND d.created_at > now() - interval '7 days'),
				count(d.*) FILTER (WHERE d.state = 'succeeded' AND d.created_at > now() - interval '7 days')
			FROM webhooks w LEFT JOIN webhook_deliveries d ON d.webhook_id = w.id
			WHERE w.project_id = $1 GROUP BY w.id ORDER BY w.created_at`, a.ProjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var url, status string
			var events []string
			var at time.Time
			var failed, ok int
			if err := rows.Scan(&id, &url, &events, &status, &at, &failed, &ok); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "url": url, "events": events, "status": status, "created_at": ts(at),
				"failed_7d": failed, "succeeded_7d": ok})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out, "event_types": webhooks.AllTypes})
}

func (s *Server) createWebhook(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := netsafe.ValidateURL(strings.TrimSpace(req.URL), s.netOptions())
	if err != nil {
		s.fail(w, r, apierr.New(422, "validation_error", "Webhook URL rejected: %s", err))
		return
	}
	events, err := validEvents(req.Events)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	id := uuid.Must(uuid.NewV7())
	secret := webhooks.NewSecret()
	enc, err := s.Sealer.Seal([]byte(secret), webhooks.SecretAAD(id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		// Serialize count-and-insert so concurrent sessions cannot exceed the
		// endpoint cap and multiply delivery work for every email event.
		var locked uuid.UUID
		if err := tx.QueryRow(r.Context(), `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, a.OrgID).Scan(&locked); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, a.ProjectID).Scan(&locked); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM webhooks WHERE project_id = $1`, a.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= 10 {
			return apierr.New(422, "validation_error", "A project can have at most 10 webhook endpoints.")
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO webhooks (id, org_id, project_id, url, events, secret_enc) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, a.OrgID, a.ProjectID, u.String(), events, enc); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, a, "webhook.created", "webhook", id.String(), map[string]any{"host": u.Host}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "url": u.String(), "events": events, "status": "enabled", "signing_secret": secret})
}

func (s *Server) updateWebhook(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		URL    *string  `json:"url"`
		Events []string `json:"events"`
		Status *string  `json:"status"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	var url *string
	if req.URL != nil {
		u, err := netsafe.ValidateURL(strings.TrimSpace(*req.URL), s.netOptions())
		if err != nil {
			s.fail(w, r, apierr.New(422, "validation_error", "Webhook URL rejected: %s", err))
			return
		}
		v := u.String()
		url = &v
	}
	var events []string
	if req.Events != nil {
		if events, err = validEvents(req.Events); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if req.Status != nil && *req.Status != "enabled" && *req.Status != "disabled" {
		s.fail(w, r, apierr.New(422, "validation_error", "`status` must be enabled or disabled."))
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE webhooks SET url = coalesce($3, url), events = coalesce($4, events), status = coalesce($5, status)
			WHERE id = $1 AND project_id = $2`, id, a.ProjectID, url, events, req.Status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.ErrNotFound
		}
		return s.audit(r.Context(), tx, a, "webhook.updated", "webhook", id.String(), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "updated": true})
}

func (s *Server) deleteWebhook(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `DELETE FROM webhooks WHERE id = $1 AND project_id = $2`, id, a.ProjectID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.ErrNotFound
		}
		return s.audit(r.Context(), tx, a, "webhook.deleted", "webhook", id.String(), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "deleted": true})
}

func (s *Server) revealSecret(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var enc []byte
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT secret_enc FROM webhooks WHERE id = $1 AND project_id = $2`, id, a.ProjectID).Scan(&enc); err != nil {
			if db.IsNoRows(err) {
				return apierr.ErrNotFound
			}
			return err
		}
		return s.audit(r.Context(), tx, a, "webhook.secret_revealed", "webhook", id.String(), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	secret, err := s.Sealer.Open(enc, webhooks.SecretAAD(id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"signing_secret": string(secret)})
}

func (s *Server) rotateSecret(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	secret := webhooks.NewSecret()
	enc, err := s.Sealer.Seal([]byte(secret), webhooks.SecretAAD(id))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE webhooks SET secret_enc = $3 WHERE id = $1 AND project_id = $2`, id, a.ProjectID, enc)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.ErrNotFound
		}
		return s.audit(r.Context(), tx, a, "webhook.secret_rotated", "webhook", id.String(), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"signing_secret": secret})
}

// testWebhook sends a signed test event to one endpoint.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	eventID := webhooks.NewEventID()
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id = $1 AND project_id = $2)`, id, a.ProjectID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return apierr.ErrNotFound
		}
		if err := s.manualWebhookBudget(r, tx, a, id); err != nil {
			return err
		}
		payload := map[string]any{"type": "hedvi.test", "created_at": time.Now().UTC().Format(time.RFC3339Nano),
			"data": map[string]any{"message": "Test event from the Hedvi dashboard.", "project_id": a.ProjectID}}
		if _, err := tx.Exec(r.Context(), `INSERT INTO webhook_events (id, org_id, project_id, type, payload) VALUES ($1, $2, $3, 'hedvi.test', $4)`,
			eventID, a.OrgID, a.ProjectID, payload); err != nil {
			return err
		}
		return webhooks.ScheduleDelivery(r.Context(), tx, a.OrgID, id, eventID, time.Now())
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"event_id": eventID})
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []map[string]any{}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT d.id, d.event_id, e.type, d.state, d.attempt_count, d.next_attempt_at, d.last_attempt_at,
				d.last_status, d.last_response, d.last_error, d.manual_resends, d.created_at
			FROM webhook_deliveries d JOIN webhook_events e ON e.id = d.event_id
			JOIN webhooks w ON w.id = d.webhook_id AND w.project_id = $2
			WHERE d.webhook_id = $1 ORDER BY d.created_at DESC LIMIT 100`, id, a.ProjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var did uuid.UUID
			var eid, typ, state, resp, lastErr string
			var attempts, resends int
			var next, created time.Time
			var last *time.Time
			var status *int
			if err := rows.Scan(&did, &eid, &typ, &state, &attempts, &next, &last, &status, &resp, &lastErr, &resends, &created); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": did, "event_id": eid, "type": typ, "state": state, "attempts": attempts,
				"next_attempt_at": ts(next), "last_attempt_at": last, "last_status": status, "last_response": resp, "last_error": lastErr,
				"manual_resends": resends, "created_at": ts(created)})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// resendDelivery re-sends the same event (same id) to the endpoint. It is
// separate from resending an email.
func (s *Server) resendDelivery(w http.ResponseWriter, r *http.Request, a *access) {
	hookID, err1 := pathUUID(r, "id")
	deliveryID, err2 := pathUUID(r, "delivery")
	if err1 != nil || err2 != nil {
		s.fail(w, r, apierr.ErrNotFound)
		return
	}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var eventID string
		err := tx.QueryRow(r.Context(), `SELECT d.event_id FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id
			WHERE d.id = $1 AND d.webhook_id = $2 AND w.project_id = $3`, deliveryID, hookID, a.ProjectID).Scan(&eventID)
		if db.IsNoRows(err) {
			return apierr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := s.manualWebhookBudget(r, tx, a, hookID); err != nil {
			return err
		}
		if err := webhooks.ScheduleDelivery(r.Context(), tx, a.OrgID, hookID, eventID, time.Now()); err != nil {
			return err
		}
		return s.audit(r.Context(), tx, a, "webhook.delivery_resent", "webhook_delivery", deliveryID.String(), map[string]any{"event_id": eventID}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 202, map[string]any{"id": deliveryID, "resent": true})
}

// The database-backed budget is shared by replicas and covers both test events
// and manual retries. Normal provider events are not charged to this budget.
func (s *Server) manualWebhookBudget(r *http.Request, tx pgx.Tx, a *access, hookID uuid.UUID) error {
	if _, err := tx.Exec(r.Context(), `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, a.OrgID); err != nil {
		return err
	}
	var total, endpoint, pending int
	if err := tx.QueryRow(r.Context(), `SELECT count(*), count(*) FILTER (WHERE target_id = $2) FROM audit_log WHERE org_id = $1 AND action = 'webhook.manual_requested' AND created_at > now() - interval '1 hour'`, a.OrgID, hookID.String()).Scan(&total, &endpoint); err != nil {
		return err
	}
	if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM webhook_deliveries WHERE org_id = $1 AND state IN ('pending','delivering')`, a.OrgID).Scan(&pending); err != nil {
		return err
	}
	if total >= 100 || endpoint >= 20 || pending >= 500 {
		return apierr.New(429, "rate_limit_exceeded", "Manual webhook limit reached (20 per endpoint and 100 per organization per hour), or too many deliveries are pending. Wait before retrying.")
	}
	return s.audit(r.Context(), tx, a, "webhook.manual_requested", "webhook", hookID.String(), nil, r)
}
