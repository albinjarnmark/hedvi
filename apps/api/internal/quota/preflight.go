package quota

import (
	"context"
	"github.com/jackc/pgx/v5"

	"time"
)

// CheckCapacity rejects requests that cannot fit before their payload is
// uploaded. Reserve still makes the authoritative decision under row locks.
func CheckCapacity(ctx context.Context, tx pgx.Tx, org Org, recipients int, dataPerUnit int64, now time.Time) error {
	if recipients == 0 {
		return nil
	}
	per, err := ensurePeriod(ctx, tx, org, now, false)
	if err != nil {
		return err
	}
	units := per.Included - per.Consumed - per.Reserved
	available := capacity(units, per.DataIncluded-per.DataConsumed-per.DataReserved, dataPerUnit)
	if available >= int64(recipients) {
		return nil
	}
	if org.Plan == "flex" || org.UseCreditsForOverage {
		rows, err := tx.Query(ctx, `SELECT granted-reserved-consumed-expired_amount,data_granted-data_reserved-data_consumed FROM credit_lots
        WHERE org_id=$1 AND billing_usable AND expires_at>$2 AND expired_at IS NULL AND granted>reserved+consumed+expired_amount ORDER BY expires_at,id`, org.ID, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var qty, data int64
			if err := rows.Scan(&qty, &data); err != nil {
				return err
			}
			units += qty
			available += capacity(qty, data, dataPerUnit)
			if available >= int64(recipients) {
				return nil
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if units > available && dataPerUnit > 0 {
		return ErrDataExceeded
	}
	return ErrRecipientsExceeded
}
