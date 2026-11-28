package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
)

// Select a fixed projection. Diagnostic messages, provider responses, payloads,
// raw alert kinds, webhook destinations and customer email contents never enter the result.
func (s *Server) ownerSummary(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	var orgFilter, projectFilter *uuid.UUID
	for name, dest := range map[string]**uuid.UUID{"org": &orgFilter, "project": &projectFilter} {
		if value := r.URL.Query().Get(name); value != "" {
			id, err := uuid.Parse(value)
			if err != nil || id == uuid.Nil {
				return nil, apierr.New(422, "validation_error", "Enter a valid organization or project ID.")
			}
			*dest = &id
		}
	}
	queries := []struct{ key, query string }{
		{"users", `SELECT u.id,u.email,u.created_at,u.email_verified_at IS NOT NULL AS verified,
   COALESCE((SELECT jsonb_agg(to_jsonb(x)) FROM (SELECT o.id,o.name FROM memberships m JOIN organizations o ON o.id=m.org_id
   WHERE m.user_id=u.id AND m.role='owner' ORDER BY o.created_at,o.id LIMIT 10)x),'[]') organizations
   FROM users u ORDER BY u.created_at DESC,u.id DESC LIMIT 50`},
		{"reviews", `SELECT p.id,p.name,p.org_id,o.name organization,p.sending_status state,p.hourly_limit,p.daily_limit,
  (SELECT count(*) FROM domains d WHERE d.project_id=p.id AND d.status='verified' AND d.deleted_at IS NULL) verified_domains,
  left(p.review_request->>'website',200) website,left(p.review_request->>'use_case',2000) use_case,
  left(p.review_request->>'recipient_consent',1000) consent,left(p.review_request->>'monthly_volume',20) volume
  FROM projects p JOIN organizations o ON o.id=p.org_id WHERE ($1::uuid IS NULL OR p.id=$1) AND ($2::uuid IS NULL OR p.org_id=$2)
  ORDER BY (p.sending_status='pending_review') DESC,p.status_changed_at DESC,p.id LIMIT 50`},
		{"grants", `SELECT g.id,g.org_id,o.name organization,g.plan,g.starts_at,g.ends_at,g.revoked_at,
  CASE WHEN g.revoked_at IS NOT NULL THEN 'revoked' WHEN g.ends_at<=now() THEN 'expired' ELSE 'active' END AS state,
  g.reason,g.revoke_reason FROM complimentary_memberships g JOIN organizations o ON o.id=g.org_id WHERE ($1::uuid IS NULL OR g.org_id=$1) ORDER BY g.starts_at DESC,g.id LIMIT 50`},
		{"purchases", `SELECT b.id,b.org_id,o.name organization,b.sku,b.state,b.amount,b.currency,b.created_at
  FROM billing_orders b JOIN organizations o ON o.id=b.org_id WHERE ($1::uuid IS NULL OR b.org_id=$1) ORDER BY b.created_at DESC,b.id LIMIT 50`},
		{"errors", `SELECT * FROM (
  (SELECT id,org_id,created_at,
    CASE kind WHEN 'review_requested' THEN 'Sender review requested' WHEN 'delivery_unknown' THEN 'Unknown send outcome'
    WHEN 'billing_review' THEN 'Payment needs review' WHEN 'queue_age' THEN 'Delayed worker queue' WHEN 'project_paused' THEN 'Sending paused after risk review'
    WHEN 'complaint' THEN 'Recipient complaint' WHEN 'delivery_unknown_unresolved' THEN 'Unresolved send outcome'
    WHEN 'provider_sending_paused' THEN 'Provider sending paused' WHEN 'quota_warning' THEN 'Usage warning' ELSE 'Operations alert' END category,severity FROM alerts WHERE resolved_at IS NULL ORDER BY created_at DESC LIMIT 50)
  UNION ALL (SELECT id,org_id,started_at,CASE state WHEN 'unknown' THEN 'Unknown send outcome' WHEN 'retryable' THEN 'Send retry scheduled' ELSE 'Send rejected' END,
    CASE WHEN state='unknown' THEN 'critical' ELSE 'warning' END FROM send_attempts WHERE state IN('rejected','retryable','unknown') ORDER BY started_at DESC LIMIT 50)
  UNION ALL (SELECT id,org_id,created_at,'Webhook delivery failed','warning' FROM webhook_deliveries WHERE state='failed' ORDER BY created_at DESC LIMIT 50)
  UNION ALL (SELECT id,NULL::uuid,created_at,CASE state WHEN 'unknown' THEN 'Account email outcome unknown' ELSE 'Account email failed' END,'warning'
    FROM auth_mail_outbox WHERE state IN('unknown','failed') ORDER BY created_at DESC LIMIT 50)
  ) events ORDER BY created_at DESC,id LIMIT 50`},
	}
	out := map[string]json.RawMessage{}
	for _, q := range queries {
		var b json.RawMessage
		var args []any
		switch q.key {
		case "reviews":
			args = []any{projectFilter, orgFilter}
		case "grants", "purchases":
			args = []any{orgFilter}
		}
		if err := tx.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(item)),'[]'::jsonb) FROM ("+q.query+") item", args...).Scan(&b); err != nil {
			return nil, err
		}
		out[q.key] = b
	}
	return out, nil
}

// Search terms stay in the request body. Both matching branches use an indexed
// literal prefix and cap their candidate sets before joining tenant details.
func (s *Server) ownerSearch(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	var body struct {
		Query string `json:"query"`
	}
	if err := decode(r, &body); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(body.Query)
	if utf8.RuneCountInString(q) < 2 || len(q) > 128 || strings.IndexFunc(q, unicode.IsControl) >= 0 {
		return nil, apierr.New(422, "validation_error", "Enter at least two characters and at most 128 bytes.")
	}
	// PostgreSQL's starts_with function interprets '%' and '_' literally.
	// Explicit C-collation bounds also support indexed generic parameter plans.
	var results json.RawMessage
	err := tx.QueryRow(ctx, `WITH names AS MATERIALIZED (
    SELECT id FROM organizations WHERE lower(name) COLLATE "C">=lower($1) COLLATE "C"
     AND lower(name) COLLATE "C"<(lower($1)||chr(1114111)) COLLATE "C" AND starts_with(lower(name),lower($1))
    ORDER BY lower(name) COLLATE "C",id LIMIT 20
   ), accounts AS MATERIALIZED (
    SELECT id,email FROM users WHERE lower(email) COLLATE "C">=lower($1) COLLATE "C"
     AND lower(email) COLLATE "C"<(lower($1)||chr(1114111)) COLLATE "C" AND starts_with(lower(email),lower($1))
    ORDER BY lower(email) COLLATE "C",id LIMIT 20
   ), candidates AS (
    SELECT id,NULL::text AS email FROM names UNION ALL
    SELECT x.org_id,a.email FROM accounts a CROSS JOIN LATERAL
      (SELECT m.org_id FROM memberships m WHERE m.user_id=a.id AND m.role='owner' ORDER BY m.org_id LIMIT 20)x
   ), matches AS (
    SELECT id,min(email) matched_email FROM candidates GROUP BY id
   ) SELECT COALESCE(jsonb_agg(to_jsonb(result)),'[]'::jsonb) FROM (
    SELECT o.id,o.name,coalesce(m.matched_email,owner.email,'') owner_email FROM matches m JOIN organizations o ON o.id=m.id
    LEFT JOIN LATERAL (SELECT u.email FROM memberships member JOIN users u ON u.id=member.user_id WHERE member.org_id=o.id
        AND member.role='owner' ORDER BY member.user_id LIMIT 1) owner ON true
    ORDER BY (m.matched_email IS NOT NULL) DESC,lower(o.name) COLLATE "C",o.id LIMIT 20
   ) result`, q).Scan(&results)
	return map[string]any{"organizations": results}, err
}

func ownerNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.ErrNotFound
	}
	return err
}
func (s *Server) ownerGrant(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	var req struct {
		ID     uuid.UUID `json:"request_id"`
		Org    uuid.UUID `json:"org_id"`
		Plan   string    `json:"plan"`
		Reason string    `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	note, err := checkedReason(req.Reason)
	if err != nil {
		return nil, err
	}
	if req.ID == uuid.Nil || req.Org == uuid.Nil {
		return nil, apierr.New(422, "validation_error", "Enter valid request and organization IDs.")
	}
	plan, ok := plans.All[req.Plan]
	if !ok || (req.Plan != "flex" && req.Plan != "pro_50k" && req.Plan != "pro_100k") {
		return nil, apierr.New(422, "validation_error", "Choose Flex, Pro 50k or Pro 100k.")
	}
	var baseline string
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT plan,statement_timestamp() FROM organizations WHERE id=$1 FOR UPDATE`, req.Org).Scan(&baseline, &now); err != nil {
		return nil, ownerNotFound(err)
	}
	var oldOrg uuid.UUID
	var oldPlan, oldNote string
	err = tx.QueryRow(ctx, `SELECT org_id,plan,reason FROM complimentary_memberships WHERE id=$1`, req.ID).Scan(&oldOrg, &oldPlan, &oldNote)
	result := map[string]any{"id": req.ID}
	if err == nil {
		if oldOrg != req.Org || oldPlan != req.Plan || oldNote != note {
			return nil, ownerConflict("This request ID already identifies a different grant.")
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	duration := 30 * 24 * time.Hour
	if req.Plan == "flex" {
		duration = plans.FlexPackValidity
	}
	end := now.Add(duration)
	var occupied, budget bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM complimentary_memberships WHERE org_id=$1 AND revoked_at IS NULL AND ends_at>$2)
  OR EXISTS(SELECT 1 FROM usage_periods WHERE org_id=$1 AND stripe_invoice IS NOT NULL AND period_end>$2 AND period_start<$3),
  (SELECT count(*) FROM complimentary_memberships WHERE org_id=$1 AND starts_at>$2::timestamptz-interval '30 days')>=4
  OR (SELECT count(*) FROM complimentary_memberships WHERE starts_at>$2::timestamptz-interval '24 hours')>=30`, req.Org, now, end).Scan(&occupied, &budget)
	if err != nil {
		return nil, err
	}
	if occupied || (baseline != "free" && baseline != "flex") {
		return nil, ownerConflict("An existing membership or paid cycle overlaps this grant.")
	}
	if budget {
		return nil, apierr.New(429, "rate_limit_exceeded", "Complimentary grant limit reached.")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO complimentary_memberships(id,org_id,plan,granted_by,reason,starts_at,ends_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, req.ID, req.Org, req.Plan, a.UserID, note, now, end); err != nil {
		return nil, err
	}
	var period uuid.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO usage_periods(org_id,period_start,period_end,plan,included,data_included,complimentary_grant) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, req.Org, now, end, req.Plan, plan.IncludedRecipients, plan.IncludedDataBytes, req.ID).Scan(&period); err != nil {
		return nil, err
	}
	if req.Plan == "flex" {
		lot, err := quota.GrantCredits(ctx, tx, req.Org, "grant", "complimentary:"+req.ID.String(), plans.FlexPackRecipients, plans.FlexPackDataBytes, duration, now)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE credit_lots SET complimentary_grant=$1 WHERE id=$2`, req.ID, lot)
		if err != nil {
			return nil, err
		}
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO credit_ledger(org_id,usage_period_id,kind,source,quantity,data_bytes,note) VALUES($1,$2,'grant','period',$3,$4,'Complimentary Pro allowance')`, req.Org, period, plan.IncludedRecipients, plan.IncludedDataBytes)
		if err != nil {
			return nil, err
		}
	}
	return result, s.staffAudit(tx, r, a, &req.Org, "membership.granted", "complimentary_membership", req.ID.String(), map[string]any{"plan": req.Plan, "reason": note, "expires": end})
}

func (s *Server) ownerRevoke(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return nil, err
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	note, err := checkedReason(req.Reason)
	if err != nil {
		return nil, err
	}
	var oid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT o.id FROM organizations o JOIN complimentary_memberships g ON g.org_id=o.id WHERE g.id=$1 FOR UPDATE OF o`, id).Scan(&oid)
	if err != nil {
		return nil, ownerNotFound(err)
	}
	result := map[string]bool{"revoked": true}
	t, err := tx.Exec(ctx, `UPDATE complimentary_memberships SET revoked_at=now(),revoke_reason=$2 WHERE id=$1 AND revoked_at IS NULL`, id, note)
	if err != nil || t.RowsAffected() == 0 {
		return result, err
	}
	if _, err = tx.Exec(ctx, `UPDATE usage_periods SET billing_usable=false WHERE complimentary_grant=$1`, id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE credit_lots SET billing_usable=false WHERE complimentary_grant=$1`, id); err != nil {
		return nil, err
	}
	return result, s.staffAudit(tx, r, a, &oid, "membership.revoked", "complimentary_membership", id.String(), map[string]any{"reason": note})
}

func (s *Server) ownerDecide(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
	pid, err := pathUUID(r, "project")
	if err != nil {
		return nil, err
	}
	var req struct {
		Action string `json:"action"`
		Hourly int    `json:"hourly_limit"`
		Daily  int    `json:"daily_limit"`
		Reason string `json:"reason"`
	}
	switch path.Base(r.URL.Path) {
	case "approve":
		var old struct {
			Note   string `json:"note"`
			Hourly int    `json:"hourly_limit"`
			Daily  int    `json:"daily_limit"`
		}
		if err := decode(r, &old); err != nil {
			return nil, err
		}
		req.Action, req.Reason, req.Hourly, req.Daily = "approve", old.Note, old.Hourly, old.Daily
		if req.Hourly == 0 {
			req.Hourly = 25
		}
		if req.Daily == 0 {
			req.Daily = 100
		}
	case "pause":
		var old struct {
			Reason string `json:"reason"`
		}
		if err := decode(r, &old); err != nil {
			return nil, err
		}
		req.Action, req.Reason = "pause", old.Reason
	default:
		if err := decode(r, &req); err != nil {
			return nil, err
		}
	}
	note, err := checkedReason(req.Reason)
	if err != nil {
		return nil, err
	}
	if req.Action != "approve" && req.Action != "pause" {
		return nil, apierr.New(422, "validation_error", "Choose approve or pause.")
	}
	var oid uuid.UUID
	var risk, state, tenant string
	err = tx.QueryRow(ctx, `SELECT o.id,o.risk_status FROM organizations o JOIN projects p ON p.org_id=o.id WHERE p.id=$1 FOR UPDATE OF o`, pid).Scan(&oid, &risk)
	if err != nil {
		return nil, ownerNotFound(err)
	}
	err = tx.QueryRow(ctx, `SELECT sending_status,ses_tenant_name FROM projects WHERE id=$1 FOR UPDATE`, pid).Scan(&state, &tenant)
	if err != nil {
		return nil, ownerNotFound(err)
	}
	if state != "paused" && state != "pending_review" && state != "approved" && !(state == "test_only" && req.Action == "pause") {
		return nil, ownerConflict("The customer must first submit a live-access request.")
	}
	if req.Action == "approve" {
		if req.Hourly < 1 || req.Hourly > 50000 || req.Daily < req.Hourly || req.Daily > 250000 {
			return nil, apierr.New(422, "validation_error", "Use 1–50,000 per hour and hourly limit–250,000 per day.")
		}
		if risk != "approved" && risk != "new" {
			return nil, ownerConflict("Resolve the organization risk restriction before approval.")
		}
		if err = s.Cfg.Env.CheckProjectTenant(pid, tenant); err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE projects SET sending_status='approved',hourly_limit=$2,daily_limit=$3,status_changed_at=now(),status_reason=$4 WHERE id=$1`, pid, req.Hourly, req.Daily, note)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE organizations SET risk_status='approved' WHERE id=$1 AND risk_status='new'`, oid)
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `UPDATE alerts SET resolved_at=now() WHERE dedupe_key=$1 AND resolved_at IS NULL`, "review:"+pid.String())
		if err != nil {
			return nil, err
		}
		state = "approved"
	} else {
		_, err = tx.Exec(ctx, `UPDATE projects SET sending_status='paused',status_changed_at=now(),status_reason=$2 WHERE id=$1`, pid, note)
		if err != nil {
			return nil, err
		}
		state = "paused"
	}
	return map[string]string{"state": state, "sending_status": state, "project_id": pid.String()}, s.staffAudit(tx, r, a, &oid, "project."+state, "project", pid.String(), map[string]any{"reason": note, "hourly_limit": req.Hourly, "daily_limit": req.Daily})
}
