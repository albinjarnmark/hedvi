package worker

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

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
