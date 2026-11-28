// Package provider sends email through Amazon SES v2 or a local simulator.
//
// Outcome classification is the core of the uncertain-send policy
// (docs/architecture/uncertain-sends.md): only errors that prove SES did not
// accept the message are retryable. Anything ambiguous is Unknown and is never
// retried automatically.
package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type Outcome int

const (
	Accepted  Outcome = iota
	Rejected          // permanent, the message was not and will not be accepted
	Retryable         // the message was provably not accepted; try again later
	Unknown           // SES may or may not have accepted the message
)

func (o Outcome) String() string {
	return [...]string{"accepted", "rejected", "retryable", "unknown"}[o]
}

type SendError struct {
	Outcome Outcome
	Code    string
	Message string
	// Alert marks errors that need operator attention (account paused etc.).
	Alert bool
}

func (e *SendError) Error() string { return fmt.Sprintf("%s: %s: %s", e.Outcome, e.Code, e.Message) }

// Classify extracts the outcome of an error returned by Send.
func Classify(err error) Outcome {
	if err == nil {
		return Accepted
	}
	var se *SendError
	if errors.As(err, &se) {
		return se.Outcome
	}
	return Unknown
}

type SendInput struct {
	From             string   // bare envelope sender, e.g. app@example.com
	Recipients       []string // envelope recipients (To, Cc and Bcc), never shown in headers
	Raw              []byte   // complete RFC 5322 message without Bcc header
	Tags             map[string]string
	TenantName       string
	ConfigurationSet string
}

type Identity struct {
	DKIMTokens []string
}

type IdentityStatus struct {
	// FollowDNS is set by the simulator: verification depends only on DNS.
	FollowDNS          bool
	VerifiedForSending bool
	DKIMStatus         string // PENDING, SUCCESS, FAILED, TEMPORARY_FAILURE, NOT_STARTED
	MailFromStatus     string // PENDING, SUCCESS, FAILED, TEMPORARY_FAILURE
	MailFromDomain     string
}

// DomainClaimTag binds the account-wide SES identity to an immutable database row.
const DomainClaimTag = "hedvi:domain-claim"

var (
	ErrIdentityOwnership = errors.New("sending identity ownership does not match the domain claim")
	ErrIdentityNotFound  = errors.New("sending identity not found")
)

type Provider interface {
	Send(ctx context.Context, in SendInput) (messageID string, err error)
	EnsureTenant(ctx context.Context, tenant string) error
	CreateIdentity(ctx context.Context, domain, mailFromDomain, tenant string, claimID uuid.UUID) (Identity, error)
	GetIdentity(ctx context.Context, domain string, claimID uuid.UUID) (IdentityStatus, error)
	DeleteIdentity(ctx context.Context, domain string, claimID uuid.UUID) error
}
