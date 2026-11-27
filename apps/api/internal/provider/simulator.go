package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/queue"
	"hedvi.dev/api/internal/sesevent"
)

// Simulator is the local SES stand-in. It stores what would have been sent in
// localmock.ses_outbound and publishes SES-format events to the events queue,
// so the real event pipeline is exercised.
//
// Recipient patterns (like the SES mailbox simulator):
//
//	*+bounce@…      permanent bounce
//	*+suppressed@…  bounce from the account suppression list
//	*+complaint@…   delivery followed by a complaint
//	*+delay@…       delivery delay followed by delivery
//	anything else   delivery
//
// Faults can be forced with HEDVI_MOCK_SES_FAULT, the X-Hedvi-Simulate
// header or the Fault hook: reject, throttle, unknown_before_accept,
// unknown_after_accept.
type Simulator struct {
	Pool   *pgxpool.Pool
	Events queue.Queue
	Fault  func(SendInput) string
	// EventDelay spaces outcome events after the Send event.
	EventDelay time.Duration
}

const simulatorSchema = `
CREATE TABLE IF NOT EXISTS localmock.ses_identities (domain text PRIMARY KEY, claim_id uuid NOT NULL);
-- Prior local simulator versions derived identities directly from these rows.
INSERT INTO localmock.ses_identities (domain, claim_id)
 SELECT name,id FROM domains WHERE provider_state = 'legacy' AND deleted_at IS NULL ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS localmock.ses_outbound (
  message_id  text PRIMARY KEY,
  from_addr   text NOT NULL,
  recipients  text[] NOT NULL,
  raw         bytea NOT NULL,
  tags        jsonb NOT NULL,
  tenant      text NOT NULL,
  config_set  text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);`

func EnsureSimulatorSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, simulatorSchema)
	return err
}

func (s *Simulator) fault(in SendInput) string {
	if s.Fault != nil {
		if f := s.Fault(in); f != "" {
			return f
		}
	}
	if msg, err := mail.ReadMessage(bytes.NewReader(in.Raw)); err == nil {
		return msg.Header.Get("X-Hedvi-Simulate")
	}
	return ""
}

func (s *Simulator) Send(ctx context.Context, in SendInput) (string, error) {
	switch s.fault(in) {
	case "reject":
		return "", &SendError{Outcome: Rejected, Code: "MessageRejected", Message: "simulated rejection"}
	case "throttle":
		return "", &SendError{Outcome: Retryable, Code: "TooManyRequestsException", Message: "simulated throttling"}
	case "unknown_before_accept":
		return "", &SendError{Outcome: Unknown, Code: "transport_error", Message: "simulated timeout before SES accepted"}
	case "unknown_after_accept":
		if _, err := s.accept(ctx, in); err != nil {
			return "", err
		}
		return "", &SendError{Outcome: Unknown, Code: "transport_error", Message: "simulated timeout after SES accepted"}
	}
	return s.accept(ctx, in)
}

func (s *Simulator) accept(ctx context.Context, in SendInput) (string, error) {
	messageID := "sim-" + uuid.NewString()
	tagJSON, _ := json.Marshal(in.Tags)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO localmock.ses_outbound
		(message_id, from_addr, recipients, raw, tags, tenant, config_set) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		messageID, in.From, in.Recipients, in.Raw, tagJSON, in.TenantName, in.ConfigurationSet); err != nil {
		return "", &SendError{Outcome: Retryable, Code: "simulator_store", Message: err.Error()}
	}
	now := time.Now().UTC()
	tags := map[string][]string{"ses:configuration-set": {in.ConfigurationSet}}
	for k, v := range in.Tags {
		tags[k] = []string{v}
	}
	m := sesevent.Mail{Timestamp: now, MessageID: messageID, Source: in.From, Destination: in.Recipients, Tags: tags}
	publish := func(ev sesevent.Event, delay time.Duration) {
		body, _ := json.Marshal(ev)
		// Publishing failures only lose simulated events, never accepted mail.
		_ = s.Events.Send(context.WithoutCancel(ctx), body, delay)
	}
	publish(sesevent.Event{EventType: "Send", Mail: m, Send: &struct{}{}}, 0)
	d := s.EventDelay
	for _, rcpt := range in.Recipients {
		local := strings.ToLower(rcpt[:strings.LastIndexByte(rcpt, '@')])
		at := now.Add(d)
		switch {
		case strings.HasSuffix(local, "+bounce") || local == "bounce":
			publish(sesevent.Event{EventType: "Bounce", Mail: m, Bounce: &sesevent.Bounce{
				BounceType: "Permanent", BounceSubType: "General", Timestamp: at, FeedbackID: "fb-" + uuid.NewString(),
				BouncedRecipients: []sesevent.Recipient{{EmailAddress: rcpt, Action: "failed", Status: "5.1.1",
					DiagnosticCode: "smtp; 550 5.1.1 user unknown (simulated)"}},
			}}, d)
		case strings.HasSuffix(local, "+suppressed") || local == "suppressionlist":
			publish(sesevent.Event{EventType: "Bounce", Mail: m, Bounce: &sesevent.Bounce{
				BounceType: "Permanent", BounceSubType: "OnAccountSuppressionList", Timestamp: at, FeedbackID: "fb-" + uuid.NewString(),
				BouncedRecipients: []sesevent.Recipient{{EmailAddress: rcpt, Action: "failed", Status: "5.1.1",
					DiagnosticCode: "Amazon SES did not send the message to this address because it is on the suppression list for your account."}},
			}}, d)
		case strings.HasSuffix(local, "+complaint") || local == "complaint":
			publish(delivery(m, rcpt, at), d)
			publish(sesevent.Event{EventType: "Complaint", Mail: m, Complaint: &sesevent.Complaint{
				Timestamp: at.Add(d), FeedbackID: "fb-" + uuid.NewString(), ComplaintFeedbackType: "abuse",
				ComplainedRecipients: []sesevent.Recipient{{EmailAddress: rcpt}},
			}}, 2*d)
		case strings.HasSuffix(local, "+delay"):
			publish(sesevent.Event{EventType: "DeliveryDelay", Mail: m, DeliveryDelay: &sesevent.DeliveryDelay{
				Timestamp: at, DelayType: "TransientCommunicationFailure",
				DelayedRecipients: []sesevent.Recipient{{EmailAddress: rcpt, Status: "4.4.7", DiagnosticCode: "smtp; 421 try again later (simulated)"}},
			}}, d)
			publish(delivery(m, rcpt, at.Add(d)), 2*d)
		default:
			publish(delivery(m, rcpt, at), d)
		}
	}
	return messageID, nil
}

func delivery(m sesevent.Mail, rcpt string, at time.Time) sesevent.Event {
	return sesevent.Event{EventType: "Delivery", Mail: m, Delivery: &sesevent.Delivery{
		Timestamp: at, Recipients: []string{rcpt}, SMTPResponse: "250 2.0.0 OK (simulated)",
	}}
}

func (s *Simulator) EnsureTenant(context.Context, string) error { return nil }

// CreateIdentity returns deterministic fake Easy DKIM tokens.
func (s *Simulator) CreateIdentity(ctx context.Context, domain, _, _ string, claimID uuid.UUID) (Identity, error) {
	if claimID == uuid.Nil {
		return Identity{}, ErrIdentityOwnership
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO localmock.ses_identities (domain,claim_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, domain, claimID); err != nil {
		return Identity{}, err
	}
	if _, err := s.GetIdentity(ctx, domain, claimID); err != nil {
		return Identity{}, err
	}
	var tokens []string
	for i := range 3 {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", domain, i)))
		tokens = append(tokens, hex.EncodeToString(sum[:16]))
	}
	return Identity{DKIMTokens: tokens}, nil
}

func (s *Simulator) GetIdentity(ctx context.Context, domain string, claimID uuid.UUID) (IdentityStatus, error) {
	var owner uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT claim_id FROM localmock.ses_identities WHERE domain=$1`, domain).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdentityStatus{}, ErrIdentityNotFound
	}
	if err != nil {
		return IdentityStatus{}, err
	}
	if owner != claimID || owner == uuid.Nil {
		return IdentityStatus{}, ErrIdentityOwnership
	}
	return IdentityStatus{FollowDNS: true}, nil
}

func (s *Simulator) DeleteIdentity(ctx context.Context, domain string, claimID uuid.UUID) error {
	if _, err := s.GetIdentity(ctx, domain, claimID); err != nil {
		if errors.Is(err, ErrIdentityNotFound) {
			return nil
		}
		return err
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM localmock.ses_identities WHERE domain=$1 AND claim_id=$2`, domain, claimID)
	return err
}
