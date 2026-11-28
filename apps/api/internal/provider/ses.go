package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"hedvi.dev/api/internal/config"
)

// SES sends through Amazon SES v2 with Tenant Management. The client must be
// built with retries disabled (aws.NopRetryer); Hedvi decides when a retry is
// safe.
type SES struct {
	Env           config.Env
	AuthTenant    string
	AuthConfigSet string
	Client        *sesv2.Client
	Region        string
	AccountID     string
	ConfigSet     string
}

func (s *SES) Send(ctx context.Context, in SendInput) (string, error) {
	if s.Env != config.EnvStaging && s.Env != config.EnvProduction || !s.authorizesSend(in) {
		return "", &SendError{Outcome: Rejected, Code: "tenant_binding", Message: "The sending project configuration requires operator review.", Alert: true}
	}
	tags := make([]sestypes.MessageTag, 0, len(in.Tags))
	names := make([]string, 0, len(in.Tags))
	for k := range in.Tags {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		tags = append(tags, sestypes.MessageTag{Name: aws.String(k), Value: aws.String(in.Tags[k])})
	}
	from := in.From
	if in.Tags["hedvi_auth"] != "" {
		from = (&mail.Address{Name: "Hedvi", Address: in.From}).String()
	}
	input := &sesv2.SendEmailInput{
		FromEmailAddress:     aws.String(from),
		Destination:          &sestypes.Destination{ToAddresses: in.Recipients},
		Content:              &sestypes.EmailContent{Raw: &sestypes.RawMessage{Data: in.Raw}},
		EmailTags:            tags,
		ConfigurationSetName: aws.String(in.ConfigurationSet),
	}
	if in.TenantName != "" {
		input.TenantName = aws.String(in.TenantName)
	}
	out, err := s.Client.SendEmail(ctx, input)
	if err != nil {
		return "", classifySES(err)
	}
	if out == nil || aws.ToString(out.MessageId) == "" {
		return "", &SendError{Outcome: Unknown, Code: "missing_message_id", Message: "Provider acceptance could not be confirmed."}
	}
	return *out.MessageId, nil
}

func (s *SES) authorizesSend(in SendInput) bool {
	if in.Tags["hedvi_auth"] != "" {
		return s.AuthTenant != "" && in.TenantName == s.AuthTenant && in.ConfigurationSet == s.AuthConfigSet && in.Tags["hedvi_project"] == ""
	}
	id, err := uuid.Parse(in.Tags["hedvi_project"])
	return err == nil && in.ConfigurationSet == s.ConfigSet && s.Env.CheckProjectTenant(id, in.TenantName) == nil
}

func classifySES(err error) error {
	var signingErr *v4.SigningError
	if errors.As(err, &signingErr) {
		return &SendError{Outcome: Retryable, Code: "signing_failed", Message: "Unable to sign provider request.", Alert: true}
	}
	var dnsErr *net.DNSError
	var opErr *net.OpError
	if errors.As(err, &dnsErr) || (errors.As(err, &opErr) && opErr.Op == "dial") {
		// The request never left this host.
		return &SendError{Outcome: Retryable, Code: "connect_failed", Message: err.Error()}
	}
	var respErr *smithyhttp.ResponseError
	status := 0
	if errors.As(err, &respErr) {
		status = respErr.HTTPStatusCode()
	}
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		if status == 401 || status == 403 {
			return &SendError{Outcome: Retryable, Code: "provider_access_denied", Message: "The provider denied access.", Alert: true}
		}
		// Timeouts, resets and cancellations after the request was written.
		return &SendError{Outcome: Unknown, Code: "transport_error", Message: err.Error()}
	}
	code := apiErr.ErrorCode()
	switch code {
	case "TooManyRequestsException", "LimitExceededException", "ThrottlingException":
		return &SendError{Outcome: Retryable, Code: code, Message: apiErr.ErrorMessage()}
	case "AccountSuspendedException", "SendingPausedException", "NotFoundException", "MailFromDomainNotVerifiedException", "AccessDenied", "AccessDeniedException", "ExpiredToken", "ExpiredTokenException", "InvalidClientTokenId", "SignatureDoesNotMatch", "UnrecognizedClientException", "InvalidSignatureException":
		return &SendError{Outcome: Retryable, Code: code, Message: apiErr.ErrorMessage(), Alert: true}
	case "MessageRejected", "BadRequestException":
		return &SendError{Outcome: Rejected, Code: code, Message: apiErr.ErrorMessage()}
	}
	switch {
	case status == 503:
		return &SendError{Outcome: Retryable, Code: code, Message: apiErr.ErrorMessage()}
	case status == 408:
		return &SendError{Outcome: Unknown, Code: code, Message: apiErr.ErrorMessage()}
	case status >= 400 && status < 500:
		// A definitive API refusal that does not establish a customer content error.
		return &SendError{Outcome: Retryable, Code: code, Message: apiErr.ErrorMessage(), Alert: true}
	default:
		return &SendError{Outcome: Unknown, Code: code, Message: apiErr.ErrorMessage()}
	}
}

func (s *SES) EnsureTenant(ctx context.Context, tenant string) error {
	if !s.validCustomerTenant(tenant) {
		return config.ErrProjectTenant
	}
	// GetTenant for a missing tenant is authorized against resource "*" by
	// SES, so a correctly resource-scoped runtime policy gets AccessDenied,
	// not NotFound. Create first; AlreadyExists never proves ownership.
	// Check the returned name and environment before associating resources.
	_, err := s.Client.CreateTenant(ctx, &sesv2.CreateTenantInput{TenantName: aws.String(tenant), Tags: []sestypes.Tag{s.environmentTag()}})
	var exists *sestypes.AlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return fmt.Errorf("create SES tenant: %w", err)
	}
	if err = s.checkTenant(ctx, tenant); err != nil {
		return err
	}
	return s.associate(ctx, tenant, fmt.Sprintf("arn:aws:ses:%s:%s:configuration-set/%s", s.Region, s.AccountID, s.ConfigSet))
}

func (s *SES) checkTenant(ctx context.Context, tenant string) error {
	out, err := s.Client.GetTenant(ctx, &sesv2.GetTenantInput{TenantName: aws.String(tenant)})
	if err != nil {
		return err
	}
	if out.Tenant == nil || aws.ToString(out.Tenant.TenantName) != tenant || !hasTag(out.Tenant.Tags, "environment", string(s.Env)) {
		return config.ErrProjectTenant
	}
	return nil
}

func (s *SES) validCustomerTenant(tenant string) bool {
	return (s.Env == config.EnvStaging || s.Env == config.EnvProduction) && s.Env.IsCustomerTenant(tenant)
}

func (s *SES) environmentTag() sestypes.Tag {
	return sestypes.Tag{Key: aws.String("environment"), Value: aws.String(string(s.Env))}
}

func hasTag(tags []sestypes.Tag, key, value string) bool {
	for _, t := range tags {
		if aws.ToString(t.Key) == key && aws.ToString(t.Value) == value {
			return true
		}
	}
	return false
}

func (s *SES) associate(ctx context.Context, tenant, arn string) error {
	_, err := s.Client.CreateTenantResourceAssociation(ctx, &sesv2.CreateTenantResourceAssociationInput{
		TenantName: aws.String(tenant), ResourceArn: aws.String(arn),
	})
	var exists *sestypes.AlreadyExistsException
	if err != nil && !errors.As(err, &exists) {
		return fmt.Errorf("associate %s with tenant: %w", arn, err)
	}
	return nil
}

func (s *SES) CreateIdentity(ctx context.Context, domain, mailFromDomain, tenant string, claimID uuid.UUID) (Identity, error) {
	if !s.validCustomerTenant(tenant) {
		return Identity{}, config.ErrProjectTenant
	}
	if claimID == uuid.Nil {
		return Identity{}, ErrIdentityOwnership
	}
	if err := s.checkTenant(ctx, tenant); err != nil {
		return Identity{}, err
	}
	var retryUntil time.Time
	got, err := s.ownedIdentity(ctx, domain, claimID)
	if errors.Is(err, ErrIdentityNotFound) {
		_, err = s.Client.CreateEmailIdentity(ctx, &sesv2.CreateEmailIdentityInput{
			EmailIdentity: aws.String(domain), ConfigurationSetName: aws.String(s.ConfigSet),
			Tags:                  []sestypes.Tag{{Key: aws.String(DomainClaimTag), Value: aws.String(claimID.String())}, s.environmentTag()},
			DkimSigningAttributes: &sestypes.DkimSigningAttributes{NextSigningKeyLength: sestypes.DkimSigningKeyLengthRsa2048Bit},
		})
		var exists *sestypes.AlreadyExistsException
		if err != nil && !errors.As(err, &exists) {
			return Identity{}, fmt.Errorf("create SES identity: %w", err)
		}
		if err == nil {
			retryUntil = time.Now().Add(8 * time.Second)
		}
		got, err = s.ownedIdentity(ctx, domain, claimID)
	}
	if err != nil {
		return Identity{}, err
	}
	if got.DkimAttributes == nil || len(got.DkimAttributes.Tokens) == 0 {
		return Identity{}, errors.New("SES identity does not yet have Easy DKIM tokens")
	}
	mailFrom := &sesv2.PutEmailIdentityMailFromAttributesInput{
		EmailIdentity: aws.String(domain), MailFromDomain: aws.String(mailFromDomain),
		BehaviorOnMxFailure: sestypes.BehaviorOnMxFailureUseDefaultValue,
	}
	if err := s.writeOwnedIdentity(ctx, domain, claimID, retryUntil, func(ctx context.Context) error {
		_, err := s.Client.PutEmailIdentityMailFromAttributes(ctx, mailFrom)
		return err
	}); err != nil {
		return Identity{}, fmt.Errorf("set MAIL FROM: %w", err)
	}
	arn := fmt.Sprintf("arn:aws:ses:%s:%s:identity/%s", s.Region, s.AccountID, domain)
	if err := s.writeOwnedIdentity(ctx, domain, claimID, retryUntil, func(ctx context.Context) error {
		return s.associate(ctx, tenant, arn)
	}); err != nil {
		return Identity{}, err
	}
	return Identity{DKIMTokens: got.DkimAttributes.Tokens}, nil
}

// SES can return the new identity's tags before IAM applies them to writes.
// Only a successful create in this call opens the shared propagation window.
func (s *SES) writeOwnedIdentity(ctx context.Context, domain string, claimID uuid.UUID, retryUntil time.Time, write func(context.Context) error) error {
	err := write(ctx)
	if !identityWriteDenied(err) || !time.Now().Before(retryUntil) {
		return err
	}
	retryCtx, cancel := context.WithDeadline(ctx, retryUntil)
	defer cancel()
	for _, delay := range [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if !time.Now().Add(delay).Before(retryUntil) {
			return err
		}
		select {
		case <-retryCtx.Done():
			return errors.Join(err, retryCtx.Err())
		case <-time.After(delay):
		}
		if _, checkErr := s.ownedIdentity(retryCtx, domain, claimID); checkErr != nil {
			return fmt.Errorf("recheck SES identity before retry: %w", checkErr)
		}
		err = write(retryCtx)
		if !identityWriteDenied(err) {
			return err
		}
	}
	return err
}

func identityWriteDenied(err error) bool {
	var apiErr smithy.APIError
	var resp *smithyhttp.ResponseError
	return errors.As(err, &apiErr) && (apiErr.ErrorCode() == "AccessDenied" || apiErr.ErrorCode() == "AccessDeniedException") &&
		errors.As(err, &resp) && resp.HTTPStatusCode() == 403
}

func (s *SES) ownedIdentity(ctx context.Context, domain string, claimID uuid.UUID) (*sesv2.GetEmailIdentityOutput, error) {
	if claimID == uuid.Nil || s.Env != config.EnvStaging && s.Env != config.EnvProduction {
		return nil, ErrIdentityOwnership
	}
	out, err := s.Client.GetEmailIdentity(ctx, &sesv2.GetEmailIdentityInput{EmailIdentity: aws.String(domain)})
	var missing *sestypes.NotFoundException
	if errors.As(err, &missing) {
		return nil, ErrIdentityNotFound
	}
	if err != nil {
		return nil, err
	}
	if hasTag(out.Tags, DomainClaimTag, claimID.String()) && hasTag(out.Tags, "environment", string(s.Env)) {
		return out, nil
	}
	return nil, ErrIdentityOwnership
}

func (s *SES) GetIdentity(ctx context.Context, domain string, claimID uuid.UUID) (IdentityStatus, error) {
	out, err := s.ownedIdentity(ctx, domain, claimID)
	if err != nil {
		return IdentityStatus{}, err
	}
	st := IdentityStatus{VerifiedForSending: out.VerifiedForSendingStatus}
	if out.DkimAttributes != nil {
		st.DKIMStatus = string(out.DkimAttributes.Status)
	}
	if out.MailFromAttributes != nil {
		st.MailFromStatus = string(out.MailFromAttributes.MailFromDomainStatus)
	}
	return st, nil
}

func (s *SES) DeleteIdentity(ctx context.Context, domain string, claimID uuid.UUID) error {
	_, err := s.ownedIdentity(ctx, domain, claimID)
	if errors.Is(err, ErrIdentityNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.Client.DeleteEmailIdentity(ctx, &sesv2.DeleteEmailIdentityInput{EmailIdentity: aws.String(domain)})
	var nf *sestypes.NotFoundException
	if errors.As(err, &nf) {
		return nil
	}
	return err
}
