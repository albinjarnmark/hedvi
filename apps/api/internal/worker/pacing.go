package worker

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A cheap early check avoids repeated S3 downloads and MIME construction for
// backlog that cannot yet use provider capacity. The attempt transaction still
// performs the authoritative locked check after preparing the payload.
func (s *Sender) deferBeforePayload(ctx context.Context, emailID uuid.UUID) (bool, error) {
	waiting := true
	err := s.finishKeepLease(ctx, emailID, func(tx pgx.Tx, e *emailInfo) error {
		var next *time.Time
		if err := tx.QueryRow(ctx, `SELECT (SELECT next_at FROM provider_send_pacing WHERE name=$1 AND next_at>clock_timestamp())`, s.ConfigSet).Scan(&next); err != nil {
			return err
		}
		if next == nil {
			waiting = false
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE emails SET send_state='retry_wait',next_attempt_at=LEAST($2,queue_deadline_at),attempt_count=greatest(attempt_count-1,0),lease_owner=NULL,lease_expires_at=NULL WHERE id=$1`, emailID, *next); err != nil {
			return err
		}
		return sendOutbox(ctx, tx, e.OrgID, emailID, *next)
	})
	return waiting, err
}

// providerSlot serializes the environment's customer-send capacity using the
// database clock. No network call or sleep holds the lock. A deferred message
// keeps its reservation and idempotency identity and creates no send attempt.
func (s *Sender) providerSlot(ctx context.Context, tx pgx.Tx, recipients int) (time.Time, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO provider_send_pacing(name,next_at) VALUES($1,clock_timestamp()) ON CONFLICT DO NOTHING`, s.ConfigSet); err != nil {
		return time.Time{}, err
	}
	var next, now time.Time
	if err := tx.QueryRow(ctx, `SELECT next_at,clock_timestamp() FROM provider_send_pacing WHERE name=$1 FOR UPDATE`, s.ConfigSet).Scan(&next, &now); err != nil {
		return time.Time{}, err
	}
	if next.After(now) {
		return next, nil
	}
	_, err := tx.Exec(ctx, `UPDATE provider_send_pacing SET next_at=clock_timestamp()+$2*interval '1 second' WHERE name=$1`, s.ConfigSet, float64(recipients)/float64(s.ProviderRecipientsPerSecond))
	return time.Time{}, err
}
