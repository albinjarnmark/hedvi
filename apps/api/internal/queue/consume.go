package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Consume runs a bounded number of independent long polls. Taking a single
// message per free worker starts its visibility timeout just before processing.
func Consume(ctx context.Context, q Queue, log *slog.Logger, workers int, handle func(context.Context, Message) error) {
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				msgs, err := q.Receive(ctx, 1, 10*time.Second)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Error("receive job", "queue", q.Name(), "err", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
					continue
				}
				for _, m := range msgs {
					if err := handle(ctx, m); err != nil {
						log.Error("process job", "queue", q.Name(), "message_id", m.ID, "err", err)
						continue
					}
					if err := q.Delete(ctx, m.Receipt); err != nil {
						log.Warn("delete job", "queue", q.Name(), "message_id", m.ID, "err", err)
					}
				}
			}
		}()
	}
	wg.Wait()
}
