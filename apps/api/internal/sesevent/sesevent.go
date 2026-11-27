// Package sesevent parses Amazon SES event publishing records delivered via
// SNS to SQS, with or without SNS raw message delivery.
package sesevent

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Recipient struct {
	EmailAddress   string `json:"emailAddress"`
	Action         string `json:"action,omitempty"`
	Status         string `json:"status,omitempty"`
	DiagnosticCode string `json:"diagnosticCode,omitempty"`
}

type Mail struct {
	Timestamp   time.Time           `json:"timestamp"`
	MessageID   string              `json:"messageId"`
	Source      string              `json:"source"`
	Destination []string            `json:"destination"`
	Tags        map[string][]string `json:"tags"`
}

type Bounce struct {
	BounceType        string      `json:"bounceType"`
	BounceSubType     string      `json:"bounceSubType"`
	BouncedRecipients []Recipient `json:"bouncedRecipients"`
	Timestamp         time.Time   `json:"timestamp"`
	FeedbackID        string      `json:"feedbackId"`
}

type Complaint struct {
	ComplainedRecipients  []Recipient `json:"complainedRecipients"`
	Timestamp             time.Time   `json:"timestamp"`
	FeedbackID            string      `json:"feedbackId"`
	ComplaintFeedbackType string      `json:"complaintFeedbackType,omitempty"`
}

type Delivery struct {
	Timestamp    time.Time `json:"timestamp"`
	Recipients   []string  `json:"recipients"`
	SMTPResponse string    `json:"smtpResponse"`
}

type DeliveryDelay struct {
	Timestamp         time.Time   `json:"timestamp"`
	DelayType         string      `json:"delayType"`
	DelayedRecipients []Recipient `json:"delayedRecipients"`
}

type Reject struct {
	Reason string `json:"reason"`
}

type Failure struct {
	ErrorMessage string `json:"errorMessage"`
}

// Event is one SES record. EventType holds eventType (event publishing) or
// notificationType (identity notifications).
type Event struct {
	EventType        string         `json:"eventType,omitempty"`
	NotificationType string         `json:"notificationType,omitempty"`
	Mail             Mail           `json:"mail"`
	Send             *struct{}      `json:"send,omitempty"`
	Delivery         *Delivery      `json:"delivery,omitempty"`
	Bounce           *Bounce        `json:"bounce,omitempty"`
	Complaint        *Complaint     `json:"complaint,omitempty"`
	DeliveryDelay    *DeliveryDelay `json:"deliveryDelay,omitempty"`
	Reject           *Reject        `json:"reject,omitempty"`
	Failure          *Failure       `json:"failure,omitempty"`
}

func (e Event) Type() string {
	if e.EventType != "" {
		return e.EventType
	}
	return e.NotificationType
}

// Tag returns a tag only when it has exactly one value.
func (e Event) Tag(name string) string {
	if v := e.Mail.Tags[name]; len(v) == 1 {
		return v[0]
	}
	return ""
}

type snsEnvelope struct {
	Type    string `json:"Type"`
	Message string `json:"Message"`
}

var ErrIgnored = errors.New("not an email event")

func Parse(body []byte) (Event, error) {
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Type != "" {
		switch env.Type {
		case "Notification":
			body = []byte(env.Message)
		default:
			// SubscriptionConfirmation etc. are handled by Terraform, not workers.
			return Event{}, fmt.Errorf("%w: SNS %s", ErrIgnored, env.Type)
		}
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil {
		return Event{}, fmt.Errorf("decode SES event: %w", err)
	}
	switch ev.Type() {
	case "Send", "Delivery", "Bounce", "Complaint", "DeliveryDelay", "Reject", "Rendering Failure", "RenderingFailure":
	case "":
		return Event{}, errors.New("SES event without type")
	default:
		return Event{}, fmt.Errorf("%w: %s", ErrIgnored, ev.Type())
	}
	if ev.Mail.MessageID == "" {
		return Event{}, errors.New("SES event without mail.messageId")
	}
	return ev, nil
}
