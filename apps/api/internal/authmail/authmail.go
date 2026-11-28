// Package authmail owns account-ownership proofs and system-mail delivery.
// It is separate from tenant messages, credits, captures and webhooks.
package authmail

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/secure"
	"net/netip"
	"strings"
	"time"
)

const TTL = 30 * time.Minute

var ErrLink = errors.New("this link is invalid, expired or already used; request a new link or sign in if you already completed it")

type Service struct {
	Begin  db.Beginner // optional transaction fault injection; defaults to Pool
	Pool   *pgxpool.Pool
	Hasher *secure.Hasher
	Sealer *secure.Sealer
}
type payload struct {
	Email  string `json:"email"`
	Token  string `json:"token"`
	Locale string `json:"locale"`
}
type Challenge struct {
	ID        uuid.UUID
	Purpose   string
	Locale    string
	EmailHash []byte
	Email     string
	UserID    *uuid.UUID
	Version   *int64
}

func payloadAAD(id uuid.UUID) []byte { return []byte("auth-mail:v1:" + id.String()) }
func emailAAD(id uuid.UUID) []byte   { return []byte("auth-email:v1:" + id.String()) }
func Lock(ctx context.Context, tx pgx.Tx) error {
	var key string
	return tx.QueryRow(ctx, `SELECT key FROM auth_mail_budgets WHERE key='global' FOR UPDATE`).Scan(&key)
}

// Charge updates a rolling budget. Callers hold the global coordination row.
func Charge(ctx context.Context, tx pgx.Tx, key string, hour, day int, cooldown time.Duration) (bool, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO auth_mail_budgets(key) VALUES($1) ON CONFLICT DO NOTHING`, key); err != nil {
		return false, err
	}
	var allowed bool
	err := tx.QueryRow(ctx, `UPDATE auth_mail_budgets SET
 hour_count=CASE WHEN hour_started<=now()-interval '1 hour' THEN 1 ELSE hour_count+1 END,
 hour_started=CASE WHEN hour_started<=now()-interval '1 hour' THEN now() ELSE hour_started END,
 day_count=CASE WHEN day_started<=now()-interval '24 hours' THEN 1 ELSE day_count+1 END,
 day_started=CASE WHEN day_started<=now()-interval '24 hours' THEN now() ELSE day_started END,last_at=now()
 WHERE key=$1 AND (hour_started<=now()-interval '1 hour' OR hour_count<$2)
 AND (day_started<=now()-interval '24 hours' OR day_count<$3)
 AND (last_at IS NULL OR last_at<=now()-$4*interval '1 second') RETURNING true`, key, hour, day, cooldown.Seconds()).Scan(&allowed)
	if db.IsNoRows(err) {
		return false, nil
	}
	return allowed, err
}

// Request does not query users. Existence and suppression never affect the
// observable request response. Durable budgets also bound stored ciphertext.
func (s *Service) Request(ctx context.Context, purpose, email, locale, ip string) error {
	ctx, release, ok := Admit(ctx)
	if !ok {
		return nil
	}
	defer release()
	if purpose != "signup" && purpose != "reset" {
		return ErrLink
	}
	// Accept legacy clients but always use the product language.
	locale = "en"
	if a, err := netip.ParseAddr(ip); err == nil && a.Is6() {
		ip = netip.PrefixFrom(a, 64).Masked().String()
	}
	eh := s.Hasher.Sum("auth-email", email)
	id := uuid.New()
	token := id.String() + "." + secure.RandomToken(32)
	enc, err := s.Sealer.Seal([]byte(email), emailAAD(id))
	if err != nil {
		return err
	}
	raw, err := json.Marshal(payload{Email: email, Token: token, Locale: locale})
	if err != nil {
		return err
	}
	sealed, err := s.Sealer.Seal(raw, payloadAAD(id))
	if err != nil {
		return err
	}
	// Returning this private sentinel rolls back all partial budget charges.
	limited := errors.New("mail budget exhausted")
	begin := s.Begin
	if begin == nil {
		begin = s.Pool
	}
	err = pgx.BeginFunc(ctx, begin, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT hedvi_auth_pending_count()`).Scan(&pending); err != nil {
			return err
		}
		if pending >= 1000 {
			return limited
		}
		for _, b := range []struct {
			k    string
			h, d int
			cd   time.Duration
		}{
			{"global", 100, 500, 0}, {"email:" + hex.EncodeToString(eh), 3, 5, time.Minute}, {"ip:" + hex.EncodeToString(s.Hasher.Sum("auth-ip", ip)), 10, 50, 0},
		} {
			ok, err := Charge(ctx, tx, b.k, b.h, b.d, b.cd)
			if err != nil {
				return err
			}
			if !ok {
				return limited
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO auth_challenges(id,purpose,token_hash,email_hash,email_enc,locale,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '30 minutes')`, id, purpose, s.Hasher.Sum("auth-token:"+purpose, token), eh, enc, locale); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_mail_outbox(id,payload_enc) VALUES($1,$2)`, id, sealed)
		return err
	})
	if errors.Is(err, limited) {
		return nil
	}
	return err
}

func (s *Service) Read(ctx context.Context, q db.Querier, token, purpose string, lock bool) (*Challenge, error) {
	if len(token) != 89 || token[36] != '.' {
		return nil, ErrLink
	}
	id, err := uuid.Parse(token[:36])
	if err != nil {
		return nil, ErrLink
	}
	c := &Challenge{ID: id}
	var digest, enc []byte
	sql := `SELECT purpose,token_hash,email_hash,email_enc,user_id,credential_version,locale FROM auth_challenges
 WHERE id=$1 AND purpose=$2 AND ready_at IS NOT NULL AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at>now()`
	if lock {
		sql += " FOR UPDATE"
	}
	err = q.QueryRow(ctx, sql, id, purpose).Scan(&c.Purpose, &digest, &c.EmailHash, &enc, &c.UserID, &c.Version, &c.Locale)
	if db.IsNoRows(err) {
		return nil, ErrLink
	}
	if err != nil {
		return nil, err
	}
	if !s.Hasher.Equal("auth-token:"+purpose, token, digest) {
		return nil, ErrLink
	}
	plain, err := s.Sealer.Open(enc, emailAAD(id))
	if err != nil {
		return nil, err
	}
	c.Email = string(plain)
	return c, nil
}
func Consume(ctx context.Context, tx pgx.Tx, c *Challenge) error {
	tag, err := tx.Exec(ctx, `UPDATE auth_challenges SET consumed_at=now(),email_enc=''::bytea,user_id=NULL,credential_version=NULL WHERE id=$1 AND consumed_at IS NULL AND invalidated_at IS NULL AND expires_at>now()`, c.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLink
	}
	_, err = tx.Exec(ctx, `UPDATE auth_challenges SET invalidated_at=now(),email_enc=''::bytea,user_id=NULL,credential_version=NULL WHERE email_hash=$1 AND id<>$2 AND consumed_at IS NULL AND invalidated_at IS NULL`, c.EmailHash, c.ID)
	return err
}

// RedactError deliberately never includes provider messages, recipients or raw
// database parameters in externally logged authentication delivery failures.
func SafeCode(raw string) string {
	if len(raw) > 80 {
		return "provider_error"
	}
	for _, c := range raw {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return "provider_error"
		}
	}
	return strings.TrimSpace(raw)
}

var admission = make(chan struct{}, 2)

// Admit bounds unauthenticated authentication work before it can occupy DB
// connections or queue behind a shared row lock. No unbounded waiting queue.
func Admit(parent context.Context) (context.Context, func(), bool) {
	select {
	case admission <- struct{}{}:
	default:
		return parent, func() {}, false
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	return ctx, func() { cancel(); <-admission }, true
}
