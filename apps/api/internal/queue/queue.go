// Package queue moves jobs between the outbox and workers. Delivery is at
// least once: consumers must tolerate duplicates and missing messages are
// recovered from PostgreSQL, which remains the source of truth.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID           string
	Body         []byte
	Receipt      string
	ReceiveCount int
}

type Queue interface {
	Name() string
	Send(ctx context.Context, body []byte, delay time.Duration) error
	Receive(ctx context.Context, max int, wait time.Duration) ([]Message, error)
	Delete(ctx context.Context, receipt string) error
}

// Set groups the queues used by Hedvi.
type Set struct {
	Send    Queue // outbox -> send worker
	Events  Queue // SES events (SNS -> SQS) -> event worker
	Webhook Queue // outbox -> webhook worker
}

// MaxDelay is the SQS DelaySeconds limit. Longer delays use outbox available_at.
const MaxDelay = 15 * time.Minute

// ---------------------------------------------------------------------------
// SQS
// ---------------------------------------------------------------------------

type SQS struct {
	Client *sqs.Client
	URL    string
	Label  string
}

func (q SQS) Name() string { return q.Label }

func (q SQS) Send(ctx context.Context, body []byte, delay time.Duration) error {
	delay = min(max(delay, 0), MaxDelay)
	_, err := q.Client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:     aws.String(q.URL),
		MessageBody:  aws.String(string(body)),
		DelaySeconds: int32(delay / time.Second),
	})
	return err
}

func (q SQS) Receive(ctx context.Context, max int, wait time.Duration) ([]Message, error) {
	if max > 10 {
		max = 10
	}
	out, err := q.Client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(q.URL),
		MaxNumberOfMessages:         int32(max),
		WaitTimeSeconds:             int32(wait / time.Second),
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(out.Messages))
	for _, m := range out.Messages {
		count, _ := strconv.Atoi(m.Attributes[string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount)])
		msgs = append(msgs, Message{ID: aws.ToString(m.MessageId), Body: []byte(aws.ToString(m.Body)),
			Receipt: aws.ToString(m.ReceiptHandle), ReceiveCount: count})
	}
	return msgs, nil
}

func (q SQS) Delete(ctx context.Context, receipt string) error {
	_, err := q.Client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(q.URL), ReceiptHandle: aws.String(receipt)})
	return err
}

// ---------------------------------------------------------------------------
// Local mock with SQS semantics: visibility timeout, receive count, DLQ and
// duplicate delivery after timeout. Lives in the localmock schema, which is
// only created for local development and tests.
// ---------------------------------------------------------------------------

type Local struct {
	Pool        *pgxpool.Pool
	QueueName   string
	Visibility  time.Duration
	MaxReceives int
}

const localSchema = `
CREATE TABLE IF NOT EXISTS localmock.queue_messages (
  id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  queue         text NOT NULL,
  body          bytea NOT NULL,
  visible_at    timestamptz NOT NULL DEFAULT now(),
  receive_count int NOT NULL DEFAULT 0,
  receipt       text,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS queue_messages_visible_idx ON localmock.queue_messages (queue, visible_at);`

// EnsureLocalSchema creates the mock queue table. It fails outside local
// environments because the localmock schema does not exist there.
func EnsureLocalSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, localSchema)
	if err != nil {
		return fmt.Errorf("local queue mock (is the localmock schema present?): %w", err)
	}
	return nil
}

func NewLocalSet(pool *pgxpool.Pool) Set {
	mk := func(name string) Queue {
		return &Local{Pool: pool, QueueName: name, Visibility: 60 * time.Second, MaxReceives: 5}
	}
	return Set{Send: mk("send"), Events: mk("events"), Webhook: mk("webhook")}
}

func (q *Local) Name() string { return q.QueueName }

func (q *Local) Send(ctx context.Context, body []byte, delay time.Duration) error {
	delay = min(max(delay, 0), MaxDelay)
	_, err := q.Pool.Exec(ctx, `INSERT INTO localmock.queue_messages (queue, body, visible_at)
		VALUES ($1, $2, now() + $3 * interval '1 millisecond')`, q.QueueName, body, delay.Milliseconds())
	return err
}

func (q *Local) Receive(ctx context.Context, max int, wait time.Duration) ([]Message, error) {
	deadline := time.Now().Add(wait)
	for {
		msgs, err := q.receiveOnce(ctx, max)
		if err != nil || len(msgs) > 0 || time.Now().After(deadline) {
			return msgs, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (q *Local) receiveOnce(ctx context.Context, max int) ([]Message, error) {
	// Move poison messages to the DLQ first, like an SQS redrive policy.
	if _, err := q.Pool.Exec(ctx, `UPDATE localmock.queue_messages SET queue = $1 || '-dlq', receive_count = 0
		WHERE queue = $1 AND visible_at <= now() AND receive_count >= $2`, q.QueueName, q.MaxReceives); err != nil {
		return nil, err
	}
	rows, err := q.Pool.Query(ctx, `
		UPDATE localmock.queue_messages m
		SET visible_at = now() + $3 * interval '1 millisecond', receive_count = m.receive_count + 1,
		    receipt = m.id::text || ':' || gen_random_uuid()::text
		WHERE m.id IN (
			SELECT id FROM localmock.queue_messages
			WHERE queue = $1 AND visible_at <= now()
			ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING m.id::text, m.body, m.receipt, m.receive_count`, q.QueueName, max, q.Visibility.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.Body, &m.Receipt, &m.ReceiveCount); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

func (q *Local) Delete(ctx context.Context, receipt string) error {
	if receipt == "" {
		return errors.New("empty receipt")
	}
	// A stale receipt (message re-received elsewhere) does not delete, like SQS.
	_, err := q.Pool.Exec(ctx, `DELETE FROM localmock.queue_messages WHERE receipt = $1`, receipt)
	return err
}

// Depth reports visible and in-flight messages; used by tests and the dev UI.
func (q *Local) Depth(ctx context.Context) (n int, err error) {
	err = q.Pool.QueryRow(ctx, `SELECT count(*) FROM localmock.queue_messages WHERE queue = $1`, q.QueueName).Scan(&n)
	return n, err
}
