// Package httpapi serves the public API at api.hedvi.dev: a documented subset
// of Resend's API (docs/compatibility.md) plus Hedvi extensions under
// /hedvi/v1.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/keys"
	"hedvi.dev/api/internal/message"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/quota"
	"hedvi.dev/api/internal/ratelimit"
	"hedvi.dev/api/internal/secure"
)

type Server struct {
	Pool    *pgxpool.Pool
	Hasher  *secure.Hasher
	Accept  *accept.Service
	Domains *domains.Service
	Blobs   blob.Store
	Limiter ratelimit.Limiter
	Log     *slog.Logger
	// Request rate is enforced per key and across the entire organization.
	TrustedProxies []netip.Prefix
	touchInFlight  atomic.Int32
	KeyRate        float64
	KeyBurst       int
}

// MaxBody covers 10 MB of decoded content after base64 expansion.
const MaxBody = 15 << 20

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /emails", s.auth(false, s.sendEmail))
	mux.HandleFunc("GET /emails", s.auth(true, s.listEmails))
	mux.HandleFunc("GET /emails/{id}", s.auth(true, s.getEmail))
	mux.HandleFunc("POST /domains", s.auth(true, liveProjectKey(s.createDomain)))
	mux.HandleFunc("GET /domains", s.auth(true, liveProjectKey(s.listDomains)))
	mux.HandleFunc("GET /domains/{id}", s.auth(true, liveProjectKey(s.getDomain)))
	mux.HandleFunc("POST /domains/{id}/verify", s.auth(true, liveProjectKey(s.verifyDomain)))
	mux.HandleFunc("DELETE /domains/{id}", s.auth(true, liveProjectKey(s.deleteDomain)))

	mux.HandleFunc("GET /hedvi/v1/emails/{id}", s.auth(true, s.getEmailDetail))
	mux.HandleFunc("GET /hedvi/v1/usage", s.auth(true, liveProjectKey(s.usage)))

	// Known Resend endpoints that are not supported yet fail loudly.
	for _, p := range []string{
		"POST /emails/batch", "PATCH /emails/{id}", "POST /emails/{id}/cancel", "GET /emails/{id}/attachments",
		"PATCH /domains/{id}", "GET /api-keys", "POST /api-keys", "DELETE /api-keys/{id}", "GET /webhooks", "POST /webhooks",
		"GET /audiences", "POST /audiences", "GET /contacts", "POST /contacts", "GET /broadcasts", "POST /broadcasts",
		"GET /templates", "POST /templates", "GET /emails/receiving", "GET /segments", "GET /topics",
	} {
		mux.HandleFunc(p, unsupported)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Pool.Ping(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "database unavailable"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
}

func unsupported(w http.ResponseWriter, r *http.Request) {
	apierr.Write(w, apierr.New(404, "not_found", "%s %s is not supported by Hedvi yet. See %s", r.Method, r.URL.Path, message.CompatibilityURL))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type principalKey struct{}

func principal(r *http.Request) accept.Principal {
	return r.Context().Value(principalKey{}).(accept.Principal)
}

// auth authenticates an API key. The key's mode (test/live) comes from the
// database row; the prefix is never trusted.
func (s *Server) auth(needFull bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			apierr.Write(w, apierr.ErrMissingKey)
			return
		}
		lookup, secret, err := keys.Parse(strings.TrimSpace(token))
		if err != nil {
			apierr.Write(w, apierr.ErrInvalidKey)
			return
		}
		if s.Limiter != nil {
			ip := "unknown"
			if peer := auth.ClientIP(r, s.TrustedProxies); peer != nil {
				ip = peer.String()
			}
			if d := s.Limiter.Allow(r.Context(), "api-preauth:"+ip, 50, 100); !d.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(max(1, int(d.Reset.Seconds()+0.999))))
				apierr.Write(w, apierr.New(429, "rate_limit_exceeded", "Too many API requests from this address."))
				return
			}
		}
		var p accept.Principal
		var hash []byte
		var revoked *time.Time
		err = s.Pool.QueryRow(r.Context(), `SELECT id, org_id, project_id, mode, permission, domain_id, secret_hash, revoked_at
			FROM hedvi_lookup_api_key($1)`, lookup).Scan(&p.KeyID, &p.OrgID, &p.ProjectID, &p.Mode, &p.Permission, &p.DomainID, &hash, &revoked)
		if err != nil || revoked != nil || !keys.Verify(s.Hasher, secret, hash) {
			if err != nil && !db.IsNoRows(err) {
				apierr.Write(w, err)
				return
			}
			apierr.Write(w, apierr.ErrInvalidKey)
			return
		}
		if (needFull && p.Permission != "full_access") || (p.Mode == "test" && p.DomainID != nil) {
			apierr.Write(w, apierr.ErrRestricted)
			return
		}
		if s.Limiter != nil {
			// Keys and projects are customer-created. An organization bucket
			// prevents key rotation/multiplication from increasing throughput.
			org := s.Limiter.Allow(r.Context(), "api-org:"+p.OrgID.String(), s.KeyRate, s.KeyBurst)
			d := s.Limiter.Allow(r.Context(), "key:"+p.KeyID.String(), s.KeyRate, s.KeyBurst)
			if !org.Allowed || (d.Allowed && org.Remaining < d.Remaining) {
				d = org
			}
			w.Header().Set("ratelimit-limit", strconv.Itoa(d.Limit))
			w.Header().Set("ratelimit-remaining", strconv.Itoa(d.Remaining))
			w.Header().Set("ratelimit-reset", strconv.Itoa(int(d.Reset.Seconds()+0.999)))
			if !d.Allowed {
				w.Header().Set("retry-after", strconv.Itoa(max(1, int(d.Reset.Seconds()+0.999))))
				apierr.Write(w, apierr.New(429, "rate_limit_exceeded", "Too many requests. Limit is %.0f requests per second shared by all API keys in the organization.", s.KeyRate))
				return
			}
		}
		if s.touchInFlight.Add(1) <= 2 {
			go func() { defer s.touchInFlight.Add(-1); s.touchKey(p) }()
		} else {
			s.touchInFlight.Add(-1)
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// Shared domains and organization billing require an unrestricted live key.
// A test key must never mutate a real SES identity or inspect live usage.
func liveProjectKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		if p.Mode != "live" || p.DomainID != nil {
			apierr.Write(w, apierr.ErrRestricted)
			return
		}
		next(w, r)
	}
}

func (s *Server) touchKey(p accept.Principal) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = db.InOrg(ctx, s.Pool, p.OrgID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1
			AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, p.KeyID)
		return err
	})
}

func (s *Server) sendEmail(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			apierr.Write(w, apierr.New(413, "validation_error", "Request body is larger than %d bytes.", MaxBody))
			return
		}
		apierr.Write(w, apierr.New(400, "validation_error", "Could not read request body."))
		return
	}
	p := principal(r)
	res, err := s.Accept.Send(r.Context(), p, r.Header.Get("Idempotency-Key"), body)
	if err != nil {
		var ae *apierr.Error
		if !errors.As(err, &ae) {
			s.Log.Error("accept email", "request_id", RequestID(r.Context()), "err", err)
		}
		apierr.WriteSend(w, err)
		return
	}
	if !res.Replayed {
		metrics.Accepted.WithLabelValues(p.Mode).Inc()
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.Status)
	_, _ = w.Write(res.Body)
}

// EmailRow is shared by the public API and the console.
type EmailRow struct {
	ID              uuid.UUID
	ProjectID       uuid.UUID
	Mode            string
	From            string
	Subject         *string
	To, Cc, Bcc     []string
	ReplyTo         []string
	Tags            []message.Tag
	LastEvent       string
	LastEventAt     time.Time
	SendState       string
	PayloadRef      *string
	ProviderMessage *string
	CreatedAt       time.Time
	ContentExpires  time.Time
	ContentDeleted  *time.Time
	AttachmentCount int
	ResendOf        *uuid.UUID
}

const EmailColumns = `id, project_id, mode, from_address, subject, to_addresses, cc_addresses, bcc_addresses, reply_to, tags,
	last_event, last_event_at, send_state, payload_ref, provider_message_id, created_at, content_expires_at, content_deleted_at,
	attachment_count, resend_of`

func ScanEmail(row pgx.Row) (*EmailRow, error) {
	var e EmailRow
	var tags []byte
	if err := row.Scan(&e.ID, &e.ProjectID, &e.Mode, &e.From, &e.Subject, &e.To, &e.Cc, &e.Bcc, &e.ReplyTo, &tags,
		&e.LastEvent, &e.LastEventAt, &e.SendState, &e.PayloadRef, &e.ProviderMessage, &e.CreatedAt, &e.ContentExpires,
		&e.ContentDeleted, &e.AttachmentCount, &e.ResendOf); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(tags, &e.Tags)
	return &e, nil
}

func ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func orNull(list []string) any {
	if len(list) == 0 {
		return nil
	}
	return list
}

func (s *Server) loadEmail(ctx context.Context, p accept.Principal, idStr string) (*EmailRow, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, apierr.ErrNotFound
	}
	var e *EmailRow
	err = db.InOrg(ctx, s.Pool, p.OrgID, func(tx pgx.Tx) error {
		var err error
		e, err = ScanEmail(tx.QueryRow(ctx, `SELECT `+EmailColumns+` FROM emails WHERE id = $1 AND project_id = $2 AND mode = $3 AND ($4::uuid IS NULL OR domain_id = $4)`, id, p.ProjectID, p.Mode, p.DomainID))
		return err
	})
	if db.IsNoRows(err) {
		return nil, apierr.ErrNotFound
	}
	return e, err
}

// Content loads the stored payload if content retention still allows it.
func Content(ctx context.Context, store blob.Store, e *EmailRow) (*message.Message, error) {
	if e.PayloadRef == nil || e.ContentDeleted != nil {
		return nil, nil
	}
	raw, err := store.Get(ctx, *e.PayloadRef)
	if errors.Is(err, blob.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return message.Load(raw)
}

func (s *Server) emailJSON(ctx context.Context, e *EmailRow, withBody bool) (map[string]any, error) {
	subject := ""
	if e.Subject != nil {
		subject = *e.Subject
	}
	out := map[string]any{
		"object": "email", "id": e.ID.String(), "to": e.To, "from": e.From, "created_at": ts(e.CreatedAt),
		"subject": subject, "bcc": orNull(e.Bcc), "cc": orNull(e.Cc), "reply_to": orNull(e.ReplyTo),
		"last_event": e.LastEvent, "scheduled_at": nil,
		"hedvi": map[string]any{"mode": e.Mode, "send_state": e.SendState, "content_expires_at": ts(e.ContentExpires),
			"content_available": e.ContentDeleted == nil},
	}
	if e.ProviderMessage != nil {
		out["message_id"] = *e.ProviderMessage
	}
	if withBody {
		out["html"], out["text"] = nil, nil
		tags := []map[string]string{}
		for _, t := range e.Tags {
			tags = append(tags, map[string]string{"name": t.Name, "value": t.Value})
		}
		out["tags"] = tags
		m, err := Content(ctx, s.Blobs, e)
		if err != nil {
			return nil, err
		}
		if m != nil {
			if m.HTML != "" {
				out["html"] = m.HTML
			}
			if m.Text != "" {
				out["text"] = m.Text
			}
		}
	}
	return out, nil
}

func (s *Server) getEmail(w http.ResponseWriter, r *http.Request) {
	e, err := s.loadEmail(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		apierr.Write(w, err)
		return
	}
	out, err := s.emailJSON(r.Context(), e, true)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// Pagination parses Resend-style limit/after/before. Email ids are UUIDv7,
// so id order is creation order.
func Pagination(r *http.Request) (limit int, after, before *uuid.UUID, err error) {
	limit = 20
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 100 {
			return 0, nil, nil, apierr.New(422, "validation_error", "`limit` must be between 1 and 100.")
		}
	}
	parse := func(name string) (*uuid.UUID, error) {
		v := r.URL.Query().Get(name)
		if v == "" {
			return nil, nil
		}
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, apierr.New(422, "validation_error", "`%s` must be an email id.", name)
		}
		return &id, nil
	}
	if after, err = parse("after"); err != nil {
		return
	}
	if before, err = parse("before"); err != nil {
		return
	}
	if after != nil && before != nil {
		err = apierr.New(422, "validation_error", "Use either `after` or `before`, not both.")
	}
	return
}

func (s *Server) listEmails(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	limit, after, before, err := Pagination(r)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	var list []*EmailRow
	err = db.InOrg(r.Context(), s.Pool, p.OrgID, func(tx pgx.Tx) error {
		q := `SELECT ` + EmailColumns + ` FROM emails WHERE project_id = $1 AND mode = $3 AND ($4::uuid IS NULL OR domain_id = $4)`
		args := []any{p.ProjectID, limit + 1, p.Mode, p.DomainID}
		order := " ORDER BY id DESC"
		switch {
		case after != nil:
			q += " AND id < $5"
			args = append(args, *after)
		case before != nil:
			q += " AND id > $5"
			args = append(args, *before)
			order = " ORDER BY id ASC"
		}
		rows, err := tx.Query(r.Context(), q+order+" LIMIT $2", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e, err := ScanEmail(rows)
			if err != nil {
				return err
			}
			list = append(list, e)
		}
		return rows.Err()
	})
	if err != nil {
		apierr.Write(w, err)
		return
	}
	hasMore := len(list) > limit
	if hasMore {
		list = list[:limit]
	}
	if before != nil {
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	}
	data := make([]map[string]any, 0, len(list))
	for _, e := range list {
		item, _ := s.emailJSON(r.Context(), e, false)
		data = append(data, item)
	}
	writeJSON(w, 200, map[string]any{"object": "list", "has_more": hasMore, "data": data})
}

type RecipientView struct {
	Email        string    `json:"email"`
	Field        string    `json:"field"`
	Status       string    `json:"status"`
	StatusDetail string    `json:"status_detail,omitempty"`
	StatusAt     time.Time `json:"status_at"`
	Billing      string    `json:"billing"`
}

type EventView struct {
	Type       string         `json:"type"`
	Recipient  string         `json:"recipient,omitempty"`
	OccurredAt time.Time      `json:"occurred_at"`
	ReceivedAt time.Time      `json:"received_at"`
	Detail     map[string]any `json:"detail,omitempty"`
}

type AttemptView struct {
	ID         uuid.UUID  `json:"id"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ErrorCode  string     `json:"error_code,omitempty"`
}

// Timeline loads recipients, provider events and send attempts.
func Timeline(ctx context.Context, tx pgx.Tx, emailID uuid.UUID) (rs []RecipientView, evs []EventView, as []AttemptView, err error) {
	rows, err := tx.Query(ctx, `SELECT address, field, status, status_detail, status_at, billing_state FROM recipients
		WHERE email_id = $1 ORDER BY CASE field WHEN 'to' THEN 0 WHEN 'cc' THEN 1 ELSE 2 END, address`, emailID)
	if err != nil {
		return
	}
	for rows.Next() {
		var v RecipientView
		if err = rows.Scan(&v.Email, &v.Field, &v.Status, &v.StatusDetail, &v.StatusAt, &v.Billing); err != nil {
			rows.Close()
			return
		}
		rs = append(rs, v)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT e.type, coalesce(r.address, ''), e.occurred_at, e.received_at, e.detail FROM email_events e
		LEFT JOIN recipients r ON r.id = e.recipient_id WHERE e.email_id = $1 ORDER BY e.occurred_at, e.received_at`, emailID)
	if err != nil {
		return
	}
	for rows.Next() {
		var v EventView
		var detail []byte
		if err = rows.Scan(&v.Type, &v.Recipient, &v.OccurredAt, &v.ReceivedAt, &detail); err != nil {
			rows.Close()
			return
		}
		_ = json.Unmarshal(detail, &v.Detail)
		evs = append(evs, v)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id, state, started_at, finished_at, error_code FROM send_attempts
		WHERE email_id = $1 ORDER BY started_at`, emailID)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var v AttemptView
		if err = rows.Scan(&v.ID, &v.State, &v.StartedAt, &v.FinishedAt, &v.ErrorCode); err != nil {
			return
		}
		as = append(as, v)
	}
	err = rows.Err()
	return
}

func (s *Server) getEmailDetail(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	e, err := s.loadEmail(r.Context(), p, r.PathValue("id"))
	if err != nil {
		apierr.Write(w, err)
		return
	}
	out, err := s.emailJSON(r.Context(), e, false)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	err = db.InOrg(r.Context(), s.Pool, p.OrgID, func(tx pgx.Tx) error {
		rs, evs, as, err := Timeline(r.Context(), tx, e.ID)
		out["recipients"], out["events"], out["attempts"] = rs, evs, as
		return err
	})
	if err != nil {
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var u quota.Usage
	err := db.InOrg(r.Context(), s.Pool, p.OrgID, func(tx pgx.Tx) error {
		var org quota.Org
		org.ID = p.OrgID
		if err := tx.QueryRow(r.Context(), `SELECT hedvi_effective_plan(id,statement_timestamp()), use_credits_for_overage, overage_enabled, overage_cap_recipients
			FROM organizations WHERE id = $1`, p.OrgID).Scan(&org.Plan, &org.UseCreditsForOverage, &org.OverageEnabled, &org.OverageCap); err != nil {
			return err
		}
		var err error
		u, err = quota.LoadUsage(r.Context(), tx, org, time.Now())
		return err
	})
	if err != nil {
		s.logIfInternal(r, err)
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, UsageJSON(u))
}

func UsageJSON(u quota.Usage) map[string]any {
	credits := []map[string]any{}
	for _, c := range u.Credits {
		remaining := int64(0)
		if c.Available {
			remaining = c.Granted - c.Reserved - c.Consumed - c.Expired
		}
		credits = append(credits, map[string]any{"available": c.Available, "id": c.ID, "granted": c.Granted, "reserved": c.Reserved, "consumed": c.Consumed,
			"expired": c.Expired, "remaining": remaining, "expires_at": ts(c.ExpiresAt), "expired_at": c.ExpiredAt})
	}
	return map[string]any{
		"object": "usage", "period_start": ts(u.PeriodStart), "period_end": ts(u.PeriodEnd),
		"recipients":            map[string]int64{"included": u.Included, "reserved": u.Reserved, "consumed": u.Consumed, "remaining": u.RemainingIncluded()},
		"overage":               map[string]int64{"cap": u.OverageCap, "reserved": u.OverageReserved, "consumed": u.OverageConsumed},
		"attachment_data_bytes": map[string]int64{"included": u.DataIncluded, "reserved": u.DataReserved, "consumed": u.DataConsumed},
		"credits":               credits,
		"hard_stop":             true,
	}
}

type createDomainRequest struct {
	Name             string `json:"name"`
	Region           string `json:"region"`
	CustomReturnPath string `json:"custom_return_path"`
	OpenTracking     *bool  `json:"open_tracking"`
	ClickTracking    *bool  `json:"click_tracking"`
	TLS              string `json:"tls"`
	Capabilities     *struct {
		Sending   *string `json:"sending"`
		Receiving *string `json:"receiving"`
	} `json:"capabilities"`
	TrackingSubdomain string `json:"tracking_subdomain"`
}

func (s *Server) createDomain(w http.ResponseWriter, r *http.Request) {
	var req createDomainRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		apierr.Write(w, apierr.New(422, "validation_error", "Invalid request body: %s", err))
		return
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		apierr.Write(w, apierr.New(422, "validation_error", "Request body must contain a single JSON object."))
		return
	}
	switch {
	case req.Name == "":
		apierr.Write(w, apierr.New(422, "missing_required_field", "Missing `name` field."))
		return
	case req.Region != "" && req.Region != "eu-north-1":
		apierr.Write(w, apierr.New(422, "invalid_region", "Hedvi sends from eu-north-1 (Stockholm) only."))
		return
	case (req.OpenTracking != nil && *req.OpenTracking) || (req.ClickTracking != nil && *req.ClickTracking) || req.TrackingSubdomain != "":
		apierr.Write(w, apierr.New(422, "validation_error", "Open and click tracking are not supported yet. See %s", message.CompatibilityURL))
		return
	case req.TLS != "" && req.TLS != "opportunistic":
		apierr.Write(w, apierr.New(422, "validation_error", "Only opportunistic TLS is supported."))
		return
	case req.Capabilities != nil && ((req.Capabilities.Receiving != nil && *req.Capabilities.Receiving != "disabled") || (req.Capabilities.Sending != nil && *req.Capabilities.Sending != "enabled")):
		apierr.Write(w, apierr.New(422, "validation_error", "Domain capabilities support sending enabled and receiving disabled only."))
		return
	}
	p := principal(r)
	d, err := s.Domains.Create(r.Context(), p.OrgID, p.ProjectID, req.Name, req.CustomReturnPath)
	if err != nil {
		s.logIfInternal(r, err)
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, DomainJSON(d, true))
}

func (s *Server) logIfInternal(r *http.Request, err error) {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		s.Log.Error("request failed", "request_id", RequestID(r.Context()), "route", r.Pattern, "err", err)
	}
}

func DomainJSON(d *domains.Domain, withRecords bool) map[string]any {
	out := map[string]any{
		"object": "domain", "id": d.ID.String(), "name": d.Name, "status": d.Status, "created_at": ts(d.CreatedAt),
		"region": d.Region, "open_tracking": false, "click_tracking": false,
		"capabilities": map[string]string{"sending": "enabled", "receiving": "disabled"},
	}
	if withRecords {
		records := d.Records
		if records == nil {
			records = []domains.Record{}
		}
		out["records"] = records
		var last, next any
		if d.LastCheckedAt != nil {
			last = ts(*d.LastCheckedAt)
		}
		if d.NextCheckAt != nil {
			next = ts(*d.NextCheckAt)
		}
		out["hedvi"] = map[string]any{"analysis": d.Analysis, "last_checked_at": last, "next_check_at": next,
			"mail_from_domain": d.MailFrom + "." + d.Name, "mail_from_revision": d.MailFromRevision}
	}
	return out
}

func (s *Server) domainID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		apierr.Write(w, apierr.ErrNotFound)
		return id, false
	}
	return id, true
}

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	list, err := s.Domains.List(r.Context(), p.OrgID, p.ProjectID)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	data := make([]map[string]any, 0, len(list))
	for _, d := range list {
		data = append(data, DomainJSON(d, false))
	}
	writeJSON(w, 200, map[string]any{"object": "list", "has_more": false, "data": data})
}

func (s *Server) getDomain(w http.ResponseWriter, r *http.Request) {
	id, ok := s.domainID(w, r)
	if !ok {
		return
	}
	p := principal(r)
	d, err := s.Domains.Get(r.Context(), p.OrgID, p.ProjectID, id)
	if err != nil {
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, DomainJSON(d, true))
}

func (s *Server) verifyDomain(w http.ResponseWriter, r *http.Request) {
	id, ok := s.domainID(w, r)
	if !ok {
		return
	}
	p := principal(r)
	d, err := s.Domains.Verify(r.Context(), p.OrgID, p.ProjectID, id)
	if err != nil {
		s.logIfInternal(r, err)
		apierr.WriteDomainCheck(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "domain", "id": d.ID.String(), "hedvi": map[string]string{"status": d.Status}})
}

func (s *Server) deleteDomain(w http.ResponseWriter, r *http.Request) {
	id, ok := s.domainID(w, r)
	if !ok {
		return
	}
	p := principal(r)
	if err := s.Domains.Delete(r.Context(), p.OrgID, p.ProjectID, id); err != nil {
		apierr.Write(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "domain", "id": id.String(), "deleted": true})
}
