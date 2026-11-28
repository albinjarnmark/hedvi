package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/keys"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/secure"
)

func (s *Server) insertSession(ctx context.Context, q db.Querier, r *http.Request, userID uuid.UUID, mfa, remembered bool) (string, error) {
	token := secure.RandomToken(32)
	var mfaAt *time.Time
	if mfa {
		now := time.Now()
		mfaAt = &now
	}
	var ip any
	if a := s.clientIP(r); a != nil {
		ip = a.String()
	}
	ua := boundedAgent(r.UserAgent())
	ttl := sessionTTL
	if remembered {
		ttl = rememberedTTL
	}
	_, err := q.Exec(ctx, `INSERT INTO sessions (user_id, token_hash, mfa_verified_at, ip, user_agent, expires_at, remembered) VALUES ($1,$2,$3,$4,$5,$6,$7)`, userID, s.Hasher.Sum(sessionPurpose, token), mfaAt, ip, ua, time.Now().Add(ttl), remembered)
	return token, err
}
func (s *Server) sessionCookie(w http.ResponseWriter, token string, remembered bool) {
	cookie := &http.Cookie{Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode}
	if remembered {
		cookie.MaxAge = int(rememberedTTL.Seconds())
		cookie.Expires = time.Now().Add(rememberedTTL)
	}
	http.SetCookie(w, cookie)
}
func (s *Server) setSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID uuid.UUID, mfa bool) error {
	token, err := s.insertSession(ctx, s.Pool, r, userID, mfa, false)
	if err == nil {
		s.sessionCookie(w, token, false)
	}
	return err
}

type signupCompleteRequest struct {
	Token        string        `json:"token"`
	Consent      legal.Consent `json:"consent"`
	Password     string        `json:"password"`
	Name         string        `json:"name"`
	Organization string        `json:"organization"`
}

// NewProject creates a project row. Tenants in SES are created when the
// first domain is added or live sending is approved.
func NewProject(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, name string, env config.Env) (uuid.UUID, error) {
	// Serialize the organization-wide limit across all replicas.
	var n int
	if _, err := tx.Exec(ctx, `SELECT id FROM organizations WHERE id = $1 FOR UPDATE`, orgID); err != nil {
		return uuid.Nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM projects WHERE org_id = $1`, orgID).Scan(&n); err != nil {
		return uuid.Nil, err
	}
	if n >= 10 {
		return uuid.Nil, apierr.New(403, "validation_error", "An organization may have at most 10 projects. Contact support for a reviewed increase.")
	}
	id := uuid.Must(uuid.NewV7())
	tenant, err := env.ProjectTenant(id)
	if err != nil {
		return uuid.Nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO projects (id, org_id, name, ses_tenant_name) VALUES ($1, $2, $3, $4)`,
		id, orgID, name, tenant)
	return id, err
}

// CreateKey inserts a key and returns its plaintext once.
func CreateKey(ctx context.Context, tx pgx.Tx, h *secure.Hasher, orgID, projectID uuid.UUID, createdBy *uuid.UUID,
	name, mode, permission string, domainID *uuid.UUID) (uuid.UUID, keys.Generated, error) {
	if mode == "test" && domainID != nil {
		return uuid.Nil, keys.Generated{}, apierr.New(422, "validation_error", "Test keys cannot be restricted to a live domain.")
	}
	// Share the creation budget across sessions and replicas. Revoking a key
	// frees an active slot but must not allow unbounded creation/deletion churn.
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM organizations WHERE id=$1 FOR UPDATE`, orgID).Scan(&locked); err != nil {
		return uuid.Nil, keys.Generated{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 AND org_id=$2 FOR UPDATE`, projectID, orgID).Scan(&locked); err != nil {
		return uuid.Nil, keys.Generated{}, err
	}
	var active, recent int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FILTER (WHERE created_at > now() - interval '24 hours') FROM api_keys WHERE project_id=$1`, projectID).Scan(&active, &recent); err != nil {
		return uuid.Nil, keys.Generated{}, err
	}
	if active >= 20 || recent >= 50 {
		return uuid.Nil, keys.Generated{}, apierr.New(429, "rate_limit_exceeded", "A project may have 20 active API keys and create at most 50 keys in 24 hours.")
	}
	g := keys.Generate(h, mode)
	var id uuid.UUID
	err := tx.QueryRow(ctx, `INSERT INTO api_keys (org_id, project_id, name, mode, permission, domain_id, lookup_id, secret_hash, hint, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		orgID, projectID, name, mode, permission, domainID, g.LookupID, g.Hash, g.Hint, createdBy).Scan(&id)
	return id, g, err
}

func (s *Server) signupComplete(w http.ResponseWriter, r *http.Request) {
	if !s.authIngress(w, r, "complete") {
		return
	}
	var req signupCompleteRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := legal.Validate(req.Consent, s.Cfg.IsDeployed()); err != nil {
		s.fail(w, r, apierr.New(422, "legal_acknowledgement_required", "%s", err))
		return
	}
	release, ok := s.authCompleteAdmission(w, r)
	if !ok {
		return
	}
	defer release()
	proof, err := s.authMail().Read(r.Context(), s.Pool, req.Token, "signup", false)
	if err != nil {
		s.fail(w, r, linkError(err))
		return
	}
	addr, err := address.Parse(proof.Email)
	if err != nil {
		s.fail(w, r, linkError(authmail.ErrLink))
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.fail(w, r, apierr.New(422, "validation_error", "%s", err))
		return
	}
	locale := proof.Locale
	if locale != "sv" {
		locale = "en"
	}
	orgName := strings.TrimSpace(req.Organization)
	if orgName == "" {
		orgName = strings.SplitN(addr.Domain, ".", 2)[0]
	}
	if len(orgName) > 100 || len(req.Name) > 100 {
		s.fail(w, r, apierr.New(422, "validation_error", "Name and organization must be at most 100 characters."))
		return
	}
	hash, err := auth.HashPasswordContext(r.Context(), req.Password)
	if err != nil {
		s.fail(w, r, passwordError(err))
		return
	}
	userID := uuid.Must(uuid.NewV7())
	orgID := uuid.Must(uuid.NewV7())
	var projectID uuid.UUID
	var key keys.Generated
	var sessionToken string
	err = s.accountTx(r.Context(), userID, &orgID, func(tx pgx.Tx) error {
		if err := authmail.Lock(r.Context(), tx); err != nil {
			return err
		}
		proof, err := s.authMail().Read(r.Context(), tx, req.Token, "signup", true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO users (id, email, name, password_hash, locale,email_verified_at) VALUES ($1, $2, $3, $4, $5,now())`,
			userID, addr.Normalized, strings.TrimSpace(req.Name), hash, locale); err != nil {
			return err
		}
		currency := "USD"
		if locale == "sv" {
			currency = "SEK"
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO organizations (id, name, currency) VALUES ($1, $2, $3)`, orgID, orgName, currency); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgID, userID); err != nil {
			return err
		}
		if projectID, err = NewProject(r.Context(), tx, orgID, "Default", s.Cfg.Env); err != nil {
			return err
		}
		if _, key, err = CreateKey(r.Context(), tx, s.Hasher, orgID, projectID, &userID, "Onboarding test key", "test", "full_access", nil); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO audit_log (org_id, actor_kind, actor_user_id, action, target_type, target_id)
   VALUES ($1, 'user', $2, 'org.created', 'organization', $3)`, orgID, userID, orgID.String()); err != nil {
			return err
		}
		if err := authmail.Consume(r.Context(), tx, proof); err != nil {
			return err
		}
		if err := legal.Record(r.Context(), tx, userID, req.Consent, s.Cfg.IsDeployed(), s.legalIP(r), r.UserAgent(), s.Cfg.LegalURLOrigin()); err != nil {
			return err
		}
		sessionToken, err = s.insertSession(r.Context(), tx, r, userID, false, false)
		return err
	})
	if db.IsUniqueViolation(err, "users_email_key") {
		s.fail(w, r, linkError(authmail.ErrLink))
		return
	}
	if err != nil {
		s.fail(w, r, accountError(err))
		return
	}
	s.sessionCookie(w, sessionToken, false)
	writeJSON(w, 201, map[string]any{
		"user":         map[string]any{"id": userID, "email": addr.Normalized, "name": req.Name},
		"organization": map[string]any{"id": orgID, "name": orgName},
		"project":      map[string]any{"id": projectID, "name": "Default"},
		"test_key":     map[string]any{"key": key.Plaintext, "hint": key.Hint, "mode": "test"},
	})
}

func (s *Server) ipString(r *http.Request) string {
	if a := s.clientIP(r); a != nil {
		return a.String()
	}
	return "unknown"
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
	Remember bool   `json:"remember_me"`
}

func passwordError(err error) error {
	if errors.Is(err, auth.ErrPasswordBusy) {
		return apierr.New(503, "temporarily_unavailable", "Sign-in capacity is busy. Try again shortly.")
	}
	return err
}

func invalidCredentials() error {
	return apierr.New(401, "invalid_api_key", "The credentials are incorrect or sign-in is temporarily unavailable.")
}
func locked(until *time.Time) bool { return until != nil && until.After(time.Now()) }

// Failure counters are durable across replicas and limiter outages. Expired
// windows/locks start a new budget, and requests during a lock cannot extend it.
func recordAuthFailure(ctx context.Context, q db.Querier, id uuid.UUID, mfa bool) error {
	count, window, until, limit := "failed_logins", "login_failure_window_started_at", "locked_until", 10
	if mfa {
		count, window, until, limit = "failed_mfa", "mfa_failure_window_started_at", "mfa_locked_until", 5
	}
	reset := fmt.Sprintf("(%s IS NULL OR %s <= now()-interval '15 minutes' OR %s <= now())", window, window, until)
	next := fmt.Sprintf("CASE WHEN %s THEN 1 ELSE %s+1 END", reset, count)
	_, err := q.Exec(ctx, fmt.Sprintf(`UPDATE users SET %s=%s, %s=CASE WHEN %s THEN now() ELSE %s END,
 %s=CASE WHEN (%s)>=$2 THEN now()+interval '15 minutes' ELSE NULL END
 WHERE id=$1 AND (%s IS NULL OR %s<=now())`, count, next, window, reset, window, until, next, until, until), id, limit)
	return err
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if parsed, err := address.Parse(req.Email); err == nil && parsed.Name == "" {
		email = parsed.Normalized
	} else {
		email = ""
	}
	if len(email) > 254 || len(req.Password) > auth.MaxPasswordLen || len(req.TOTPCode) > 32 {
		s.fail(w, r, invalidCredentials())
		return
	}
	// The unauthenticated account bucket is IP-scoped; another client cannot
	// exhaust a victim's entire pre-authentication budget by naming their email.
	for _, k := range []string{"login-ip:" + s.ipString(r), "login-pair:" + s.ipString(r) + ":" + email} {
		if !s.allowConsole(w, r, k, 0.2, 10) {
			return
		}
	}
	var userID uuid.UUID
	var hash string
	var until, verified *time.Time
	err := s.Pool.QueryRow(r.Context(), `SELECT id,password_hash,locked_until,email_verified_at FROM users WHERE email=$1`, email).Scan(&userID, &hash, &until, &verified)
	if db.IsNoRows(err) {
		if err := auth.DummyCheckContext(r.Context(), req.Password); err != nil {
			s.fail(w, r, passwordError(err))
			return
		}
		s.fail(w, r, invalidCredentials())
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	ok, err := auth.CheckPasswordContext(r.Context(), hash, req.Password)
	if err != nil {
		s.fail(w, r, passwordError(err))
		return
	}
	if locked(until) {
		s.fail(w, r, invalidCredentials())
		return
	}
	if !ok {
		if err := recordAuthFailure(r.Context(), s.Pool, userID, false); err != nil {
			s.fail(w, r, err)
			return
		}
		s.fail(w, r, invalidCredentials())
		return
	}
	if verified == nil {
		s.fail(w, r, apierr.New(403, "email_verification_required", "Verify your email by requesting a password reset and choosing a new password."))
		return
	}
	var token string
	var authErr error
	// Hash outside the lock, then re-read all factors under the user lock. Invalid
	// TOTP attempts commit their counter without committing a session or code.
	err = db.Tx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		var currentHash string
		var enc []byte
		var enabled, mfaUntil *time.Time
		var last int64
		if err := tx.QueryRow(r.Context(), `SELECT password_hash,totp_secret_enc,totp_enabled_at,totp_last_step,locked_until,mfa_locked_until FROM users WHERE id=$1 AND email_verified_at IS NOT NULL FOR UPDATE`, userID).Scan(&currentHash, &enc, &enabled, &last, &until, &mfaUntil); err != nil {
			return err
		}
		if currentHash != hash || locked(until) || locked(mfaUntil) {
			authErr = invalidCredentials()
			return nil
		}
		mfa := false
		if enabled != nil {
			if req.TOTPCode == "" {
				authErr = apierr.New(401, "mfa_required", "Enter the code from your authenticator app.")
				return nil
			}
			secret, err := s.Sealer.Open(enc, totpAAD(userID))
			if err != nil {
				return err
			}
			step, ok := auth.VerifyTOTP(string(secret), req.TOTPCode, time.Now(), last)
			if !ok {
				authErr = invalidCredentials()
				return recordAuthFailure(r.Context(), tx, userID, true)
			}
			if _, err := tx.Exec(r.Context(), `UPDATE users SET totp_last_step=$2 WHERE id=$1`, userID, step); err != nil {
				return err
			}
			mfa = true
		}
		if _, err := tx.Exec(r.Context(), `UPDATE users SET failed_logins=0,locked_until=NULL,login_failure_window_started_at=NULL,failed_mfa=0,mfa_locked_until=NULL,mfa_failure_window_started_at=NULL WHERE id=$1`, userID); err != nil {
			return err
		}
		var err error
		token, err = s.insertSession(r.Context(), tx, r, userID, mfa, req.Remember)
		return err
	})
	if err == nil {
		err = authErr
	}
	if err != nil {
		var ae *apierr.Error
		if errors.As(err, &ae) && ae.Name == "mfa_required" {
			writeJSON(w, 401, map[string]any{"statusCode": 401, "name": "mfa_required", "message": ae.Message, "mfa_required": true})
		} else {
			s.fail(w, r, err)
		}
		return
	}
	s.sessionCookie(w, token, req.Remember)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func totpAAD(userID uuid.UUID) []byte { return []byte("totp:" + userID.String()) }

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName()); err == nil {
		if _, err := s.Pool.Exec(r.Context(), `UPDATE sessions SET revoked_at = now() WHERE token_hash = $1`, s.Hasher.Sum(sessionPurpose, c.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.Cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, sess *session) {
	type project struct {
		ID            uuid.UUID `json:"id"`
		Name          string    `json:"name"`
		SendingStatus string    `json:"sending_status"`
	}
	type org struct {
		ID       uuid.UUID `json:"id"`
		Name     string    `json:"name"`
		Role     string    `json:"role"`
		Plan     string    `json:"plan"`
		Currency string    `json:"currency"`
		Projects []project `json:"projects"`
	}
	var orgs []org
	var locale string
	err := db.InUser(r.Context(), s.Pool, sess.UserID, nil, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT locale FROM users WHERE id = $1`, sess.UserID).Scan(&locale); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT o.id, o.name, m.role, hedvi_effective_plan(o.id,statement_timestamp()), o.currency FROM memberships m
			JOIN organizations o ON o.id = m.org_id WHERE m.user_id = $1 ORDER BY o.created_at`, sess.UserID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var o org
			if err := rows.Scan(&o.ID, &o.Name, &o.Role, &o.Plan, &o.Currency); err != nil {
				rows.Close()
				return err
			}
			orgs = append(orgs, o)
		}
		rows.Close()
		for i := range orgs {
			// Projects are tenant rows; scope each query to its organization.
			if _, err := tx.Exec(r.Context(), `SELECT set_config('hedvi.org_id', $1, true)`, orgs[i].ID.String()); err != nil {
				return err
			}
			prow, err := tx.Query(r.Context(), `SELECT id, name, sending_status FROM projects WHERE org_id = $1 ORDER BY created_at`, orgs[i].ID)
			if err != nil {
				return err
			}
			for prow.Next() {
				var p project
				if err := prow.Scan(&p.ID, &p.Name, &p.SendingStatus); err != nil {
					prow.Close()
					return err
				}
				orgs[i].Projects = append(orgs[i].Projects, p)
			}
			prow.Close()
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"user": map[string]any{"id": sess.UserID, "email": sess.Email, "name": sess.Name, "locale": locale,
			"mfa_enabled": sess.TOTPEnabled, "is_staff": sess.IsStaff, "session_mfa": sess.MFAVerified, "legal_accepted": sess.LegalAccepted},
		"organizations": orgs,
		"environment":   map[string]any{"aws_mode": s.Cfg.AWSMode, "env": s.Cfg.Env, "api_url": s.Cfg.PublicAPIURL},
	})
}

type mfaRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// All security changes require fresh password knowledge, a shared durable MFA
// failure budget, and rate limiting independent of the general console bucket.
func (s *Server) mfaPassword(w http.ResponseWriter, r *http.Request, sess *session, password string) (string, error) {
	if !s.allowConsole(w, r, "mfa-user:"+sess.UserID.String(), 0.1, 5) {
		return "", errResponseWritten
	}
	if password == "" || len(password) > auth.MaxPasswordLen {
		return "", invalidCredentials()
	}
	var hash string
	var until *time.Time
	if err := s.Pool.QueryRow(r.Context(), `SELECT password_hash,mfa_locked_until FROM users WHERE id=$1`, sess.UserID).Scan(&hash, &until); err != nil {
		return "", err
	}
	ok, err := auth.CheckPasswordContext(r.Context(), hash, password)
	if err != nil {
		return "", passwordError(err)
	}
	if locked(until) {
		return "", invalidCredentials()
	}
	if !ok {
		if err := recordAuthFailure(r.Context(), s.Pool, sess.UserID, true); err != nil {
			return "", err
		}
		return "", invalidCredentials()
	}
	return hash, nil
}

var errResponseWritten = errors.New("response already written")

func (s *Server) mfaSetup(w http.ResponseWriter, r *http.Request, sess *session) {
	var req struct {
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	hash, err := s.mfaPassword(w, r, sess, req.Password)
	if err != nil {
		if !errors.Is(err, errResponseWritten) {
			s.fail(w, r, err)
		}
		return
	}
	secret := auth.NewTOTPSecret()
	enc, err := s.Sealer.Seal([]byte(secret), totpAAD(sess.UserID))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = db.Tx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		var currentHash string
		var enabled, until *time.Time
		if err := tx.QueryRow(r.Context(), `SELECT password_hash,totp_enabled_at,mfa_locked_until FROM users WHERE id=$1 FOR UPDATE`, sess.UserID).Scan(&currentHash, &enabled, &until); err != nil {
			return err
		}
		if currentHash != hash || locked(until) {
			return invalidCredentials()
		}
		if enabled != nil {
			return apierr.New(409, "validation_error", "Multi-factor authentication is already enabled.")
		}
		tag, err := tx.Exec(r.Context(), `UPDATE users SET totp_secret_enc=$2,totp_last_step=0 WHERE id=$1 AND EXISTS(SELECT 1 FROM sessions WHERE id=$3 AND user_id=$1 AND revoked_at IS NULL AND expires_at>now())`, sess.UserID, enc, sess.ID)
		if err == nil && tag.RowsAffected() != 1 {
			return invalidCredentials()
		}
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"secret": secret, "otpauth_url": auth.TOTPURL(secret, sess.Email)})
}

func (s *Server) changeMFA(w http.ResponseWriter, r *http.Request, sess *session, enable bool) {
	var req mfaRequest
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if !enable && sess.IsStaff {
		s.fail(w, r, apierr.New(403, "security_error", "Staff accounts must keep multi-factor authentication."))
		return
	}
	hash, err := s.mfaPassword(w, r, sess, req.Password)
	if err != nil {
		if !errors.Is(err, errResponseWritten) {
			s.fail(w, r, err)
		}
		return
	}
	var authErr error
	err = db.Tx(r.Context(), s.Pool, func(tx pgx.Tx) error {
		var currentHash string
		var enc []byte
		var last int64
		var enabled, until *time.Time
		var staff bool
		if err := tx.QueryRow(r.Context(), `SELECT password_hash,totp_secret_enc,totp_last_step,totp_enabled_at,mfa_locked_until,is_staff FROM users WHERE id=$1 FOR UPDATE`, sess.UserID).Scan(&currentHash, &enc, &last, &enabled, &until, &staff); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>now())`, sess.ID, sess.UserID).Scan(&active); err != nil {
			return err
		}
		if !active || currentHash != hash || locked(until) {
			authErr = invalidCredentials()
			return nil
		}
		if !enable && staff {
			return apierr.New(403, "security_error", "Staff accounts must keep multi-factor authentication.")
		}
		if enc == nil || (enable && enabled != nil) || (!enable && enabled == nil) {
			return apierr.New(409, "validation_error", "Account security changed. Refresh and try again.")
		}
		secret, err := s.Sealer.Open(enc, totpAAD(sess.UserID))
		if err != nil {
			return err
		}
		step, ok := auth.VerifyTOTP(string(secret), req.Code, time.Now(), last)
		if !ok {
			authErr = invalidCredentials()
			return recordAuthFailure(r.Context(), tx, sess.UserID, true)
		}
		if enable {
			_, err = tx.Exec(r.Context(), `UPDATE users SET totp_last_step=$2,totp_enabled_at=now() WHERE id=$1`, sess.UserID, step)
		} else {
			_, err = tx.Exec(r.Context(), `UPDATE users SET totp_last_step=$2,totp_enabled_at=NULL,totp_secret_enc=NULL WHERE id=$1`, sess.UserID, step)
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE users SET failed_mfa=0,mfa_locked_until=NULL,mfa_failure_window_started_at=NULL WHERE id=$1`, sess.UserID); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND id<>$2`, sess.UserID, sess.ID); err != nil {
			return err
		}
		if enable {
			_, err = tx.Exec(r.Context(), `UPDATE sessions SET mfa_verified_at=now() WHERE id=$1`, sess.ID)
		} else {
			_, err = tx.Exec(r.Context(), `UPDATE sessions SET mfa_verified_at=NULL WHERE id=$1`, sess.ID)
		}
		return err
	})
	if err == nil {
		err = authErr
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"mfa_enabled": enable})
}

func (s *Server) mfaEnable(w http.ResponseWriter, r *http.Request, sess *session) {
	s.changeMFA(w, r, sess, true)
}
func (s *Server) mfaDisable(w http.ResponseWriter, r *http.Request, sess *session) {
	s.changeMFA(w, r, sess, false)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, sess *session) {
	orgID, err := pathUUID(r, "org")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 {
		s.fail(w, r, apierr.New(422, "validation_error", "Project name must be 1-80 characters."))
		return
	}
	var pid uuid.UUID
	var key keys.Generated
	err = db.InUser(r.Context(), s.Pool, sess.UserID, &orgID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(r.Context(), `SELECT role FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, sess.UserID).Scan(&role); err != nil {
			if db.IsNoRows(err) {
				return apierr.ErrNotFound
			}
			return err
		}
		if role != "owner" && role != "admin" {
			return apierr.New(403, "restricted_api_key", "Only owners and admins can create projects.")
		}
		var err error
		if pid, err = NewProject(r.Context(), tx, orgID, name, s.Cfg.Env); err != nil {
			return err
		}
		_, key, err = CreateKey(r.Context(), tx, s.Hasher, orgID, pid, &sess.UserID, "Test key", "test", "full_access", nil)
		return err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": pid, "name": name, "test_key": map[string]any{"key": key.Plaintext, "hint": key.Hint}})
}
