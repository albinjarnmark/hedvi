package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/metrics"
	"hedvi.dev/api/internal/netsafe"
	"hedvi.dev/api/internal/queue"
	"hedvi.dev/api/internal/secure"
)

// RetrySchedule follows the Standard Webhooks convention: immediately, then
// after 5 s, 5 min, 30 min, 2 h, 5 h, 10 h and 10 h. After the last attempt
// the delivery is failed (our dead-letter state) and can be resent manually.
var RetrySchedule = []time.Duration{5 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 10 * time.Hour}

// MaxStoredResponse limits how much of a receiver's response is kept.
const MaxStoredResponse = 1024

type Deliverer struct {
	Pool   *pgxpool.Pool
	Queue  queue.Queue
	Client *http.Client
	Sealer *secure.Sealer
	Faults *faults.Injector
	Log    *slog.Logger
	Now    func() time.Time
}

func SecretAAD(webhookID uuid.UUID) []byte { return []byte("webhook-secret:" + webhookID.String()) }

func (d *Deliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Deliverer) Run(ctx context.Context) {
	queue.Consume(ctx, d.Queue, d.Log, 4, func(ctx context.Context, m queue.Message) error {
		var job struct {
			DeliveryID string `json:"delivery_id"`
		}
		if err := json.Unmarshal(m.Body, &job); err != nil {
			return nil
		}
		id, err := uuid.Parse(job.DeliveryID)
		if err != nil {
			return nil
		}
		return d.Handle(ctx, id)
	})
}

type claimedDelivery struct {
	orgID, webhookID, leaseToken uuid.UUID
	eventID                      string
	attempt                      int
	url                          string
	secretEnc                    []byte
	status                       string
	payload                      []byte
}

// Handle attempts one delivery. A nil error means the job can be dropped.
func (d *Deliverer) Handle(ctx context.Context, deliveryID uuid.UUID) error {
	var c claimedDelivery
	c.leaseToken = uuid.New()
	deferred := false
	err := db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT org_id,webhook_id FROM webhook_deliveries WHERE id=$1 AND
			((state='pending' AND next_attempt_at<=now()+interval '2 seconds') OR (state='delivering' AND lease_expires_at<now()))`, deliveryID).Scan(&c.orgID, &c.webhookID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,615))`, c.orgID.String()); err != nil {
			return err
		}
		var full bool
		if err := tx.QueryRow(ctx, `SELECT count(*)>=2 OR count(*) FILTER (WHERE webhook_id=$2)>=1 FROM webhook_deliveries
			WHERE org_id=$1 AND state='delivering' AND lease_expires_at>now()`, c.orgID, c.webhookID).Scan(&full); err != nil {
			return err
		}
		if full {
			var next time.Time
			if err := tx.QueryRow(ctx, `UPDATE webhook_deliveries SET state='pending',next_attempt_at=now()+interval '5 seconds',lease_token=NULL,lease_expires_at=NULL
				WHERE id=$1 AND ((state='pending' AND next_attempt_at<=now()+interval '2 seconds') OR (state='delivering' AND lease_expires_at<now()))
				RETURNING next_attempt_at`, deliveryID).Scan(&next); err != nil {
				return err
			}
			deferred = true
			return EnqueueDelivery(ctx, tx, c.orgID, deliveryID, next)
		}
		return tx.QueryRow(ctx, `UPDATE webhook_deliveries wd SET state = 'delivering', attempt_count = wd.attempt_count + 1,
				last_attempt_at = now(), lease_token=$2, lease_expires_at = now() + interval '1 minute'
			FROM webhooks w, webhook_events ev
			WHERE wd.id = $1 AND w.id = wd.webhook_id AND ev.id = wd.event_id
			  AND ((wd.state = 'pending' AND wd.next_attempt_at <= now() + interval '2 seconds')
			       OR (wd.state = 'delivering' AND wd.lease_expires_at < now()))
			RETURNING wd.org_id, wd.webhook_id, wd.event_id, wd.attempt_count, w.url, w.secret_enc, w.status, ev.payload::text`,
			deliveryID, c.leaseToken).Scan(&c.orgID, &c.webhookID, &c.eventID, &c.attempt, &c.url, &c.secretEnc, &c.status, &c.payload)
	})
	if db.IsNoRows(err) || (deferred && err == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.status != "enabled" {
		return d.record(ctx, deliveryID, c, 0, "", "endpoint disabled", true)
	}
	secret, err := d.Sealer.Open(c.secretEnc, SecretAAD(c.webhookID))
	if err != nil {
		return d.record(ctx, deliveryID, c, 0, "", "signing secret unavailable", true)
	}
	status, body, sendErr := d.post(ctx, c, string(secret))
	if err := d.Faults.Check(faults.WebhookAfterHTTP); err != nil {
		return err
	}
	if sendErr != nil {
		metrics.WebhookDeliveries.WithLabelValues("error").Inc()
		return d.record(context.WithoutCancel(ctx), deliveryID, c, 0, "", sendErr.Error(), false)
	}
	ok := status >= 200 && status < 300
	metrics.WebhookDeliveries.WithLabelValues(map[bool]string{true: "success", false: "http_error"}[ok]).Inc()
	errMsg := ""
	if !ok {
		errMsg = fmt.Sprintf("receiver responded with HTTP %d", status)
	}
	return d.record(context.WithoutCancel(ctx), deliveryID, c, status, body, errMsg, ok)
}

func (d *Deliverer) post(ctx context.Context, c claimedDelivery, secret string) (int, string, error) {
	now := d.now()
	sig, err := Sign(secret, c.eventID, now, c.payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(c.payload))
	if err != nil {
		return 0, "", errors.New("invalid destination URL")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hedvi-Webhooks/1 (+https://hedvi.dev/docs/webhooks)")
	// Standard Webhooks headers (verified by Resend's SDK) plus the svix-*
	// aliases for receivers that use the Svix library directly.
	for _, prefix := range []string{"webhook-", "svix-"} {
		req.Header.Set(prefix+"id", c.eventID)
		req.Header.Set(prefix+"timestamp", ts)
		req.Header.Set(prefix+"signature", sig)
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return 0, "", redactErr(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, MaxStoredResponse))
	return resp.StatusCode, sanitize(b), nil
}

func redactErr(err error) error {
	// Never persist an underlying error: it can include the full URL,
	// internal resolver addresses, or the destination's resolved IP.
	var dnsErr *net.DNSError
	var timeout net.Error
	switch {
	case errors.Is(err, netsafe.ErrBlocked):
		return errors.New("destination address is not allowed")
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()):
		return errors.New("connection timed out")
	case errors.As(err, &dnsErr):
		return errors.New("destination DNS lookup failed")
	default:
		return errors.New("request failed")
	}
}

func sanitize(b []byte) string {
	s := string(b)
	if !utf8.ValidString(s) {
		return "[binary response omitted]"
	}
	return strings.ReplaceAll(s, "\x00", "")
}

func (d *Deliverer) record(ctx context.Context, deliveryID uuid.UUID, c claimedDelivery, status int, body, errMsg string, final bool) error {
	return db.Tx(ctx, d.Pool, func(tx pgx.Tx) error {
		var statusPtr *int
		if status != 0 {
			statusPtr = &status
		}
		success := final && errMsg == ""
		if success {
			_, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET state = 'succeeded', last_status = $2, last_response = $3, last_error = '',
				lease_expires_at = NULL,lease_token=NULL WHERE id = $1 AND lease_token=$4`, deliveryID, statusPtr, body, c.leaseToken)
			return err
		}
		if final || c.attempt > len(RetrySchedule) {
			_, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET state = 'failed', last_status = $2, last_response = $3, last_error = $4,
				lease_expires_at = NULL,lease_token=NULL WHERE id = $1 AND lease_token=$5`, deliveryID, statusPtr, body, errMsg, c.leaseToken)
			return err
		}
		next := d.now().Add(RetrySchedule[c.attempt-1])
		tag, err := tx.Exec(ctx, `UPDATE webhook_deliveries SET state = 'pending', next_attempt_at = $2, last_status = $3,
			last_response = $4, last_error = $5, lease_expires_at = NULL,lease_token=NULL WHERE id = $1 AND lease_token=$6`, deliveryID, next, statusPtr, body, errMsg, c.leaseToken)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return EnqueueDelivery(ctx, tx, c.orgID, deliveryID, next)
	})
}
