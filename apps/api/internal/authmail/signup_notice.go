package authmail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/provider"
)

// SendSignupNotice claims one committed registration. A stale claim becomes
// unknown instead of automatically repeating a possibly accepted email.
func (w *Worker) SendSignupNotice(ctx context.Context) error {
	if w.SignupRecipient == "" {
		return nil
	}
	if _, err := w.Pool.Exec(ctx, `UPDATE signup_notifications SET state='unknown'
        WHERE state='sending' AND available_at<=now()`); err != nil {
		return err
	}
	attempt := uuid.New()
	var org uuid.UUID
	var created time.Time
	var test bool
	err := w.Pool.QueryRow(ctx, `UPDATE signup_notifications SET state='sending', attempt_id=$1,
        attempts=attempts+1,available_at=now()+interval '2 minutes'
        WHERE org_id=(SELECT org_id FROM signup_notifications WHERE state='queued' AND available_at<=now()
          ORDER BY available_at,org_id LIMIT 1 FOR UPDATE SKIP LOCKED)
        RETURNING org_id,created_at,is_test`, attempt).Scan(&org, &created, &test)
	if db.IsNoRows(err) {
		return nil
	}
	if err != nil {
		return err
	}

	link := strings.TrimRight(w.AppURL, "/") + "/app/admin"
	copy := accountCopy{Locale: "en", Subject: "New Hedvi customer", Eyebrow: "Owner console", Heading: "A new customer joined Hedvi.",
		Intro:  fmt.Sprintf("A customer verified their email and created a workspace on %s. You can find them under Latest registrations.", created.UTC().Format("2 Jan 2006 at 15:04 UTC")),
		Action: "View registrations", Expires: "Workspace ID: " + org.String(), URL: link, LinkLabel: "Open the owner console",
		Fallback: "You can also open the owner console here:", Ignore: "You receive this email as the Hedvi instance owner."}
	if test {
		copy.Subject, copy.Heading, copy.Intro, copy.Expires = "Hedvi signup notification test", "Signup notifications are connected.", "This is a delivery test for the owner notification mailbox. No new customer account was created.", ""
	}
	copy.Preheader = copy.Intro
	raw, err := w.renderCopy(copy, w.SignupRecipient)
	state := "failed"
	var mid string
	if err == nil {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		mid, err = w.Provider.Send(sendCtx, provider.SendInput{From: w.From, Recipients: []string{w.SignupRecipient}, Raw: raw,
			TenantName: w.Tenant, ConfigurationSet: w.ConfigSet, Tags: map[string]string{"hedvi_auth": org.String(), "hedvi_auth_attempt": attempt.String(), "hedvi_notice": "signup"}})
		cancel()
		switch {
		case err == nil:
			state = "accepted"
		case provider.Classify(err) == provider.Retryable:
			state = "queued"
		case provider.Classify(err) == provider.Unknown:
			state = "unknown"
		}
	}
	_, err = w.Pool.Exec(ctx, `UPDATE signup_notifications SET state=CASE WHEN $3='queued' AND attempts>=6 THEN 'failed' ELSE $3 END,
        available_at=now()+interval '1 minute'*(1<<attempts),provider_message_id=nullif($4,'')
        WHERE org_id=$1 AND attempt_id=$2 AND state='sending'`, org, attempt, state, mid)
	return err
}

// A delivery or failure can arrive before the worker records the send response.
func (w *Worker) applySignupEvent(ctx context.Context, org, attempt uuid.UUID, mid, kind string) error {
	state := ""
	switch kind {
	case "Delivery":
		state = "delivered"
	case "Send":
		state = "accepted"
	case "Bounce", "Complaint", "Reject", "Rendering Failure", "RenderingFailure":
		state = "failed"
	default:
		return nil
	}
	_, err := w.Pool.Exec(ctx, `UPDATE signup_notifications SET state=$4, provider_message_id=$3
        WHERE org_id=$1 AND attempt_id=$2 AND (provider_message_id IS NULL OR provider_message_id=$3)
        AND (state IN ('sending','unknown','accepted') OR state='delivered' AND $4='failed')`, org, attempt, mid, state)
	return err
}
