package worker

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// AlertPublisher must return success only after the independent channel has
// accepted the alert. A failed publish leaves notified_at unset for retry.
type AlertPublisher interface {
	PublishAlert(context.Context, string) error
}

type SNSPublishClient interface {
	Publish(context.Context, *sns.PublishInput, ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SNSAlertPublisher uses SNS rather than SES so sending suspension does not
// prevent operational notifications. Subscribe and confirm the ops mailbox.
type SNSAlertPublisher struct {
	Client   SNSPublishClient
	TopicARN string
}

func (p *SNSAlertPublisher) PublishAlert(ctx context.Context, body string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := p.Client.Publish(ctx, &sns.PublishInput{
		TopicArn: aws.String(p.TopicARN),
		Subject:  aws.String("Hedvi operations alert"),
		Message:  aws.String(body),
	})
	if err != nil {
		return err
	}
	if out == nil || aws.ToString(out.MessageId) == "" {
		return errors.New("SNS did not acknowledge the alert")
	}
	return nil
}
