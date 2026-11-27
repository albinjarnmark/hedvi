package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/queue"
)

// Dispatcher publishes due outbox jobs. Rows stay locked while publishing; if
// the process dies after publishing but before commit, the job is published
// again later. Consumers tolerate the duplicate.
type Dispatcher struct {
	Pool   *pgxpool.Pool
	Queues queue.Set
	Faults *faults.Injector
	Log    *slog.Logger
	Batch  int
}

func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	batch := d.Batch
	if batch == 0 {
		batch = 100
	}
	published := 0
	err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, topic, message, publish_attempts FROM outbox
			WHERE published_at IS NULL AND available_at <= now()
			ORDER BY available_at, id LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
		if err != nil {
			return err
		}
		type job struct {
			id    int64
			topic string
			msg   json.RawMessage
			tries int
		}
		var jobs []job
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.id, &j.topic, &j.msg, &j.tries); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, j)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, j := range jobs {
			q := d.Queues.Send
			if j.topic == "webhook" {
				q = d.Queues.Webhook
			}
			if err := q.Send(ctx, j.msg, 0); err != nil {
				backoff := time.Duration(min(300, 1<<min(j.tries+1, 9))) * time.Second
				if _, err2 := tx.Exec(ctx, `UPDATE outbox SET publish_attempts = publish_attempts + 1, last_error = $2,
					available_at = now() + $3 * interval '1 second' WHERE id = $1`, j.id, err.Error(), backoff.Seconds()); err2 != nil {
					return err2
				}
				d.Log.Warn("outbox publish failed", "outbox_id", j.id, "topic", j.topic, "err", err)
				continue
			}
			if err := d.Faults.Check(faults.DispatchAfterPublish); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now(), publish_attempts = publish_attempts + 1 WHERE id = $1`, j.id); err != nil {
				return err
			}
			metrics.OutboxPublished.WithLabelValues(j.topic).Inc()
			published++
		}
		return nil
	})
	return published, err
}

func (d *Dispatcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		n, err := d.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			d.Log.Error("dispatcher", "err", err)
		}
		if n == 0 || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
	}
}
