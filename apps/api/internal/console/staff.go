package console

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/db"
	"strings"
)

// GrantStaff is an operator-only bootstrap operation. The operator must verify
// the human and their exact account ID through an established separate channel;
// a claimed signup email is not proof. Every grant revokes existing sessions.
func GrantStaff(ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID, email, note string, identityVerified bool) error {
	if !identityVerified || userID == uuid.Nil || strings.TrimSpace(email) == "" || len(strings.TrimSpace(note)) < 20 {
		return errors.New("staff grant requires a verified exact user ID, matching email, and an audit note of at least 20 characters describing the independent identity check")
	}
	return db.Tx(ctx, pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET is_staff = true WHERE id = $1 AND email = $2 AND totp_enabled_at IS NOT NULL AND email_verified_at IS NOT NULL`, userID, strings.ToLower(strings.TrimSpace(email)))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("no matching account with MFA enabled; no privilege granted")
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE user_id = $1`, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_kind, action, target_type, target_id, metadata) VALUES ('staff','user.staff_granted','user',$1,$2)`, userID.String(), map[string]any{"identity_check": strings.TrimSpace(note)})
		return err
	})
}
