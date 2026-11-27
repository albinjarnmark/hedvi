package console

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/legal"
	"net/http"
	"net/netip"
	"strings"
)

func (s *Server) authMail() *authmail.Service {
	return &authmail.Service{Pool: s.Pool, Hasher: s.Hasher, Sealer: s.Sealer}
}
func linkError(err error) error {
	if errors.Is(err, authmail.ErrLink) {
		return apierr.New(400, "invalid_link", "%s", err)
	}
	return err
}
func (s *Server) legalIP(r *http.Request) any {
	if ip := s.clientIP(r); ip != nil {
		return ip.String()
	}
	return nil
}
func (s *Server) signup(w http.ResponseWriter, r *http.Request) { s.requestAuthMail(w, r, "signup") }
func (s *Server) requestReset(w http.ResponseWriter, r *http.Request) {
	s.requestAuthMail(w, r, "reset")
}
func (s *Server) requestAuthMail(w http.ResponseWriter, r *http.Request, purpose string) {
	if !s.authIngress(w, r, "request") {
		return
	}
	var req struct {
		Email  string `json:"email"`
		Locale string `json:"locale"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := address.Parse(req.Email)
	if err != nil || a.Name != "" {
		s.fail(w, r, apierr.New(422, "validation_error", "Enter a valid email address."))
		return
	}
	if purpose == "signup" && s.Cfg.IsDeployed() && !legal.Current().Adopted {
		s.fail(w, r, apierr.New(503, "temporarily_unavailable", "Account registration is not available yet."))
		return
	}
	if err := s.authMail().Request(r.Context(), purpose, a.Normalized, req.Locale, s.ipString(r)); err != nil {
		// Never log bound email/token values if a transaction fails.
		s.Log.Error("authentication mail admission failed")
		apierr.Write(w, apierr.New(503, "temporarily_unavailable", "Please try again shortly."))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 202, map[string]any{"accepted": true})
}
func (s *Server) completeReset(w http.ResponseWriter, r *http.Request) {
	if !s.authIngress(w, r, "complete") {
		return
	}
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	release, ok := s.authCompleteAdmission(w, r)
	if !ok {
		return
	}
	defer release()
	if _, err := s.authMail().Read(r.Context(), s.Pool, req.Token, "reset", false); err != nil {
		s.fail(w, r, linkError(err))
		return
	}
	if err := auth.ValidatePassword(req.Password); err != nil {
		s.fail(w, r, apierr.New(422, "validation_error", "%s", err))
		return
	}
	hash, err := auth.HashPasswordContext(r.Context(), req.Password)
	if err != nil {
		s.fail(w, r, passwordError(err))
		return
	}
	err = s.accountTx(r.Context(), uuid.Nil, nil, func(tx pgx.Tx) error {
		if err := authmail.Lock(r.Context(), tx); err != nil {
			return err
		}
		c, err := s.authMail().Read(r.Context(), tx, req.Token, "reset", true)
		if err != nil {
			return err
		}
		if c.UserID == nil || c.Version == nil {
			return authmail.ErrLink
		}
		var current int64
		err = tx.QueryRow(r.Context(), `SELECT credential_version FROM users WHERE id=$1 AND email=$2 FOR UPDATE`, c.UserID, c.Email).Scan(&current)
		if db.IsNoRows(err) {
			return authmail.ErrLink
		}
		if err != nil {
			return err
		}
		if current != *c.Version {
			return authmail.ErrLink
		}
		if _, err = tx.Exec(r.Context(), `UPDATE users SET password_hash=$2,email_verified_at=coalesce(email_verified_at,now()),credential_version=credential_version+1,failed_logins=0,locked_until=NULL,login_failure_window_started_at=NULL WHERE id=$1`, c.UserID, hash); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, c.UserID); err != nil {
			return err
		}
		return authmail.Consume(r.Context(), tx, c)
	})
	if err != nil {
		s.fail(w, r, accountError(err))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) legalManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, legal.ForOrigin(s.Cfg.LegalURLOrigin()))
}
func (s *Server) acceptLegal(w http.ResponseWriter, r *http.Request, sess *session) {
	var req legal.Consent
	if err := decode(r, &req); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := legal.Validate(req, s.Cfg.IsDeployed()); err != nil {
		s.fail(w, r, apierr.New(422, "legal_acknowledgement_required", "%s", err))
		return
	}
	err := db.InUser(r.Context(), s.Pool, sess.UserID, nil, func(tx pgx.Tx) error {
		var valid bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM sessions WHERE id=$1 AND revoked_at IS NULL AND expires_at>now())`, sess.ID).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return invalidCredentials()
		}
		return legal.Record(r.Context(), tx, sess.UserID, req, s.Cfg.IsDeployed(), s.legalIP(r), r.UserAgent(), s.Cfg.LegalURLOrigin())
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func boundedAgent(ua string) string {
	r := []rune(strings.ToValidUTF8(ua, ""))
	if len(r) > 300 {
		r = r[:300]
	}
	return string(r)
}
func (s *Server) authIngress(w http.ResponseWriter, r *http.Request, kind string) bool {
	ip := s.ipString(r)
	if a, err := netip.ParseAddr(ip); err == nil && a.Is6() {
		ip = netip.PrefixFrom(a, 64).Masked().String()
	}
	if !s.allowConsole(w, r, "auth-"+kind+":"+hex.EncodeToString(s.Hasher.Sum("auth-ingress", ip)), 0.1, 5) {
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	return true
}

func (s *Server) authCompleteAdmission(w http.ResponseWriter, r *http.Request) (func(), bool) {
	ctx, release, ok := authmail.Admit(r.Context())
	if !ok {
		apierr.Write(w, apierr.New(503, "temporarily_unavailable", "Account security capacity is busy. Try again shortly."))
		return release, false
	}
	*r = *r.WithContext(ctx)
	return release, true
}

func (s *Server) accountTx(ctx context.Context, user uuid.UUID, org *uuid.UUID, fn func(pgx.Tx) error) error {
	begin := s.AuthBegin
	if begin == nil {
		begin = s.Pool
	}
	return pgx.BeginFunc(ctx, begin, func(tx pgx.Tx) error {
		uid, oid := "", ""
		if user != uuid.Nil {
			uid = user.String()
		}
		if org != nil {
			oid = org.String()
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('hedvi.user_id',$1,true),set_config('hedvi.org_id',$2,true)`, uid, oid); err != nil {
			return err
		}
		return fn(tx)
	})
}

func accountError(err error) error {
	if errors.Is(err, authmail.ErrLink) {
		return linkError(err)
	}
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}
	return apierr.New(503, "account_change_unconfirmed", "The account change could not be confirmed. Try signing in with your chosen password. If that does not work, request a new email link.")
}
