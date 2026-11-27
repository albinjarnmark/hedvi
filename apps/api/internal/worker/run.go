package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Runnable interface{ Run(context.Context) }

type Periodic struct {
	Name     string
	Interval time.Duration
	Fn       func(context.Context) error
}

// RunAll runs long-lived consumers and periodic jobs until ctx ends.
func RunAll(ctx context.Context, log *slog.Logger, consumers []Runnable, jobs []Periodic) {
	var wg sync.WaitGroup
	for _, c := range consumers {
		wg.Add(1)
		go func() { defer wg.Done(); c.Run(ctx) }()
	}
	for _, j := range jobs {
		wg.Add(1)
		go func() { defer wg.Done(); loop(ctx, log, j.Name, j.Interval, j.Fn) }()
	}
	wg.Wait()
}
