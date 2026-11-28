package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/net/html"

	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/httpapi"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/plans"
	"hedvi.dev/api/internal/quota"
)

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (s *Server) orgQuota(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) (quota.Org, error) {
	org := quota.Org{ID: orgID}
	err := tx.QueryRow(ctx, `SELECT hedvi_effective_plan(id,statement_timestamp()), use_credits_for_overage, overage_enabled, overage_cap_recipients FROM organizations WHERE id = $1`,
		orgID).Scan(&org.Plan, &org.UseCreditsForOverage, &org.OverageEnabled, &org.OverageCap)
	return org, err
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request, a *access) {
	out := map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var name, status, reason string
		var hourly, daily, orgHourly, orgDaily int
		var changed time.Time
		if err := tx.QueryRow(r.Context(), `SELECT name, sending_status, status_reason, status_changed_at, lim.hourly, lim.daily, lim.org_hourly, lim.org_daily
			FROM projects p CROSS JOIN LATERAL hedvi_live_limits(p.id,statement_timestamp()) lim WHERE p.id = $1`, a.ProjectID).Scan(&name, &status, &reason, &changed, &hourly, &daily, &orgHourly, &orgDaily); err != nil {
			return err
		}
		out["project"] = map[string]any{"id": a.ProjectID, "name": name, "sending_status": status, "status_reason": reason,
			"status_changed_at": ts(changed), "hourly_limit": hourly, "daily_limit": daily, "org_hourly_limit": orgHourly, "org_daily_limit": orgDaily}
		counts := map[string]int{}
		rows, err := tx.Query(r.Context(), `SELECT mode || ':' || last_event, count(*) FROM emails
			WHERE project_id = $1 AND created_at > now() - interval '24 hours' GROUP BY 1`, a.ProjectID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			var n int
			if err := rows.Scan(&k, &n); err != nil {
				rows.Close()
				return err
			}
			counts[k] = n
		}
		rows.Close()
		out["last_24h"] = counts
		var keysTest, keysLive, testEmails, domainsTotal, domainsVerified, liveEmails, webhooks int
		if err := tx.QueryRow(r.Context(), `SELECT
				(SELECT count(*) FROM api_keys WHERE project_id = $1 AND mode = 'test' AND revoked_at IS NULL),
				(SELECT count(*) FROM api_keys WHERE project_id = $1 AND mode = 'live' AND revoked_at IS NULL),
				(SELECT count(*) FROM emails WHERE project_id = $1 AND mode = 'test'),
				(SELECT count(*) FROM domains WHERE project_id = $1 AND deleted_at IS NULL),
				(SELECT count(*) FROM domains WHERE project_id = $1 AND deleted_at IS NULL AND status = 'verified'),
				(SELECT count(*) FROM emails WHERE project_id = $1 AND mode = 'live'),
				(SELECT count(*) FROM webhooks WHERE project_id = $1 AND status = 'enabled')`, a.ProjectID).Scan(
			&keysTest, &keysLive, &testEmails, &domainsTotal, &domainsVerified, &liveEmails, &webhooks); err != nil {
			return err
		}
		out["checklist"] = []map[string]any{
			{"id": "test_key", "done": keysTest > 0},
			{"id": "test_email", "done": testEmails > 0},
			{"id": "domain_added", "done": domainsTotal > 0},
			{"id": "domain_verified", "done": domainsVerified > 0},
			{"id": "live_access", "done": status == "approved", "state": status},
			{"id": "live_key", "done": keysLive > 0},
			{"id": "first_live_email", "done": liveEmails > 0},
			{"id": "webhook", "done": webhooks > 0, "optional": true},
		}
		org, err := s.orgQuota(r.Context(), tx, a.OrgID)
		if err != nil {
			return err
		}
		u, err := quota.LoadUsage(r.Context(), tx, org, time.Now())
		if err != nil {
			return err
		}
		out["usage"] = httpapi.UsageJSON(u)
		out["plan"] = org.Plan
		al, err := loadAlerts(r.Context(), tx, a, 5)
		out["alerts"] = al
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func loadAlerts(ctx context.Context, tx pgx.Tx, a *access, limit int) ([]map[string]any, error) {
	rows, err := tx.Query(ctx, `SELECT id, kind, severity, message, created_at FROM alerts
		WHERE org_id = $1 AND (project_id IS NULL OR project_id = $2) AND resolved_at IS NULL ORDER BY created_at DESC LIMIT $3`,
		a.OrgID, a.ProjectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id uuid.UUID
		var kind, sev, msg string
		var at time.Time
		if err := rows.Scan(&id, &kind, &sev, &msg, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "severity": sev, "message": msg, "created_at": ts(at)})
	}
	return out, rows.Err()
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request, a *access) {
	var out []map[string]any
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var err error
		out, err = loadAlerts(r.Context(), tx, a, 100)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// --- API keys -------------------------------------------------------------

func (s *Server) listKeys(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT k.id, k.name, k.mode, k.permission, d.name, k.hint, k.created_at, k.last_used_at, k.revoked_at
			FROM api_keys k LEFT JOIN domains d ON d.id = k.domain_id WHERE k.project_id = $1 ORDER BY k.created_at DESC`, a.ProjectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var name, mode, perm, hint string
			var domain *string
			var created time.Time
			var used, revoked *time.Time
			if err := rows.Scan(&id, &name, &mode, &perm, &domain, &hint, &created, &used, &revoked); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "name": name, "mode": mode, "permission": perm, "domain": domain, "hint": hint,
				"created_at": ts(created), "last_used_at": used, "revoked_at": revoked})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		Name       string     `json:"name"`
		Mode       string     `json:"mode"`
		Permission string     `json:"permission"`
		DomainID   *uuid.UUID `json:"domain_id"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 60 {
		s.fail(w, r, apierr.New(422, "validation_error", "Key name must be 1-60 characters."))
		return
	}
	if req.Mode != "test" && req.Mode != "live" {
		s.fail(w, r, apierr.New(422, "validation_error", "`mode` must be test or live."))
		return
	}
	if req.Mode == "test" && req.DomainID != nil {
		s.fail(w, r, apierr.New(422, "validation_error", "Test keys cannot be restricted to a live domain. Use an unscoped test key or a domain-scoped live key."))
		return
	}
	if req.Permission == "" {
		req.Permission = "full_access"
	}
	if req.Permission != "full_access" && req.Permission != "sending_access" {
		s.fail(w, r, apierr.New(422, "validation_error", "`permission` must be full_access or sending_access."))
		return
	}
	var id uuid.UUID
	var plaintext, hint string
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		if req.DomainID != nil {
			var ok bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM domains WHERE id = $1 AND project_id = $2 AND deleted_at IS NULL)`,
				*req.DomainID, a.ProjectID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return apierr.New(422, "validation_error", "Unknown domain for this project.")
			}
		}
		var err error
		var g = struct{ p, h string }{}
		var gen = func() error {
			kid, k, err := CreateKey(r.Context(), tx, s.Hasher, a.OrgID, a.ProjectID, &a.UserID, req.Name, req.Mode, req.Permission, req.DomainID)
			id, g.p, g.h = kid, k.Plaintext, k.Hint
			return err
		}
		if err = gen(); err != nil {
			return err
		}
		plaintext, hint = g.p, g.h
		return s.audit(r.Context(), tx, a, "api_key.created", "api_key", id.String(), map[string]any{"mode": req.Mode, "permission": req.Permission}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "key": plaintext, "hint": hint, "mode": req.Mode,
		"note": "Copy the key now. Hedvi stores only a hash and cannot show it again."})
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND project_id = $2 AND revoked_at IS NULL`, id, a.ProjectID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apierr.ErrNotFound
		}
		return s.audit(r.Context(), tx, a, "api_key.revoked", "api_key", id.String(), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "revoked": true})
}

// --- Emails -----------------------------------------------------------------

func searchTerm(raw string) (string, error) {
	if len(raw) > 256 || strings.IndexFunc(raw, unicode.IsControl) >= 0 || !utf8.ValidString(raw) {
		return "", apierr.New(422, "validation_error", "Search terms must be at most 256 bytes without control characters.")
	}
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(strings.TrimSpace(raw))), nil
}

func (s *Server) listEmailsFor(r *http.Request, a *access, mode string) ([]map[string]any, bool, error) {
	limit, after, _, err := httpapi.Pagination(r)
	if err != nil {
		return nil, false, err
	}
	q := r.URL.Query()
	search, err := searchTerm(q.Get("q"))
	if err != nil {
		return nil, false, err
	}
	var list []*httpapi.EmailRow
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		sql := `SELECT ` + httpapi.EmailColumns + ` FROM emails e WHERE project_id = $1 AND mode = $2`
		args := []any{a.ProjectID, mode, limit + 1}
		if after != nil {
			args = append(args, *after)
			sql += fmt.Sprintf(" AND id < $%d", len(args))
		}
		if st := q.Get("status"); st != "" {
			args = append(args, st)
			sql += fmt.Sprintf(" AND last_event = $%d", len(args))
		}
		if search != "" {
			args = append(args, search)
			n := len(args)
			sql += fmt.Sprintf(` AND (EXISTS (SELECT 1 FROM recipients rc WHERE rc.email_id = e.id AND rc.normalized LIKE $%d || '%%' ESCAPE '!')
				OR lower(coalesce(subject, '')) LIKE '%%' || $%d || '%%' ESCAPE '!' OR e.id::text = $%d)`, n, n, n)
		}
		if since := q.Get("since"); since != "" {
			if t, err := time.Parse(time.RFC3339Nano, since); err == nil {
				args = append(args, t)
				sql += fmt.Sprintf(" AND created_at > $%d", len(args))
			}
		}
		rows, err := tx.Query(r.Context(), sql+" ORDER BY id DESC LIMIT $3", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := httpapi.ScanEmail(rows)
			if err != nil {
				return err
			}
			list = append(list, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		out = append(out, emailSummary(e))
	}
	return out, more, nil
}

func emailSummary(e *httpapi.EmailRow) map[string]any {
	return map[string]any{"id": e.ID, "mode": e.Mode, "from": e.From, "to": e.To, "cc": e.Cc, "bcc": e.Bcc, "subject": e.Subject,
		"last_event": e.LastEvent, "last_event_at": ts(e.LastEventAt), "send_state": e.SendState, "created_at": ts(e.CreatedAt),
		"content_available": e.ContentDeleted == nil, "content_expires_at": ts(e.ContentExpires), "attachments": e.AttachmentCount,
		"resend_of": e.ResendOf}
}

func (s *Server) listEmails(w http.ResponseWriter, r *http.Request, a *access) {
	mode := r.URL.Query().Get("mode")
	if mode != "test" {
		mode = "live"
	}
	data, more, err := s.listEmailsFor(r, a, mode)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "has_more": more})
}

func (s *Server) loadEmail(r *http.Request, a *access) (*httpapi.EmailRow, error) {
	id, err := pathUUID(r, "id")
	if err != nil {
		return nil, err
	}
	var e *httpapi.EmailRow
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var err error
		e, err = httpapi.ScanEmail(tx.QueryRow(r.Context(), `SELECT `+httpapi.EmailColumns+` FROM emails WHERE id = $1 AND project_id = $2`, id, a.ProjectID))
		return err
	})
	if db.IsNoRows(err) {
		return nil, apierr.ErrNotFound
	}
	return e, err
}

// extractLinks lists link targets so the dashboard can show them without
// letting the sandboxed preview navigate.
func extractLinks(src string) []string {
	z := html.NewTokenizer(strings.NewReader(src))
	seen := map[string]bool{}
	var out []string
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return out
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, hasAttr := z.TagName()
		if string(name) != "a" || !hasAttr {
			continue
		}
		for {
			k, v, more := z.TagAttr()
			if string(k) == "href" && !seen[string(v)] && len(out) < 200 {
				seen[string(v)] = true
				out = append(out, string(v))
			}
			if !more {
				break
			}
		}
	}
}

func (s *Server) emailDetail(w http.ResponseWriter, r *http.Request, a *access) {
	e, err := s.loadEmail(r, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := emailSummary(e)
	out["reply_to"] = e.ReplyTo
	out["tags"] = e.Tags
	out["provider_message_id"] = e.ProviderMessage
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rs, evs, as, err := httpapi.Timeline(r.Context(), tx, e.ID)
		out["recipients"], out["events"], out["attempts"] = rs, evs, as
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := httpapi.Content(r.Context(), s.Blobs, e)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if m != nil {
		atts := []map[string]any{}
		for i, at := range m.Attachments {
			atts = append(atts, map[string]any{"index": i, "filename": at.Filename, "content_type": at.ContentType,
				"content_id": at.ContentID, "size": len(at.Data)})
		}
		raw, _ := message.BuildMIME(stripAttachmentData(m), e.CreatedAt)
		out["content"] = map[string]any{"html": m.HTML, "text": m.Text, "headers": m.Headers, "attachments": atts,
			"links": extractLinks(m.HTML), "raw_headers": rawHeaders(raw)}
	}
	writeJSON(w, 200, out)
}

func stripAttachmentData(m *message.Message) *message.Message {
	c := *m
	c.Attachments = nil
	return &c
}

func rawHeaders(raw []byte) string {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return string(raw[:i])
	}
	return string(raw)
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request, a *access) {
	e, err := s.loadEmail(r, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	idx, err := strconv.Atoi(r.PathValue("index"))
	m, cerr := httpapi.Content(r.Context(), s.Blobs, e)
	if err != nil || cerr != nil || m == nil || idx < 0 || idx >= len(m.Attachments) {
		s.fail(w, r, apierr.ErrNotFound)
		return
	}
	at := m.Attachments[idx]
	// Always download, never render: attachments are untrusted content.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": at.Filename}))
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = w.Write(at.Data)
}

func (s *Server) resendEmail(w http.ResponseWriter, r *http.Request, a *access) {
	e, err := s.loadEmail(r, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	m, err := httpapi.Content(r.Context(), s.Blobs, e)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if m == nil {
		s.fail(w, r, apierr.New(409, "validation_error", "The content of this email has been deleted under your retention settings, so it cannot be resent."))
		return
	}
	body, _ := json.Marshal(requestFromMessage(m))
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = "console-" + a.UserID.String()
	}
	res, err := s.Accept.Resend(r.Context(), s.principal(a, e.Mode), e.ID, key, body, func(tx pgx.Tx, id uuid.UUID) error {
		return s.audit(r.Context(), tx, a, "email.resent", "email", e.ID.String(), map[string]any{"new_email_id": id}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": res.EmailID, "resend_of": e.ID,
		"note": "A resend is a new email: it uses quota again and recipients may receive a duplicate."})
}

func requestFromMessage(m *message.Message) map[string]any {
	req := map[string]any{"from": m.From, "to": m.To, "subject": m.Subject}
	if len(m.Cc) > 0 {
		req["cc"] = m.Cc
	}
	if len(m.Bcc) > 0 {
		req["bcc"] = m.Bcc
	}
	if len(m.ReplyTo) > 0 {
		req["reply_to"] = m.ReplyTo
	}
	if m.HTML != "" {
		req["html"] = m.HTML
	}
	if m.Text != "" {
		req["text"] = m.Text
	}
	if len(m.Headers) > 0 {
		h := map[string]string{}
		for _, x := range m.Headers {
			h[x.Name] = x.Value
		}
		req["headers"] = h
	}
	if len(m.Tags) > 0 {
		req["tags"] = m.Tags
	}
	if len(m.Attachments) > 0 {
		var atts []map[string]any
		for _, at := range m.Attachments {
			x := map[string]any{"filename": at.Filename, "content_type": at.ContentType, "content": at.Data}
			if at.ContentID != "" {
				x["content_id"] = at.ContentID
			}
			atts = append(atts, x)
		}
		req["attachments"] = atts
	}
	return req
}

// --- Test inbox -------------------------------------------------------------

func (s *Server) testInbox(w http.ResponseWriter, r *http.Request, a *access) {
	data, more, err := s.listEmailsFor(r, a, "test")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var used, limit int64
	var today, dailyLimit int
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT
				(SELECT coalesce(sum(payload_bytes), 0) FROM emails WHERE project_id = $1 AND mode = 'test' AND content_deleted_at IS NULL),
				p.test_storage_limit_bytes,
				coalesce((SELECT count FROM send_counters WHERE project_id = $1 AND window_kind = 'test_day' AND window_start = date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'), 0),
				p.test_daily_limit
			FROM projects p WHERE p.id = $1`, a.ProjectID).Scan(&used, &limit, &today, &dailyLimit)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": data, "has_more": more,
		"limits": map[string]any{"storage_used": used, "storage_limit": limit, "today": today, "daily_limit": dailyLimit, "retention_days": 7}})
}

func (s *Server) sendSample(w http.ResponseWriter, r *http.Request, a *access) {
	body, _ := json.Marshal(map[string]any{
		"from":    "Hedvi <onboarding@example.org>",
		"to":      a.Email,
		"subject": "Your first Hedvi test email",
		"html":    `<h1 style="font-family:Arial">It works</h1><p>This email was captured by your Hedvi test inbox. It was <strong>not</strong> delivered to an external mailbox.</p>`,
		"text":    "It works. This email was captured by your Hedvi test inbox and was not delivered externally.",
		"tags":    []map[string]string{{"name": "source", "value": "dashboard_sample"}},
	})
	res, err := s.Accept.Send(r.Context(), s.principal(a, "test"), "", body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": res.EmailID})
}

func (s *Server) deleteTestEmails(ctx context.Context, a *access, id *uuid.UUID) (int, error) {
	var refs []string
	var deleted int
	err := s.tx(ctx, a, func(tx pgx.Tx) error {
		q := `DELETE FROM emails WHERE project_id = $1 AND mode = 'test'`
		args := []any{a.ProjectID}
		if id != nil {
			q += " AND id = $2"
			args = append(args, *id)
		}
		rows, err := tx.Query(ctx, q+" RETURNING payload_ref", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ref *string
			if err := rows.Scan(&ref); err != nil {
				return err
			}
			deleted++
			if ref != nil {
				refs = append(refs, *ref)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	for _, ref := range refs {
		// Payloads that fail to delete are removed by the orphan sweeper.
		_ = s.Blobs.Delete(ctx, ref)
	}
	return deleted, nil
}

func (s *Server) clearTestInbox(w http.ResponseWriter, r *http.Request, a *access) {
	n, err := s.deleteTestEmails(r.Context(), a, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

func (s *Server) deleteTestEmail(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := s.deleteTestEmails(r.Context(), a, &id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		s.fail(w, r, apierr.ErrNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": n})
}

// --- Domains ----------------------------------------------------------------

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request, a *access) {
	list, err := s.Domains.List(r.Context(), a.OrgID, a.ProjectID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := []map[string]any{}
	for _, d := range list {
		out = append(out, httpapi.DomainJSON(d, true))
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		Name     string `json:"name"`
		MailFrom string `json:"mail_from_subdomain"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Domains.Create(r.Context(), a.OrgID, a.ProjectID, req.Name, req.MailFrom)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		return s.audit(r.Context(), tx, a, "domain.created", "domain", d.ID.String(), map[string]any{"name": d.Name}, r)
	})
	writeJSON(w, 201, httpapi.DomainJSON(d, true))
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Domains.Get(r.Context(), a.OrgID, a.ProjectID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, httpapi.DomainJSON(d, true))
}

func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Domains.Verify(r.Context(), a.OrgID, a.ProjectID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, httpapi.DomainJSON(d, true))
}

func (s *Server) prepareDNSExport(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Domains.PrepareDNSExport(r.Context(), a.OrgID, a.ProjectID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, httpapi.DomainJSON(d, true))
}

func (s *Server) changeMailFrom(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Previous string `json:"previous"`
		Label    string `json:"label"`
		Revision *int64 `json:"revision"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if req.Revision == nil || *req.Revision < 0 {
		s.fail(w, r, apierr.New(422, "validation_error", "Refresh the domain to get its current MAIL FROM revision."))
		return
	}
	d, err := s.Domains.ChangeMailFrom(r.Context(), a.OrgID, a.ProjectID, id, a.UserID, req.Previous, req.Label, *req.Revision)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, httpapi.DomainJSON(d, true))
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Domains.Delete(r.Context(), a.OrgID, a.ProjectID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		return s.audit(r.Context(), tx, a, "domain.deleted", "domain", id.String(), nil, r)
	})
	writeJSON(w, 200, map[string]any{"id": id, "deleted": true})
}

// --- Suppressions -----------------------------------------------------------

func (s *Server) listSuppressions(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if len(q) > 254 {
		s.fail(w, r, apierr.New(422, "validation_error", "Search text exceeds 254 bytes."))
		return
	}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT id, normalized, reason, source, note, created_at, expires_at FROM suppressions
			WHERE org_id = $1 AND removed_at IS NULL AND (expires_at IS NULL OR expires_at > now()) AND ($2 = '' OR strpos(normalized, $2) > 0)
			ORDER BY created_at DESC LIMIT 200`, a.OrgID, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var addr, reason, source, note string
			var created time.Time
			var expires *time.Time
			if err := rows.Scan(&id, &addr, &reason, &source, &note, &created, &expires); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "email": addr, "reason": reason, "source": source, "note": note,
				"created_at": ts(created), "expires_at": expires, "scope": "organization"})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) addSuppression(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		Email string `json:"email"`
		Note  string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	addr, err := address.Parse(req.Email)
	if err != nil {
		s.fail(w, r, apierr.New(422, "validation_error", "Enter a valid email address."))
		return
	}
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `SELECT hedvi_suppress($1,$2,'manual','dashboard','',NULL,$3,$4)`, a.OrgID, addr.Normalized, truncate(req.Note, 300), a.UserID)
		if err != nil {
			return err
		}
		return s.audit(r.Context(), tx, a, "suppression.added", "suppression", accept_mask(addr.Normalized), nil, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"email": addr.Normalized, "suppressed": true})
}

func (s *Server) removeSuppression(w http.ResponseWriter, r *http.Request, a *access) {
	id, err := pathUUID(r, "id")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	req.Note = truncate(req.Note, 300)
	err = s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var reason string
		err := tx.QueryRow(r.Context(), `UPDATE suppressions SET removed_at = now(), removed_by = $3, remove_note = $4
			WHERE id = $1 AND org_id = $2 AND removed_at IS NULL AND reason <> 'complaint' RETURNING reason`, id, a.OrgID, a.UserID, req.Note).Scan(&reason)
		if db.IsNoRows(err) {
			var protected bool
			if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM suppressions WHERE id=$1 AND org_id=$2 AND removed_at IS NULL AND reason='complaint')`, id, a.OrgID).Scan(&protected); err != nil {
				return err
			}
			if protected {
				return apierr.New(403, "security_error", "Complaint suppressions require a staff review.")
			}
			return apierr.ErrNotFound
		}
		if err != nil {
			return err
		}
		return s.audit(r.Context(), tx, a, "suppression.removed", "suppression", id.String(), map[string]any{"reason": reason, "note": req.Note}, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "removed": true})
}

func accept_mask(a string) string {
	at := strings.LastIndexByte(a, '@')
	if at <= 1 {
		return "***" + a[max(at, 0):]
	}
	return a[:1] + "***" + a[at:]
}

func truncate(s string, n int) string {
	if len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		return s[:n]
	}
	return s
}

// --- Settings, usage, members, audit -----------------------------------------

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, a *access) {
	out := map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var name, plan, currency, risk string
		var md, cd *int32
		var useCredits, overage bool
		var overageCap int64
		if err := tx.QueryRow(r.Context(), `SELECT name, hedvi_effective_plan(id,statement_timestamp()), currency, risk_status, metadata_retention_days, content_retention_days,
			use_credits_for_overage, overage_enabled, overage_cap_recipients FROM organizations WHERE id = $1`, a.OrgID).Scan(
			&name, &plan, &currency, &risk, &md, &cd, &useCredits, &overage, &overageCap); err != nil {
			return err
		}
		p, _ := plans.Get(plan)
		metadata, content := plans.Retention(p, md, cd)
		out["organization"] = map[string]any{"id": a.OrgID, "name": name, "plan": plan, "plan_name": p.Name, "currency": currency,
			"risk_status": risk, "use_credits_for_overage": useCredits, "overage_enabled": overage, "overage_cap": overageCap,
			"overage_allowed": p.OverageAllowed}
		out["retention"] = map[string]any{"metadata_days": int(metadata.Hours() / 24), "content_days": int(content.Hours() / 24),
			"plan_metadata_days": p.MetadataDays, "plan_max_content_days": p.MaxContentDays, "test_inbox_days": 7}
		var pname, status, reason string
		var review []byte
		var hourly, daily, orgHourly, orgDaily int
		if err := tx.QueryRow(r.Context(), `SELECT name, sending_status, status_reason, review_request, lim.hourly, lim.daily, lim.org_hourly, lim.org_daily FROM projects p
			CROSS JOIN LATERAL hedvi_live_limits(p.id,statement_timestamp()) lim WHERE p.id = $1`,
			a.ProjectID).Scan(&pname, &status, &reason, &review, &hourly, &daily, &orgHourly, &orgDaily); err != nil {
			return err
		}
		var rv any
		_ = json.Unmarshal(review, &rv)
		out["project"] = map[string]any{"id": a.ProjectID, "name": pname, "sending_status": status, "status_reason": reason,
			"review_request": rv, "hourly_limit": hourly, "daily_limit": daily, "org_hourly_limit": orgHourly, "org_daily_limit": orgDaily}
		out["role"] = a.Role
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) updateOrgSettings(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		Name                  *string `json:"name"`
		ContentRetentionDays  *int    `json:"content_retention_days"`
		MetadataRetentionDays *int    `json:"metadata_retention_days"`
		UseCreditsForOverage  *bool   `json:"use_credits_for_overage"`
		OverageEnabled        *bool   `json:"overage_enabled"`
		OverageCap            *int64  `json:"overage_cap"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		var planID string
		if err := tx.QueryRow(r.Context(), `SELECT hedvi_effective_plan(id,statement_timestamp()) FROM organizations WHERE id = $1 FOR UPDATE`, a.OrgID).Scan(&planID); err != nil {
			return err
		}
		p, _ := plans.Get(planID)
		changes := map[string]any{}
		if req.Name != nil {
			n := strings.TrimSpace(*req.Name)
			if n == "" || len(n) > 100 {
				return apierr.New(422, "validation_error", "Organization name must be 1-100 characters.")
			}
			if _, err := tx.Exec(r.Context(), `UPDATE organizations SET name = $2 WHERE id = $1`, a.OrgID, n); err != nil {
				return err
			}
			changes["name"] = n
		}
		if v := req.ContentRetentionDays; v != nil {
			if *v < 0 || *v > p.MaxContentDays {
				return apierr.New(422, "validation_error", "Content retention must be between 0 and %d days. 0 means deleted after processing.", p.MaxContentDays)
			}
			if _, err := tx.Exec(r.Context(), `UPDATE organizations SET content_retention_days = $2 WHERE id = $1`, a.OrgID, *v); err != nil {
				return err
			}
			changes["content_retention_days"] = *v
		}
		if v := req.MetadataRetentionDays; v != nil {
			if *v < 1 || *v > p.MetadataDays {
				return apierr.New(422, "validation_error", "Metadata retention must be between 1 and %d days on your plan.", p.MetadataDays)
			}
			if _, err := tx.Exec(r.Context(), `UPDATE organizations SET metadata_retention_days = $2 WHERE id = $1`, a.OrgID, *v); err != nil {
				return err
			}
			changes["metadata_retention_days"] = *v
		}
		if req.UseCreditsForOverage != nil {
			if _, err := tx.Exec(r.Context(), `UPDATE organizations SET use_credits_for_overage = $2 WHERE id = $1`, a.OrgID, *req.UseCreditsForOverage); err != nil {
				return err
			}
			changes["use_credits_for_overage"] = *req.UseCreditsForOverage
		}
		if req.OverageEnabled != nil || req.OverageCap != nil {
			return apierr.New(422, "validation_error", "Postpaid overage is unavailable. Prepaid Flex credits can cover additional usage.")
		}
		return s.audit(r.Context(), tx, a, "organization.settings_updated", "organization", a.OrgID.String(), changes, r)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.getSettings(w, r, a)
}

// Kept for existing clients. Current onboarding activates through verified DNS;
// this endpoint cannot approve, unpause, or change a delivery limit.
func (s *Server) requestLiveAccess(w http.ResponseWriter, r *http.Request, a *access) {
	var req struct {
		Website       string `json:"website"`
		UseCase       string `json:"use_case"`
		MonthlyVolume int    `json:"monthly_volume"`
		Consent       string `json:"recipient_consent"`
		AcceptAUP     bool   `json:"accept_aup"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	var state string
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT sending_status FROM projects WHERE id=$1`, a.ProjectID).Scan(&state); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `UPDATE domains SET next_check_at=now() WHERE project_id=$1 AND deleted_at IS NULL AND provider_state='active'`, a.ProjectID)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"sending_status": state, "automatic": true, "message": "Verify your domain. Live sending activates automatically; no manual application is needed."})
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request, a *access) {
	var out map[string]any
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		org, err := s.orgQuota(r.Context(), tx, a.OrgID)
		if err != nil {
			return err
		}
		u, err := quota.LoadUsage(r.Context(), tx, org, time.Now())
		if err != nil {
			return err
		}
		out = httpapi.UsageJSON(u)
		out["plan"] = org.Plan
		var ledger []map[string]any
		rows, err := tx.Query(r.Context(), `SELECT kind, source, quantity, data_bytes, note, created_at FROM credit_ledger
			WHERE org_id = $1 AND kind IN ('grant', 'expire', 'adjust') ORDER BY created_at DESC LIMIT 50`, a.OrgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var kind, source, note string
			var q, d int64
			var at time.Time
			if err := rows.Scan(&kind, &source, &q, &d, &note, &at); err != nil {
				return err
			}
			ledger = append(ledger, map[string]any{"kind": kind, "source": source, "quantity": q, "data_bytes": d, "note": note, "created_at": ts(at)})
		}
		out["ledger"] = ledger
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) members(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT u.id, u.email, u.name, m.role, u.totp_enabled_at IS NOT NULL, m.created_at
			FROM memberships m JOIN users u ON u.id = m.user_id WHERE m.org_id = $1 ORDER BY m.created_at`, a.OrgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			var email, name, role string
			var mfa bool
			var at time.Time
			if err := rows.Scan(&id, &email, &name, &role, &mfa, &at); err != nil {
				return err
			}
			out = append(out, map[string]any{"id": id, "email": email, "name": name, "role": role, "mfa_enabled": mfa, "joined_at": ts(at)})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

func (s *Server) auditLog(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	err := s.tx(r.Context(), a, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT l.action, l.actor_kind, coalesce(u.email, ''), l.target_type, l.target_id, l.metadata, l.created_at
			FROM audit_log l LEFT JOIN users u ON u.id = l.actor_user_id WHERE l.org_id = $1 ORDER BY l.created_at DESC LIMIT 200`, a.OrgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var action, kind, email, tt, tid string
			var meta []byte
			var at time.Time
			if err := rows.Scan(&action, &kind, &email, &tt, &tid, &meta, &at); err != nil {
				return err
			}
			var m any
			_ = json.Unmarshal(meta, &m)
			out = append(out, map[string]any{"action": action, "actor_kind": kind, "actor": email, "target_type": tt, "target_id": tid,
				"metadata": m, "created_at": ts(at)})
		}
		return rows.Err()
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// devOutbox shows what the local SES simulator would have sent.
func (s *Server) devOutbox(w http.ResponseWriter, r *http.Request, a *access) {
	out := []map[string]any{}
	rows, err := s.StaffPool.Query(r.Context(), `SELECT message_id, from_addr, recipients, length(raw), created_at
		FROM localmock.ses_outbound WHERE tags->>'hedvi_project' = $1 ORDER BY created_at DESC LIMIT 50`, a.ProjectID.String())
	if err != nil {
		writeJSON(w, 200, map[string]any{"data": out})
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, from string
		var rcpts []string
		var size int
		var at time.Time
		if err := rows.Scan(&id, &from, &rcpts, &size, &at); err != nil {
			s.fail(w, r, err)
			return
		}
		out = append(out, map[string]any{"message_id": id, "from": from, "recipients": rcpts, "bytes": size, "created_at": ts(at)})
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

var _ = blob.PayloadPrefix
