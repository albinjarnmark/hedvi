package console

import (
	"context"
	"encoding/json"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/worker"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Staff endpoints use the worker pool (cross-tenant) and require staff + MFA.
// Every action is written to the audit log of the affected organization.

func (s *Server) staffAudit(tx pgx.Tx, r *http.Request, sess *session, orgID *uuid.UUID, action, targetType, targetID string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	var ip any
	if addr := s.clientIP(r); addr != nil {
		ip = addr.String()
	}
	_, err := tx.Exec(r.Context(), `INSERT INTO audit_log (org_id, actor_kind, actor_user_id, action, target_type, target_id, metadata, ip)
		VALUES ($1, 'staff', $2, $3, $4, $5, $6, $7)`, orgID, sess.UserID, action, targetType, targetID, meta, ip)
	return err
}

// The historical local operator endpoints share the session and transaction
// guard used by the hosted workflow. Raw diagnostics stay out of these lists.
func ownerLegacyList(kind string) ownerHandler {
	return func(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
		var q string
		switch kind {
		case "reviews":
			q = `SELECT p.id project_id,p.name project,o.id org_id,o.name organization,p.sending_status,p.status_changed_at FROM projects p JOIN organizations o ON o.id=p.org_id WHERE p.sending_status IN('pending_review','paused') ORDER BY status_changed_at LIMIT 50`
		case "alerts":
			q = `SELECT id,org_id,project_id,severity,created_at,'Operations alert' message FROM alerts WHERE resolved_at IS NULL ORDER BY created_at DESC LIMIT 50`
		case "unknown":
			q = `SELECT id attempt_id,email_id,org_id,started_at,finished_at FROM send_attempts WHERE state='unknown' ORDER BY started_at LIMIT 50`
		}
		var out json.RawMessage
		err := tx.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(item)),'[]') FROM ("+q+") item").Scan(&out)
		return map[string]any{"data": out}, err
	}
}
func (s *Server) ownerResolveAlert(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return nil, err
	}
	if r.ContentLength != 0 {
		var empty struct{}
		if err := decode(r, &empty); err != nil {
			return nil, err
		}
	}
	var org *uuid.UUID
	if err := tx.QueryRow(ctx, `UPDATE alerts SET resolved_at=now() WHERE id=$1 AND resolved_at IS NULL RETURNING org_id`, id).Scan(&org); err != nil {
		return nil, ownerNotFound(err)
	}
	return map[string]any{"id": id, "resolved": true}, s.staffAudit(tx, r, a, org, "alert.resolved", "alert", id.String(), nil)
}
func (s *Server) ownerResolveAttempt(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Outcome    string `json:"outcome"`
		ProviderID string `json:"provider_message_id"`
		Note       string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	note, err := checkedReason(req.Note)
	if err != nil {
		return nil, err
	}
	if len(req.ProviderID) > 254 || (req.Outcome != "accepted" && req.Outcome != "not_sent") {
		return nil, apierr.New(422, "validation_error", "Choose accepted or not_sent and a valid provider ID.")
	}
	var email, org uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT email_id,org_id FROM send_attempts WHERE id=$1 AND state='unknown' FOR UPDATE`, id).Scan(&email, &org); err != nil {
		return nil, ownerNotFound(err)
	}
	if err := worker.ResolveUnknown(ctx, tx, email, id, req.Outcome, req.ProviderID, "staff: "+note, a.UserID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key IN($1,$2) AND resolved_at IS NULL`, "unknown:"+email.String(), "unknown-escalated:"+id.String()); err != nil {
		return nil, err
	}
	return map[string]any{"attempt_id": id, "outcome": req.Outcome}, s.staffAudit(tx, r, a, &org, "send_attempt.resolved", "send_attempt", id.String(), map[string]any{"outcome": req.Outcome, "note": note, "email_id": email})
}
