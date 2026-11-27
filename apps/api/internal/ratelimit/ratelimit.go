// Package ratelimit implements token buckets in Valkey with an in-process
// fallback. Rate limits protect the service; they are never the source of
// truth for quota, which lives in PostgreSQL.
package ratelimit

import (
	"container/list"
	"context"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

type Decision struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, ratePerSec float64, burst int) Decision
}

// Valkey token bucket. KEYS[1] bucket; ARGV: rate, burst, now_ms.
var bucketScript = valkey.NewLuaScript(`
local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local data = redis.call('HMGET', KEYS[1], 't', 'ts')
local tokens = tonumber(data[1]) or burst
local ts = tonumber(data[2]) or now
tokens = math.min(burst, tokens + (now - ts) / 1000 * rate)
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 't', tokens, 'ts', now)
redis.call('PEXPIRE', KEYS[1], math.ceil(burst / rate * 1000) + 1000)
return {allowed, math.floor(tokens), math.ceil((1 - math.min(tokens, 1)) / rate * 1000)}
`)

type Valkey struct {
	Client   valkey.Client
	Fallback *Memory
	Log      *slog.Logger
}

func NewValkey(url string, log *slog.Logger) (*Valkey, error) {
	opt, err := valkey.ParseURL(url)
	if err != nil {
		return nil, err
	}
	opt.DisableCache = true
	c, err := valkey.NewClient(opt)
	if err != nil {
		return nil, err
	}
	return &Valkey{Client: c, Fallback: NewMemory(), Log: log}, nil
}

func (v *Valkey) Allow(ctx context.Context, key string, rate float64, burst int) Decision {
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	res, err := bucketScript.Exec(ctx, v.Client, []string{"rl:" + key},
		[]string{strconv.FormatFloat(rate, 'f', -1, 64), strconv.Itoa(burst), strconv.FormatInt(time.Now().UnixMilli(), 10)}).AsIntSlice()
	if err != nil || len(res) != 3 {
		// Valkey unavailable: degrade to a per-process limiter rather than
		// failing requests. Quota and acceptance do not depend on Valkey.
		if v.Log != nil {
			v.Log.Warn("rate limiter fallback", "err", err)
		}
		return v.Fallback.Allow(ctx, key, rate, burst)
	}
	return Decision{Allowed: res[0] == 1, Limit: burst, Remaining: int(res[1]), Reset: time.Duration(res[2]) * time.Millisecond}
}

func (v *Valkey) Close() { v.Client.Close() }

// Memory is a per-process token bucket.
type Memory struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	order    list.List
	capacity int
}

type bucket struct {
	tokens  float64
	at      time.Time
	element *list.Element
}

func NewMemory() *Memory { return &Memory{buckets: map[string]*bucket{}, capacity: 100_000} }

func (m *Memory) Allow(_ context.Context, key string, rate float64, burst int) Decision {
	if rate <= 0 || burst <= 0 {
		return Decision{Reset: time.Second}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	b, ok := m.buckets[key]
	if !ok {
		// Bound storage and work per request even under unique-key floods.
		// Eviction never scans the map while holding the shared lock.
		if len(m.buckets) >= m.capacity {
			oldest := m.order.Back()
			delete(m.buckets, oldest.Value.(string))
			m.order.Remove(oldest)
		}
		b = &bucket{tokens: float64(burst), at: now}
		b.element = m.order.PushFront(key)
		m.buckets[key] = b
	} else {
		m.order.MoveToFront(b.element)
	}
	b.tokens = math.Min(float64(burst), b.tokens+now.Sub(b.at).Seconds()*rate)
	b.at = now
	d := Decision{Limit: burst}
	if b.tokens >= 1 {
		b.tokens--
		d.Allowed = true
	}
	d.Remaining = int(b.tokens)
	d.Reset = time.Duration((1 - math.Min(b.tokens, 1)) / rate * float64(time.Second))
	return d
}
