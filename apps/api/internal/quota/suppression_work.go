package quota

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const SuppressionWorkLimit = 10_000

var ErrSuppressionWork = errors.New("suppression processing budget exceeded")

// RecordSuppressionWork is not billing. It bounds recipient events generated
// for addresses to which nothing is sent. Workers finish late suppressions and
// release their credit, while admission refuses further work at the ceiling.
func RecordSuppressionWork(ctx context.Context, tx pgx.Tx, org uuid.UUID, count int, at time.Time, admission bool) error {
	var used int64
	err := tx.QueryRow(ctx, `INSERT INTO suppression_work_counters(org_id,window_start,count) VALUES($1,$2,$3)
	 ON CONFLICT(org_id,window_start) DO UPDATE SET count=suppression_work_counters.count+EXCLUDED.count
	 WHERE NOT $4 OR (suppression_work_counters.count<$5 AND suppression_work_counters.count+EXCLUDED.count<=$5)
	 RETURNING count`, org, at.UTC().Truncate(24*time.Hour), count, admission, SuppressionWorkLimit).Scan(&used)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && admission && used > SuppressionWorkLimit) {
		return ErrSuppressionWork
	}
	return err
}
