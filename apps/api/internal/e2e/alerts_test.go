package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/google/uuid"

	"hedvi.dev/api/internal/testenv"
	"hedvi.dev/api/internal/worker"
)

type alertSNS struct {
	fail  bool
	calls []*sns.PublishInput
}

func (s *alertSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	s.calls = append(s.calls, in)
	if s.fail {
		return nil, errors.New("SNS unavailable")
	}
	return &sns.PublishOutput{MessageId: aws.String("accepted-alert")}, nil
}

func TestOperationalAlertRetriesUntilSNSAccepts(t *testing.T) {
	e := testenv.New(t)
	id := uuid.Must(uuid.NewV7())
	e.Exec(`INSERT INTO alerts (id, kind, severity, dedupe_key, message)
		VALUES ($1, 'delivery_unknown', 'critical', 'sns-retry', 'A send outcome needs investigation')`, id)
	client := &alertSNS{fail: true}
	const topic = "arn:aws:sns:eu-north-1:123456789012:hedvi-staging-ops-alarms"
	m := &worker.Monitor{Pool: e.WorkerPool, Log: slog.Default(),
		Publisher: &worker.SNSAlertPublisher{Client: client, TopicARN: topic}}
	if err := m.RunOnce(e.Ctx); err == nil {
		t.Fatal("failed SNS publish must be reported")
	}
	if n := e.Count(`SELECT count(*) FROM alerts WHERE id = $1 AND notified_at IS NOT NULL`, id); n != 0 {
		t.Fatal("a failed notification was marked delivered")
	}
	client.fail = false
	if err := m.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM alerts WHERE id = $1 AND notified_at IS NOT NULL`, id); n != 1 {
		t.Fatal("SNS acceptance was not recorded")
	}
	if err := m.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if len(client.calls) != 2 {
		t.Fatalf("want failed attempt + successful retry, got %d publishes", len(client.calls))
	}
	in := client.calls[1]
	var body map[string]any
	if err := json.Unmarshal([]byte(aws.ToString(in.Message)), &body); err != nil {
		t.Fatal(err)
	}
	if aws.ToString(in.TopicArn) != topic || body["id"] != id.String() || body["kind"] != "delivery_unknown" {
		t.Fatalf("wrong routing or correlation: topic %s, body %v", aws.ToString(in.TopicArn), body)
	}
}
