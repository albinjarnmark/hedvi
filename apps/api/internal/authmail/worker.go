package authmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/address"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/faults"
	"hedvi.dev/api/internal/provider"
	"hedvi.dev/api/internal/sesevent"
	"mime/quotedprintable"
	"net/url"
	"strings"
	"time"
)

type Worker struct {
	ClaimBegin db.Beginner // optional transaction fault injection
	Faults     *faults.Injector
	*Service
	Provider                        provider.Provider
	From, AppURL, Tenant, ConfigSet string
}
type job struct {
	ID, Attempt uuid.UUID
	Data        payload
	Purpose     string
	Existing    bool
}

// RunOnce performs one bounded send. Global coordination caps active attempts
// across replicas; a worker never holds a DB transaction while calling SES.
func (w *Worker) RunOnce(ctx context.Context) error {
	ctx, cancelWork := context.WithTimeout(ctx, 45*time.Second)
	defer cancelWork()
	if err := w.Cleanup(ctx); err != nil {
		return err
	}
	var j *job
	begin := w.ClaimBegin
	if begin == nil {
		begin = w.Pool
	}
	err := pgx.BeginFunc(ctx, begin, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_mail_outbox WHERE state='sending'`).Scan(&active); err != nil {
			return err
		}
		if active >= 2 {
			return nil
		}
		next := &job{Attempt: uuid.New()}
		var enc, emailHash, digest []byte
		var eligible bool
		err := tx.QueryRow(ctx, `SELECT o.id,o.payload_enc,c.purpose,c.email_hash,c.token_hash,
  c.expires_at>now() AND c.consumed_at IS NULL AND c.invalidated_at IS NULL
  FROM auth_mail_outbox o JOIN auth_challenges c ON c.id=o.id
  WHERE o.state='queued' AND o.available_at<=now() ORDER BY o.available_at,o.id LIMIT 1 FOR UPDATE OF o,c SKIP LOCKED`).Scan(&next.ID, &enc, &next.Purpose, &emailHash, &digest, &eligible)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		cancel := func(code string) error {
			_, err := tx.Exec(ctx, `UPDATE auth_mail_outbox SET state='canceled',payload_enc=NULL,finished_at=now(),result_code=$2 WHERE id=$1`, next.ID, code)
			return err
		}
		if !eligible {
			return cancel("link_unavailable")
		}
		var suppressed bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_mail_suppressions WHERE email_hash=$1 AND (expires_at IS NULL OR expires_at>now()))`, emailHash).Scan(&suppressed); err != nil {
			return err
		}
		if suppressed {
			return cancel("suppressed")
		}
		plain, err := w.Sealer.Open(enc, payloadAAD(next.ID))
		if err != nil {
			return cancel("payload_invalid")
		}
		if err = json.Unmarshal(plain, &next.Data); err != nil {
			return cancel("payload_invalid")
		}
		if !w.Hasher.Equal("auth-token:"+next.Purpose, next.Data.Token, digest) || !w.Hasher.Equal("auth-email", next.Data.Email, emailHash) {
			return cancel("payload_invalid")
		}
		var uid uuid.UUID
		var version int64
		err = tx.QueryRow(ctx, `SELECT id,credential_version FROM users WHERE email=$1 FOR UPDATE`, next.Data.Email).Scan(&uid, &version)
		if err != nil && !db.IsNoRows(err) {
			return err
		}
		exists := err == nil
		if next.Purpose == "reset" && !exists {
			return cancel("no_account")
		}
		next.Existing = next.Purpose == "signup" && exists
		if next.Existing {
			if _, err := tx.Exec(ctx, `UPDATE auth_challenges SET ready_at=NULL WHERE id=$1`, next.ID); err != nil {
				return err
			}
		} else {
			var user any
			var gen any
			if exists {
				user = uid
				gen = version
			}
			if _, err := tx.Exec(ctx, `UPDATE auth_challenges SET ready_at=coalesce(ready_at,now()),user_id=$2,credential_version=$3 WHERE id=$1`, next.ID, user, gen); err != nil {
				return err
			}
		}
		allowed, err := Charge(ctx, tx, "attempts", 100, 500, 0)
		if err != nil {
			return err
		}
		if !allowed {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_mail_outbox SET state='sending',attempt_id=$2,attempts=attempts+1,lease_until=now()+interval '2 minutes' WHERE id=$1`, next.ID, next.Attempt); err != nil {
			return err
		}
		j = next
		return nil
	})
	// In particular do not send after an ambiguous claim COMMIT result.
	if err != nil || j == nil {
		return err
	}
	if err := w.Faults.Check(faults.AuthAfterClaim); err != nil {
		return err
	}
	raw, err := w.render(j)
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	messageID, sendErr := w.Provider.Send(sendCtx, provider.SendInput{From: w.From, Recipients: []string{j.Data.Email}, Raw: raw, TenantName: w.Tenant, ConfigurationSet: w.ConfigSet, Tags: map[string]string{"hedvi_auth": j.ID.String(), "hedvi_auth_attempt": j.Attempt.String()}})
	cancel()
	if err := w.Faults.Check(faults.AuthAfterProvider); err != nil {
		return err
	}
	code := "accepted"
	state := "sent"
	if sendErr != nil {
		code = "provider_error"
		var se *provider.SendError
		if errors.As(sendErr, &se) {
			code = SafeCode(se.Code)
		}
		switch provider.Classify(sendErr) {
		case provider.Retryable:
			state = "queued"
		case provider.Rejected:
			state = "failed"
		default:
			state = "unknown"
		}
	}
	// Settlement can fail without causing a resend: the stale lease is unknown.
	_, err = w.Pool.Exec(ctx, `UPDATE auth_mail_outbox SET
 state=CASE WHEN $3='queued' AND attempts>=3 THEN 'failed' ELSE $3 END,
 payload_enc=CASE WHEN $3='queued' AND attempts<3 THEN payload_enc ELSE NULL END,
 available_at=now()+attempts*interval '1 minute', lease_until=NULL,
 finished_at=CASE WHEN $3='queued' AND attempts<3 THEN NULL ELSE now() END,
 provider_message_id=nullif($4,''),result_code=$5
 WHERE id=$1 AND attempt_id=$2 AND state='sending'`, j.ID, j.Attempt, state, messageID, code)
	if err != nil {
		return err
	}
	if state != "sent" {
		_, err = w.Pool.Exec(ctx, `INSERT INTO alerts(kind,severity,dedupe_key,message,data) VALUES('auth_mail_delivery','warning','auth-mail:'||$1,'Account security email delivery needs attention. Inspect system-mail status without logging message contents.',$2) ON CONFLICT DO NOTHING`, code, map[string]any{"outbox_id": j.ID.String(), "result": state, "code": code})
	}
	return err
}

func (w *Worker) render(j *job) ([]byte, error) {
	u, err := url.Parse(w.AppURL)
	if err != nil {
		return nil, err
	}
	path := "/verify-email"
	subject := "Finish creating your Hedvi account"
	intro := "Use this link to verify your email and choose your password."
	if j.Purpose == "reset" {
		path = "/reset-password"
		subject = "Reset your Hedvi password"
		intro = "Use this link to choose a new password. Your authenticator remains required if enabled."
	}
	if j.Existing {
		path = "/forgot-password"
		subject = "Your Hedvi account"
		intro = "An account already uses this email. Sign in or request a password reset here."
	}
	u.Path = path
	u.RawPath = ""
	u.RawQuery = "locale=" + j.Data.Locale
	u.Fragment = "token=" + j.Data.Token
	if j.Existing {
		u.Fragment = ""
	}
	body := intro + "\n\n" + u.String() + "\n\nThis link expires in 30 minutes. If you did not request this message, you can ignore it.\nHedvi\n"
	if j.Data.Locale == "sv" {
		body = "Du har begärt åtkomst till Hedvi. Öppna länken för att fortsätta. En eventuell autentiseringsapp krävs fortfarande.\n\n" + u.String() + "\n\nLänken gäller i 30 minuter. Om du inte begärde meddelandet kan du ignorera det.\nHedvi\n"
	}
	if j.Existing {
		body = strings.ReplaceAll(body, "This link expires in 30 minutes. ", "")
		body = strings.ReplaceAll(body, "Länken gäller i 30 minuter. ", "")
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "From: Hedvi <%s>\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", w.From, j.Data.Email, subject)
	q := quotedprintable.NewWriter(&out)
	if _, err := q.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := q.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (w *Worker) Cleanup(ctx context.Context) error {
	return db.Tx(ctx, w.Pool, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_mail_outbox SET state='unknown',payload_enc=NULL,finished_at=now(),result_code='lease_expired',lease_until=NULL WHERE state='sending' AND lease_until<=now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_mail_outbox o SET state='canceled',payload_enc=NULL,finished_at=now(),result_code='link_unavailable'
  FROM auth_challenges c WHERE c.id=o.id AND o.state='queued' AND (c.expires_at<=now() OR c.consumed_at IS NOT NULL OR c.invalidated_at IS NOT NULL)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_challenges SET email_enc=''::bytea,user_id=NULL,credential_version=NULL WHERE id IN (SELECT id FROM auth_challenges WHERE email_enc<>''::bytea AND (expires_at<=now() OR consumed_at IS NOT NULL OR invalidated_at IS NOT NULL) LIMIT 1000)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_challenges WHERE id IN (SELECT id FROM auth_challenges WHERE created_at<now()-interval '90 days' LIMIT 1000)`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM auth_mail_budgets WHERE key IN (SELECT key FROM auth_mail_budgets WHERE key NOT IN ('global','attempts') AND last_at<now()-interval '2 days' LIMIT 1000)`)
		return err
	})
}

// ApplyEvent handles only authenticated SES queue events with our job/attempt
// identity. It emits no tenant webhook and records no message body or token.
func (w *Worker) ApplyEvent(ctx context.Context, ev sesevent.Event) error {
	id, err := uuid.Parse(ev.Tag("hedvi_auth"))
	if err != nil {
		return nil
	}
	if ev.Mail.Source != w.From || len(ev.Mail.Destination) != 1 || ev.EventType != "" && ev.NotificationType != "" && ev.EventType != ev.NotificationType {
		return nil
	}
	for _, tag := range []string{"hedvi_email", "hedvi_project", "hedvi_attempt"} {
		if _, ok := ev.Mail.Tags[tag]; ok {
			return nil
		}
	}
	recipient, err := address.Parse(ev.Mail.Destination[0])
	if err != nil || recipient.Name != "" {
		return nil
	}
	match := func(v string) bool {
		a, err := address.Parse(v)
		return err == nil && a.Name == "" && a.Normalized == recipient.Normalized
	}
	switch ev.Type() {
	case "Send":
		if ev.Send == nil {
			return nil
		}
	case "Delivery":
		if ev.Delivery == nil || len(ev.Delivery.Recipients) != 1 || !match(ev.Delivery.Recipients[0]) {
			return nil
		}
	case "Bounce":
		if ev.Bounce == nil || len(ev.Bounce.BouncedRecipients) != 1 || !match(ev.Bounce.BouncedRecipients[0].EmailAddress) {
			return nil
		}
	case "Complaint":
		if ev.Complaint == nil || len(ev.Complaint.ComplainedRecipients) != 1 || !match(ev.Complaint.ComplainedRecipients[0].EmailAddress) {
			return nil
		}
	case "DeliveryDelay":
		if ev.DeliveryDelay == nil || len(ev.DeliveryDelay.DelayedRecipients) != 1 || !match(ev.DeliveryDelay.DelayedRecipients[0].EmailAddress) {
			return nil
		}
	case "Reject":
		if ev.Reject == nil {
			return nil
		}
	case "Rendering Failure", "RenderingFailure":
		if ev.Failure == nil {
			return nil
		}
	default:
		return nil
	}
	attempt, err := uuid.Parse(ev.Tag("hedvi_auth_attempt"))
	if err != nil || ev.Mail.MessageID == "" {
		return nil
	}
	return db.Tx(ctx, w.Pool, func(tx pgx.Tx) error {
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		var hash []byte
		var mid *string
		err := tx.QueryRow(ctx, `SELECT c.email_hash,o.provider_message_id FROM auth_mail_outbox o JOIN auth_challenges c ON c.id=o.id WHERE o.id=$1 AND o.attempt_id=$2 AND o.state IN ('sending','unknown','sent') FOR UPDATE OF o`, id, attempt).Scan(&hash, &mid)
		if db.IsNoRows(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if mid != nil && *mid != ev.Mail.MessageID {
			return nil
		}
		if !w.Hasher.Equal("auth-email", recipient.Normalized, hash) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE auth_mail_outbox SET state='sent',provider_message_id=$2,payload_enc=NULL,lease_until=NULL,finished_at=coalesce(finished_at,now()),result_code='event_confirmed' WHERE id=$1`, id, ev.Mail.MessageID); err != nil {
			return err
		}
		reason := ""
		var expiry any
		if ev.Type() == "Complaint" {
			reason = "complaint"
		} else if ev.Type() == "Bounce" && ev.Bounce.BounceType == "Permanent" {
			reason = "bounce"
			expiry = time.Now().Add(90 * 24 * time.Hour)
		}
		if reason == "" {
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO auth_mail_suppressions(email_hash,reason,expires_at) VALUES($1,$2,$3) ON CONFLICT(email_hash) DO UPDATE SET reason=CASE WHEN auth_mail_suppressions.reason='complaint' THEN 'complaint' ELSE excluded.reason END,expires_at=CASE WHEN auth_mail_suppressions.reason='complaint' OR excluded.reason='complaint' THEN NULL ELSE excluded.expires_at END`, hash, reason, expiry)
		return err
	})
}
