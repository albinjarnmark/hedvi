// Package faults provides named crash points for acceptance tests. In
// production the injector is nil and every check is a no-op.
package faults

import (
	"errors"
	"sync"
)

// ErrCrash simulates a process dying at a point: callers return immediately
// without cleanup, exactly as a crash would leave state.
var ErrCrash = errors.New("simulated crash")

type Injector struct {
	mu     sync.Mutex
	points map[string]int // remaining hits that crash; -1 = always
}

func New() *Injector { return &Injector{points: map[string]int{}} }

// Arm makes the next n checks of point crash (n < 0: every check).
func (i *Injector) Arm(point string, n int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.points[point] = n
}

func (i *Injector) Check(point string) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	n, ok := i.points[point]
	if !ok || n == 0 {
		return nil
	}
	if n > 0 {
		i.points[point] = n - 1
	}
	return ErrCrash
}

// Crash points.
const (
	AuthAfterClaim        = "auth_mail.after_claim"
	AuthAfterProvider     = "auth_mail.after_provider"
	AcceptAfterBlobPut    = "accept.after_blob_put"
	AcceptBeforeCommit    = "accept.before_commit"
	AcceptAfterCommit     = "accept.after_commit"
	DispatchAfterPublish  = "dispatch.after_publish"
	SendAfterClaim        = "send.after_claim"
	SendAfterAttemptStart = "send.after_attempt_start"
	SendAfterProviderCall = "send.after_provider_call"
	EventsBeforeCommit    = "events.before_commit"
	WebhookAfterHTTP      = "webhook.after_http"
)
