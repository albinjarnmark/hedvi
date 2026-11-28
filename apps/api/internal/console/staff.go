package console

import (
	"context"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/secure"
)

// OwnerFactor is accepted exclusively on the trusted infrastructure CLI's stdin.
// Never log or persist this structure or return it through an HTTP route.
type OwnerFactor struct {
	Environment string    `json:"environment"`
	UserID      uuid.UUID `json:"user_id"`
	Email       string    `json:"email"`
	Secret      string    `json:"secret"`
	Code        string    `json:"code"`
}

func ownerFactorAAD(id uuid.UUID) []byte { return []byte("owner-console-totp:v1:" + id.String()) }

// GrantStaff binds independently verified identity to two distinct factors.
// The worker-only secret and a successfully consumed first step commit together.
func GrantStaff(ctx context.Context, pool *pgxpool.Pool, sealer *secure.Sealer, environment string, userID uuid.UUID, email, note string, identityVerified bool, f OwnerFactor) error {
	email = strings.ToLower(strings.TrimSpace(email))
	note = strings.TrimSpace(note)
	if !identityVerified || userID == uuid.Nil || email == "" || len(note) < 20 || len(note) > 500 {
		return errors.New("staff grant requires an independently verified exact account, matching email and a 20–500 character audit note")
	}
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	decoded, err := encoding.DecodeString(f.Secret)
	if sealer == nil || f.UserID != userID || f.Environment != environment || f.Email != email || len(decoded) != 20 || err != nil || encoding.EncodeToString(decoded) != f.Secret {
		return errors.New("owner enrollment does not match the verified account and environment, or has an invalid key")
	}
	return db.Tx(ctx, pool, func(tx pgx.Tx) error {
		var normal []byte
		err := tx.QueryRow(ctx, `SELECT totp_secret_enc FROM users WHERE id=$1 AND email=$2 AND email_verified_at IS NOT NULL AND totp_enabled_at IS NOT NULL AND totp_secret_enc IS NOT NULL FOR UPDATE`, userID, email).Scan(&normal)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("no matching account with verified email and active MFA")
		}
		if err != nil {
			return err
		}
		signInSecret, err := sealer.Open(normal, totpAAD(userID))
		if err != nil {
			return errors.New("could not verify the sign-in factor")
		}
		if subtle.ConstantTimeCompare([]byte(strings.ToUpper(strings.TrimSpace(string(signInSecret)))), []byte(f.Secret)) == 1 {
			return errors.New("generate a separate operator factor; the sign-in factor cannot be reused")
		}
		var previous []byte
		var last int64
		err = tx.QueryRow(ctx, `SELECT owner_secret_enc,last_step FROM instance_owner WHERE user_id=$1 FOR UPDATE`, userID).Scan(&previous, &last)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if previous != nil {
			previousSecret, err := sealer.Open(previous, ownerFactorAAD(userID))
			if err != nil {
				return errors.New("could not verify previous owner enrollment")
			}
			if subtle.ConstantTimeCompare(previousSecret, []byte(f.Secret)) != 1 {
				last = 0
			}
		}
		step, valid := auth.VerifyTOTP(f.Secret, f.Code, time.Now(), last)
		if !valid {
			return errors.New("verify a fresh code from the separate owner authenticator before activation")
		}
		encrypted, err := sealer.Seal([]byte(f.Secret), ownerFactorAAD(userID))
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE users SET is_staff=true WHERE id=$1`, userID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO instance_owner(singleton,user_id,email,enrollment_hash,owner_secret_enc,last_step)
    VALUES(true,$1,$2,$3,$4,$5) ON CONFLICT(singleton) DO UPDATE SET
    email=excluded.email,enrollment_hash=excluded.enrollment_hash,owner_secret_enc=excluded.owner_secret_enc,last_step=excluded.last_step,
    failures=0,window_at=NULL,locked_until=NULL WHERE instance_owner.user_id=excluded.user_id`, userID, email, secure.SHA256(normal), encrypted, step)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("another instance owner is already bound")
		}
		if _, err = tx.Exec(ctx, `DELETE FROM owner_sessions WHERE user_id=$1`, userID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_kind,action,target_type,target_id,metadata) VALUES('staff','user.staff_granted','user',$1,$2)`, userID.String(), map[string]string{"identity_check": note})
		return err
	})
}

// RevokeStaff requires infrastructure credentials. The public console cannot appoint an owner.
func RevokeStaff(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, note string) error {
	if id == uuid.Nil || len(strings.TrimSpace(note)) < 20 || len(note) > 500 {
		return errors.New("provide the exact owner UUID and a 20–500 character reason")
	}
	return db.Tx(ctx, pool, func(tx pgx.Tx) error {
		t, err := tx.Exec(ctx, `UPDATE users SET is_staff=false WHERE id=$1 AND is_staff`, id)
		if err != nil {
			return err
		}
		if t.RowsAffected() != 1 {
			return errors.New("owner not found")
		}
		if _, err = tx.Exec(ctx, `DELETE FROM instance_owner WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_kind,action,target_type,target_id,metadata) VALUES('staff','user.staff_revoked','user',$1,$2)`, id.String(), map[string]string{"reason": strings.TrimSpace(note)})
		return err
	})
}
