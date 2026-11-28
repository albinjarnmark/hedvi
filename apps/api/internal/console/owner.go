package console

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/secure"
)

const ownerLifetime = 30 * time.Minute

func (s *Server) ownerCookie() string {
	if s.Cfg.CookieSecure {
		return "__Host-hedvi_owner"
	}
	return "hedvi_owner"
}
func (s *Server) setOwnerCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: s.ownerCookie(), Value: value, Path: "/", HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
}
func ownerLocked() error {
	return apierr.New(403, "owner_unlock_required", "Use a fresh code from your separate Hedvi Owner authenticator.")
}
func ownerConflict(reason string) error {
	return apierr.New(409, "owner_action_conflict", "%s", reason)
}

type ownerHandler func(context.Context, pgx.Tx, *http.Request, *session) (any, error)

// This mux only exposes bounded owner operations. The deployed public API never mounts it.
func (s *Server) OwnerRoutes(mux *http.ServeMux) {
	wrap := func(method, path string, h sessionHandler) {
		handler := s.withSession(s.staffOnly(h))
		if method != "GET" {
			handler = s.guard(handler)
		}
		mux.HandleFunc(method+" /console/v1/admin/"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			handler(w, r.WithContext(ctx))
		})
	}
	wrap("POST", "unlock", s.ownerUnlock)
	route := func(method, path string, fn ownerHandler) {
		wrap(method, path, func(w http.ResponseWriter, r *http.Request, a *session) {
			var result any
			err := db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
				if _, err := s.requireBoundOwner(r, tx, a, true); err != nil {
					return err
				}
				var err error
				result, err = fn(r.Context(), tx, r, a)
				return err
			})
			if err != nil {
				s.fail(w, r, err)
				return
			}
			if path == "lock" {
				s.setOwnerCookie(w, "", -1)
			}
			writeJSON(w, 200, result)
		})
	}
	route("GET", "summary", s.ownerSummary)
	route("POST", "search", s.ownerSearch)
	route("POST", "lock", func(ctx context.Context, tx pgx.Tx, r *http.Request, a *session) (any, error) {
		var req struct{}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		_, err := tx.Exec(ctx, `DELETE FROM owner_sessions WHERE session_id=$1`, a.ID)
		return map[string]bool{"locked": true}, err
	})
	route("POST", "memberships", s.ownerGrant)
	route("POST", "memberships/{id}/revoke", s.ownerRevoke)
	route("POST", "projects/{project}/decision", s.ownerDecide)
	if !s.Cfg.IsDeployed() {
		route("GET", "alerts", ownerLegacyList("alerts"))
		route("GET", "unknown-attempts", ownerLegacyList("unknown"))
		route("GET", "reviews", ownerLegacyList("reviews"))
		route("POST", "projects/{project}/approve", s.ownerDecide)
		route("POST", "projects/{project}/pause", s.ownerDecide)
		route("POST", "alerts/{id}/resolve", s.ownerResolveAlert)
		route("POST", "attempts/{id}/resolve", s.ownerResolveAttempt)
	}
}

type ownerBinding struct {
	enc           []byte
	last, version int64
	lockedUntil   *time.Time
}

func (s *Server) requireBoundOwner(r *http.Request, tx pgx.Tx, a *session, unlocked bool) (ownerBinding, error) {
	var b ownerBinding
	ordinary, err := r.Cookie(s.cookieName())
	if err != nil {
		return b, apierr.ErrNotFound
	}
	ctx := r.Context()
	// Use the same user -> session order as password/MFA resets. Recheck inside
	// the transaction which reads private rows or commits a privileged mutation.
	err = tx.QueryRow(ctx, `SELECT o.owner_secret_enc,u.credential_version,o.last_step,o.locked_until
  FROM users u JOIN instance_owner o ON o.user_id=u.id AND o.email=u.email WHERE u.id=$1 AND u.is_staff
  AND u.email_verified_at IS NOT NULL AND u.totp_enabled_at IS NOT NULL AND o.enrollment_hash=sha256(u.totp_secret_enc) FOR UPDATE OF u,o`, a.UserID).Scan(&b.enc, &b.version, &b.last, &b.lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, apierr.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	var sid uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM sessions WHERE id=$1 AND user_id=$2 AND token_hash=$3 AND mfa_verified_at IS NOT NULL AND revoked_at IS NULL
 AND expires_at>clock_timestamp() AND last_seen_at>clock_timestamp()-(CASE WHEN remembered THEN $5::float8 ELSE $4::float8 END)*interval '1 second' FOR SHARE`, a.ID, a.UserID, s.Hasher.Sum(sessionPurpose, ordinary.Value), sessionIdle.Seconds(), rememberedTTL.Seconds()).Scan(&sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, apierr.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	if unlocked {
		c, err := r.Cookie(s.ownerCookie())
		if err != nil || len(c.Value) != 52 {
			return b, ownerLocked()
		}
		var ok bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM owner_sessions WHERE token_hash=$1 AND session_id=$2 AND user_id=$3
    AND credential_version=$4 AND expires_at>clock_timestamp())`, s.Hasher.Sum("owner-console", c.Value), a.ID, a.UserID, b.version).Scan(&ok)
		if err != nil {
			return b, err
		}
		if !ok {
			return b, ownerLocked()
		}
	}
	return b, nil
}
func (s *Server) ownerUnlock(w http.ResponseWriter, r *http.Request, a *session) {
	var req struct {
		Code string `json:"code"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(req.Code) != 6 {
		s.fail(w, r, ownerLocked())
		return
	}
	var token string
	err := db.Tx(r.Context(), s.StaffPool, func(tx pgx.Tx) error {
		b, err := s.requireBoundOwner(r, tx, a, false)
		if err != nil {
			return err
		}
		if b.lockedUntil != nil && b.lockedUntil.After(time.Now()) {
			return ownerLocked()
		}
		secret, err := s.Sealer.Open(b.enc, ownerFactorAAD(a.UserID))
		if err != nil {
			return err
		}
		step, ok := auth.VerifyTOTP(string(secret), req.Code, time.Now(), b.last)
		if !ok {
			_, err = tx.Exec(r.Context(), `UPDATE instance_owner SET failures=CASE WHEN window_at IS NULL OR window_at<=now()-interval '15 minutes' THEN 1 ELSE failures+1 END,
      window_at=CASE WHEN window_at IS NULL OR window_at<=now()-interval '15 minutes' THEN now() ELSE window_at END,
      locked_until=CASE WHEN window_at>now()-interval '15 minutes' AND failures>=4 THEN now()+interval '15 minutes' END WHERE user_id=$1`, a.UserID)
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE instance_owner SET last_step=$2,failures=0,window_at=NULL,locked_until=NULL WHERE user_id=$1`, a.UserID, step)
		if err != nil {
			return err
		}
		token = secure.RandomToken(32)
		// Only one active owner console session. Every new unlock invalidates the previous bearer.
		if _, err = tx.Exec(r.Context(), `DELETE FROM owner_sessions WHERE user_id=$1`, a.UserID); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO owner_sessions(token_hash,session_id,user_id,credential_version,expires_at)
    VALUES($1,$2,$3,$4,now()+interval '30 minutes')`, s.Hasher.Sum("owner-console", token), a.ID, a.UserID, b.version)
		if err != nil {
			return err
		}
		return s.staffAudit(tx, r, a, nil, "owner.unlocked", "user", a.UserID.String(), nil)
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if token == "" {
		s.fail(w, r, ownerLocked())
		return
	}
	s.setOwnerCookie(w, token, int(ownerLifetime.Seconds()))
	writeJSON(w, 200, map[string]bool{"unlocked": true})
}

func checkedReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if len(r) < 10 || len(r) > 500 || strings.IndexFunc(r, unicode.IsControl) >= 0 {
		return "", apierr.New(422, "validation_error", "Enter a single-line reason between 10 and 500 bytes.")
	}
	return r, nil
}
