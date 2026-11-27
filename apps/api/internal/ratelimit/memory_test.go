package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestMemoryBoundedUnderConcurrentUniqueKeyFlood(t *testing.T) {
	m := NewMemory()
	m.capacity = 128
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				m.Allow(context.Background(), fmt.Sprintf("%d:%d", worker, i), 1, 2)
			}
		}()
	}
	wg.Wait()
	if len(m.buckets) != 128 || m.order.Len() != 128 {
		t.Fatalf("unbounded buckets %d order %d", len(m.buckets), m.order.Len())
	}
	for range 2 {
		if !m.Allow(context.Background(), "active", 0.001, 2).Allowed {
			t.Fatal("initial tokens unavailable")
		}
	}
	if m.Allow(context.Background(), "active", 0.001, 2).Allowed {
		t.Fatal("existing bucket lost its rate limit")
	}
	for i := range 1000 {
		m.Allow(context.Background(), fmt.Sprint(i), 1, 1)
		m.Allow(context.Background(), "active", 0.001, 2)
	}
	if m.Allow(context.Background(), "active", 0.001, 2).Allowed {
		t.Fatal("active bucket evicted during churn")
	}
}
