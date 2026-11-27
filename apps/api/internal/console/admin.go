package console

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/worker"
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

func (s *Server) adminReviews(w http.ResponseWriter, r *http.Request, sess *session) {
	out := []map[string]any{}
	rows, err := s.StaffPool.Query(r.Context(), `SELECT p.id, p.name, o.id, o.name, hedvi_effective_plan(o.id,statement_timestamp()), p.sending_status, p.review_request, p.status_changed_at,
			(SELECT count(*) FROM domains d WHERE d.project_id = p.id AND d.status = 'verified' AND d.deleted_at IS NULL)
		FROM projects p JOIN organizations o ON o.id = p.org_id
		WHERE p.sending_status IN ('pending_review', 'paused') ORDER BY p.status_changed_at`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var pid, oid uuid.UUID
		var pname, oname, plan, status string
		var review []byte
		var changed time.Time
		var verified int
		if err := rows.Scan(&pid, &pname, &oid, &oname, &plan, &status, &review, &changed, &verified); err != nil {
			s.fail(w, r, err)
			return
		}
		var rv any
		_ = json.Unmarshal(review, &rv)
		out = append(out, map[string]any{"project_id": pid, "project": pname, "org_id": oid, "organization": oname, "plan": plan,
			"sending_status": status, "review_request": rv, "status_changed_at": ts(changed), "verified_domains": verified})
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) adminApprove(w http.ResponseWriter, r *http.Request, sess *session) {
	pid, err := pathUUID(r, "project")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		HourlyLimit int    `json:"hourly_limit"`
		DailyLimit  int    `json:"daily_limit"`
		Note        string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.HourlyLimit == 0 {
		req.HourlyLimit = 25
	}
	if req.DailyLimit == 0 {
		req.DailyLimit = 100
	}
	var tenant string
	var orgID uuid.UUID
	if err := s.StaffPool.QueryRow(r.Context(), `SELECT ses_tenant_name, org_id FROM projects WHERE id = $1`, pid).Scan(&tenant, &orgID); err != nil {
		s.fail(w, r, apierr.ErrNotFound)
		return
	}
	if err := s.Cfg.Env.CheckProjectTenant(pid, tenant); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Provider.EnsureTenant(r.Context(), tenant); err != nil {
		s.fail(w, r, err)
		return
	}
	err = db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `UPDATE projects SET sending_status = 'approved', status_reason = $2, status_changed_at = now(),
			hourly_limit = $3, daily_limit = $4 WHERE id = $1`, pid, req.Note, req.HourlyLimit, req.DailyLimit); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE alerts SET resolved_at = now() WHERE dedupe_key = $1 AND resolved_at IS NULL`, "review:"+pid.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE organizations SET risk_status = 'approved' WHERE id = $1 AND risk_status = 'new'`, orgID); err != nil {
			return err
		}
		return s.staffAudit(tx, r, sess, &orgID, "project.approved", "project", pid.String(),
			map[string]any{"hourly_limit": req.HourlyLimit, "daily_limit": req.DailyLimit, "note": req.Note})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project_id": pid, "sending_status": "approved"})
}

func (s *Server) adminPause(w http.ResponseWriter, r *http.Request, sess *session) {
	pid, err := pathUUID(r, "project")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil || req.Reason == "" {
		s.fail(w, r, apierr.New(422, "validation_error", "A reason is required."))
		return
	}
	err = db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
		var orgID uuid.UUID
		if err := tx.QueryRow(r.Context(), `UPDATE projects SET sending_status = 'paused', status_reason = $2, status_changed_at = now()
			WHERE id = $1 RETURNING org_id`, pid, req.Reason).Scan(&orgID); err != nil {
			if db.IsNoRows(err) {
				return apierr.ErrNotFound
			}
			return err
		}
		return s.staffAudit(tx, r, sess, &orgID, "project.paused", "project", pid.String(), map[string]any{"reason": req.Reason})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"project_id": pid, "sending_status": "paused"})
}

func (s *Server) adminAlerts(w http.ResponseWriter, r *http.Request, sess *session) {
	out := []map[string]any{}
	rows, err := s.StaffPool.Query(r.Context(), `SELECT id, org_id, project_id, kind, severity, message, data, created_at, notified_at
		FROM alerts WHERE resolved_at IS NULL ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var org, project *uuid.UUID
		var kind, sev, msg string
		var data []byte
		var at time.Time
		var notified *time.Time
		if err := rows.Scan(&id, &org, &project, &kind, &sev, &msg, &data, &at, &notified); err != nil {
			s.fail(w, r, err)
			return
		}
		var d any
		_ = json.Unmarshal(data, &d)
		out = append(out, map[string]any{"id": id, "org_id": org, "project_id": project, "kind": kind, "severity": sev, "message": msg,
			"data": d, "created_at": ts(at), "notified_at": notified})
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) adminResolveAlert(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
		var org *uuid.UUID
		if err := tx.QueryRow(r.Context(), `UPDATE alerts SET resolved_at = now() WHERE id = $1 AND resolved_at IS NULL RETURNING org_id`, id).Scan(&org); err != nil {
			if db.IsNoRows(err) {
				return apierr.ErrNotFound
			}
			return err
		}
		return s.staffAudit(tx, r, sess, org, "alert.resolved", "alert", id.String(), nil)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "resolved": true})
}

func (s *Server) adminUnknown(w http.ResponseWriter, r *http.Request, sess *session) {
	out := []map[string]any{}
	rows, err := s.StaffPool.Query(r.Context(), `SELECT a.id, a.email_id, e.org_id, e.project_id, a.error_code, a.error_message, a.started_at, a.finished_at
		FROM send_attempts a JOIN emails e ON e.id = a.email_id WHERE a.state = 'unknown' ORDER BY a.started_at LIMIT 200`)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, email, org, project uuid.UUID
		var code, msg string
		var started time.Time
		var finished *time.Time
		if err := rows.Scan(&id, &email, &org, &project, &code, &msg, &started, &finished); err != nil {
			s.fail(w, r, err)
			return
		}
		out = append(out, map[string]any{"attempt_id": id, "email_id": email, "org_id": org, "project_id": project,
			"error_code": code, "error_message": msg, "started_at": ts(started), "finished_at": finished})
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// adminResolveAttempt settles an unknown attempt after investigation in SES.
// "accepted" consumes quota; "not_sent" releases it and fails the recipients.
// Neither resends: a resend is a separate, audited action.
func (s *Server) adminResolveAttempt(w http.ResponseWriter, r *http.Request, sess *session) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Outcome           string `json:"outcome"`
		ProviderMessageID string `json:"provider_message_id"`
		Note              string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Note == "" || (req.Outcome != "accepted" && req.Outcome != "not_sent") {
		s.fail(w, r, apierr.New(422, "validation_error", "`outcome` must be accepted or not_sent and a note is required."))
		return
	}
	err = db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
		var emailID, orgID uuid.UUID
		if err := tx.QueryRow(r.Context(), `SELECT a.email_id, e.org_id FROM send_attempts a JOIN emails e ON e.id = a.email_id
			WHERE a.id = $1 AND a.state = 'unknown' FOR UPDATE`, id).Scan(&emailID, &orgID); err != nil {
			if db.IsNoRows(err) {
				return apierr.New(404, "not_found", "No unresolved attempt with this id.")
			}
			return err
		}
		if err := worker.ResolveUnknown(r.Context(), tx, emailID, id, req.Outcome, req.ProviderMessageID, "staff: "+req.Note, sess.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE alerts SET resolved_at = now() WHERE dedupe_key IN ($1, $2) AND resolved_at IS NULL`,
			"unknown:"+emailID.String(), "unknown-escalated:"+id.String()); err != nil {
			return err
		}
		return s.staffAudit(tx, r, sess, &orgID, "send_attempt.resolved", "send_attempt", id.String(),
			map[string]any{"outcome": req.Outcome, "note": req.Note, "email_id": emailID})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"attempt_id": id, "outcome": req.Outcome})
}
