// Package console serves the dashboard API (/console/v1) used by apps/web.
// Authentication uses HttpOnly session cookies; state-changing requests
// require a custom header (CSRF protection) and a matching Origin.
package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/accept"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/billing"
	"hedvi.dev/api/internal/blob"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/domains"
	"hedvi.dev/api/internal/httpapi"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/ratelimit"
	"hedvi.dev/api/internal/secure"
)

type Server struct {
	AuthBegin db.Beginner // optional transaction fault injection; production uses Pool
	Billing   *billing.Service
	Cfg       *config.Config
	Pool      *pgxpool.Pool // API role, row level security applies
	StaffPool *pgxpool.Pool // local development only; deployed API has no worker credential
	Hasher    *secure.Hasher
	Sealer    *secure.Sealer
	Accept    *accept.Service
	Domains   *domains.Service
	Provider  provider.Provider
	Blobs     blob.Store
	Limiter   ratelimit.Limiter
	Log       *slog.Logger
	API       *httpapi.Server
}

const (
	sessionTTL     = 14 * 24 * time.Hour
	sessionIdle    = 3 * 24 * time.Hour
	csrfHeader     = "X-Hedvi-Console"
	sessionPurpose = "session"
)

func (s *Server) cookieName() string {
	if s.Cfg.CookieSecure {
		return "__Host-hedvi_session"
	}
	return "hedvi_session"
}

func (s *Server) Routes(mux *http.ServeMux) {
	p := "/console/v1"
	mux.HandleFunc("POST "+p+"/auth/signup", s.guard(s.signup))
	mux.HandleFunc("POST "+p+"/auth/signup/complete", s.guard(s.signupComplete))
	mux.HandleFunc("POST "+p+"/auth/password-reset/request", s.guard(s.requestReset))
	mux.HandleFunc("POST "+p+"/auth/password-reset/complete", s.guard(s.completeReset))
	mux.HandleFunc("GET "+p+"/auth/legal", s.legalManifest)
	mux.HandleFunc("POST "+p+"/auth/legal/accept", s.guard(s.withSession(s.acceptLegal)))
	mux.HandleFunc("POST "+p+"/auth/login", s.guard(s.login))
	mux.HandleFunc("POST "+p+"/auth/logout", s.guard(s.logout))
	mux.HandleFunc("GET "+p+"/me", s.withSession(s.me))
	mux.HandleFunc("POST "+p+"/me/mfa/setup", s.guard(s.withSession(s.mfaSetup)))
	mux.HandleFunc("POST "+p+"/me/mfa/enable", s.guard(s.withSession(s.mfaEnable)))
	mux.HandleFunc("POST "+p+"/me/mfa/disable", s.guard(s.withSession(s.mfaDisable)))
	mux.HandleFunc("POST "+p+"/orgs/{org}/projects", s.guard(s.withSession(s.createProject)))

	all := []string{"owner", "admin", "developer", "billing"}
	dev := []string{"owner", "admin", "developer"}
	admin := []string{"owner", "admin"}
	cost := []string{"owner", "admin", "billing"}
	pr := func(method, path string, roles []string, h projectHandler) {
		hf := s.withProject(roles, h)
		if method != "GET" {
			hf = s.guard(hf)
		}
		mux.HandleFunc(method+" "+p+"/projects/{project}"+path, hf)
	}
	pr("GET", "/billing", cost, s.billingStatus)
	pr("GET", "/billing/orders/{id}", cost, s.billingOrder)
	pr("POST", "/billing/checkout", cost, s.billingCheckout)
	pr("POST", "/billing/baseline", cost, s.billingBaseline)
	pr("POST", "/billing/portal", cost, s.billingPortal)
	pr("GET", "/overview", all, s.overview)
	pr("GET", "/api-keys", dev, s.listKeys)
	pr("POST", "/api-keys", dev, s.createKey)
	pr("POST", "/api-keys/{id}/revoke", dev, s.revokeKey)
	pr("GET", "/emails", dev, s.listEmails)
	pr("GET", "/emails/{id}", dev, s.emailDetail)
	pr("GET", "/emails/{id}/attachments/{index}", dev, s.attachment)
	pr("POST", "/emails/{id}/resend", dev, s.resendEmail)
	pr("GET", "/test-inbox", dev, s.testInbox)
	pr("POST", "/test-inbox/sample", dev, s.sendSample)
	pr("DELETE", "/test-inbox", dev, s.clearTestInbox)
	pr("DELETE", "/test-inbox/{id}", dev, s.deleteTestEmail)
	pr("GET", "/domains", dev, s.listDomains)
	pr("POST", "/domains", dev, s.createDomain)
	pr("GET", "/domains/{id}", dev, s.getDomain)
	pr("POST", "/domains/{id}/verify", dev, s.verifyDomain)
	pr("DELETE", "/domains/{id}", dev, s.deleteDomain)
	pr("GET", "/webhooks", dev, s.listWebhooks)
	pr("POST", "/webhooks", dev, s.createWebhook)
	pr("PATCH", "/webhooks/{id}", dev, s.updateWebhook)
	pr("DELETE", "/webhooks/{id}", dev, s.deleteWebhook)
	pr("POST", "/webhooks/{id}/secret", dev, s.revealSecret)
	pr("POST", "/webhooks/{id}/rotate-secret", dev, s.rotateSecret)
	pr("POST", "/webhooks/{id}/test", dev, s.testWebhook)
	pr("GET", "/webhooks/{id}/deliveries", dev, s.listDeliveries)
	pr("POST", "/webhooks/{id}/deliveries/{delivery}/resend", dev, s.resendDelivery)
	pr("GET", "/suppressions", dev, s.listSuppressions)
	pr("POST", "/suppressions", dev, s.addSuppression)
	pr("DELETE", "/suppressions/{id}", admin, s.removeSuppression)
	pr("GET", "/settings", all, s.getSettings)
	pr("PATCH", "/settings/org", cost, s.updateOrgSettings)
	pr("POST", "/live-access", admin, s.requestLiveAccess)
	pr("GET", "/usage", all, s.usage)
	pr("GET", "/members", all, s.members)
	pr("GET", "/audit", admin, s.auditLog)
	pr("GET", "/alerts", all, s.alerts)
	if s.Cfg.AWSMode == "mock" {
		pr("GET", "/dev/ses-outbox", dev, s.devOutbox)
	}

	// Hosted operator actions run through the authenticated infrastructure CLI,
	// not through the Internet-facing API process with cross-tenant credentials.
	if s.Cfg.IsDeployed() || s.StaffPool == nil {
		return
	}
	st := func(method, path string, h sessionHandler) {
		hf := s.withSession(s.staffOnly(h))
		if method != "GET" {
			hf = s.guard(hf)
		}
		mux.HandleFunc(method+" "+p+"/admin"+path, hf)
	}
	st("GET", "/reviews", s.adminReviews)
	st("POST", "/projects/{project}/approve", s.adminApprove)
	st("POST", "/projects/{project}/pause", s.adminPause)
	st("GET", "/alerts", s.adminAlerts)
	st("POST", "/alerts/{id}/resolve", s.adminResolveAlert)
	st("GET", "/unknown-attempts", s.adminUnknown)
	st("POST", "/attempts/{id}/resolve", s.adminResolveAttempt)
}

// guard enforces CSRF protection for state-changing requests.
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(csrfHeader) != "1" {
			apierr.Write(w, apierr.New(403, "security_error", "Missing %s header.", csrfHeader))
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !s.originAllowed(o) {
			apierr.Write(w, apierr.New(403, "security_error", "Cross-origin request refused."))
			return
		}
		next(w, r)
	}
}

func (s *Server) originAllowed(o string) bool {
	allowed := s.Cfg.AllowedOrigins
	if len(allowed) == 0 {
		allowed = []string{strings.TrimSuffix(s.Cfg.AppURL, "/")}
	}
	for _, a := range allowed {
		if a == o {
			return true
		}
	}
	return false
}

type session struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Email         string
	Name          string
	IsStaff       bool
	TOTPEnabled   bool
	MFAVerified   bool
	LegalAccepted bool
}

type sessionHandler func(http.ResponseWriter, *http.Request, *session)

func (s *Server) loadSession(r *http.Request) (*session, error) {
	c, err := r.Cookie(s.cookieName())
	if err != nil || c.Value == "" {
		return nil, apierr.New(401, "missing_api_key", "Sign in required.")
	}
	var sess session
	var mfaAt, totpAt *time.Time
	var lastSeen time.Time
	err = s.Pool.QueryRow(r.Context(), `SELECT s.id, s.user_id, u.email, u.name, u.is_staff, u.totp_enabled_at, s.mfa_verified_at, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE u.email_verified_at IS NOT NULL AND s.token_hash = $1 AND s.revoked_at IS NULL AND s.expires_at > now() AND s.last_seen_at > now() - $2 * interval '1 second'`,
		s.Hasher.Sum(sessionPurpose, c.Value), sessionIdle.Seconds()).Scan(
		&sess.ID, &sess.UserID, &sess.Email, &sess.Name, &sess.IsStaff, &totpAt, &mfaAt, &lastSeen)
	if err != nil {
		if db.IsNoRows(err) {
			return nil, apierr.New(401, "invalid_api_key", "Your session has expired. Sign in again.")
		}
		return nil, err
	}
	sess.TOTPEnabled = totpAt != nil
	sess.MFAVerified = mfaAt != nil
	if sess.TOTPEnabled && !sess.MFAVerified {
		return nil, apierr.New(401, "invalid_api_key", "Sign in again with your authenticator code.")
	}
	if time.Since(lastSeen) > 5*time.Minute {
		_, _ = s.Pool.Exec(r.Context(), `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, sess.ID)
	}
	sess.LegalAccepted, err = legal.HasCurrent(r.Context(), s.Pool, sess.UserID, s.Cfg.IsDeployed())
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Server) withSession(next sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.allowConsole(w, r, "console-ip:"+s.ipString(r), 20, 60) {
			return
		}
		sess, err := s.loadSession(r)
		if err != nil {
			apierr.Write(w, err)
			return
		}
		if !s.allowConsole(w, r, "console-user:"+sess.UserID.String(), 10, 30) {
			return
		}
		if !sess.LegalAccepted && r.URL.Path != "/console/v1/me" && r.URL.Path != "/console/v1/auth/legal/accept" && !strings.HasPrefix(r.URL.Path, "/console/v1/me/mfa/") {
			apierr.Write(w, apierr.New(403, "legal_acknowledgement_required", "Review the current terms before continuing."))
			return
		}
		next(w, r, sess)
	}
}

func (s *Server) staffOnly(next sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess *session) {
		if !sess.IsStaff {
			apierr.Write(w, apierr.ErrNotFound)
			return
		}
		if !sess.TOTPEnabled || !sess.MFAVerified {
			apierr.Write(w, apierr.New(403, "security_error", "Staff access requires multi-factor authentication. Enable it under your profile and sign in again."))
			return
		}
		next(w, r, sess)
	}
}

type access struct {
	*session
	OrgID     uuid.UUID
	ProjectID uuid.UUID
	Role      string
}

type projectHandler func(http.ResponseWriter, *http.Request, *access)

func (s *Server) withProject(roles []string, next projectHandler) http.HandlerFunc {
	return s.withSession(func(w http.ResponseWriter, r *http.Request, sess *session) {
		pid, err := uuid.Parse(r.PathValue("project"))
		if err != nil {
			apierr.Write(w, apierr.ErrNotFound)
			return
		}
		a := &access{session: sess, ProjectID: pid}
		err = s.Pool.QueryRow(r.Context(), `SELECT org_id, role FROM hedvi_project_access($1, $2)`, pid, sess.UserID).Scan(&a.OrgID, &a.Role)
		if err != nil {
			if db.IsNoRows(err) {
				apierr.Write(w, apierr.ErrNotFound)
				return
			}
			apierr.Write(w, err)
			return
		}
		allowed := false
		for _, role := range roles {
			allowed = allowed || role == a.Role
		}
		if !allowed {
			apierr.Write(w, apierr.New(403, "restricted_api_key", "Your role (%s) cannot do this.", a.Role))
			return
		}
		if !s.allowConsole(w, r, "console-org:"+a.OrgID.String(), 10, 30) {
			return
		}
		next(w, r, a)
	})
}

// tx runs fn scoped to the caller's user and organization.
func (s *Server) tx(ctx context.Context, a *access, fn func(pgx.Tx) error) error {
	return db.InUser(ctx, s.Pool, a.UserID, &a.OrgID, fn)
}

func (s *Server) principal(a *access, mode string) accept.Principal {
	return accept.Principal{OrgID: a.OrgID, ProjectID: a.ProjectID, Mode: mode, Permission: "full_access"}
}

func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if err != nil {
		return apierr.New(400, "validation_error", "Could not read request body.")
	}
	if len(body) > 1<<20 {
		return apierr.New(413, "validation_error", "Request body exceeds 1 MiB.")
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apierr.New(422, "validation_error", "Invalid request: %s", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return apierr.New(422, "validation_error", "Request must contain exactly one JSON object.")
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil || containsNUL(generic) {
		return apierr.New(422, "validation_error", "JSON strings must not contain NUL characters.")
	}
	return nil
}

func containsNUL(v any) bool {
	switch value := v.(type) {
	case string:
		return strings.ContainsRune(value, 0)
	case []any:
		for _, item := range value {
			if containsNUL(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range value {
			if strings.ContainsRune(key, 0) || containsNUL(item) {
				return true
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		s.Log.Error("console request failed", "request_id", httpapi.RequestID(r.Context()), "route", r.Pattern, "err", err)
	}
	apierr.Write(w, err)
}

func (s *Server) clientIP(r *http.Request) *netip.Addr {
	if ip := auth.AttestedClientIP(r, s.Cfg.ProxyAttestationKey, time.Now()); ip != nil {
		return ip
	}
	return auth.ClientIP(r, s.Cfg.TrustedProxies)
}

func (s *Server) allowConsole(w http.ResponseWriter, r *http.Request, key string, rate float64, burst int) bool {
	if s.Limiter == nil {
		return true
	}
	if d := s.Limiter.Allow(r.Context(), key, rate, burst); !d.Allowed {
		w.Header().Set("Retry-After", "5")
		apierr.Write(w, apierr.New(429, "rate_limit_exceeded", "Too many requests. Try again shortly."))
		return false
	}
	return true
}

func (s *Server) audit(ctx context.Context, tx pgx.Tx, a *access, action, targetType, targetID string, meta map[string]any, r *http.Request) error {
	if meta == nil {
		meta = map[string]any{}
	}
	var ip any
	if addr := s.clientIP(r); addr != nil {
		ip = addr.String()
	}
	_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_kind, actor_user_id, action, target_type, target_id, metadata, ip)
		VALUES ($1, 'user', $2, $3, $4, $5, $6, $7)`, a.OrgID, a.UserID, action, targetType, targetID, meta, ip)
	return err
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return id, apierr.ErrNotFound
	}
	return id, nil
}
