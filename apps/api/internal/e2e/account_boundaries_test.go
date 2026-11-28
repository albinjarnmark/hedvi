package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"hedvi.dev/api/internal/apierr"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/sesevent"
	"hedvi.dev/api/internal/testenv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOperatorAuthNamespaceCannotBeUsedByCustomers(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	reserved := "auth.example.test"
	e.Domains.ReservedDomain = reserved
	e.Accept.ReservedSenderDomain = reserved
	denied := func(err error) {
		t.Helper()
		var ae *apierr.Error
		if !errors.As(err, &ae) || ae.Status != 403 {
			t.Fatal("reserved namespace not rejected", err)
		}
	}
	for _, name := range []string{reserved, "child." + reserved, "example.test"} {
		_, err := e.Domains.Create(e.Ctx, tn.OrgID, tn.ProjectID, name, "send")
		denied(err)
	}
	var id uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `INSERT INTO domains(org_id,project_id,name,status) VALUES($1,$2,$3,'verified') RETURNING id`, tn.OrgID, tn.ProjectID, reserved).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, err := e.Domains.Verify(e.Ctx, tn.OrgID, tn.ProjectID, id)
	denied(err)
	denied(e.Domains.Delete(e.Ctx, tn.OrgID, tn.ProjectID, id))
	body := map[string]any{"from": "accounts@" + reserved, "to": "recipient@example.com", "subject": "impersonation", "text": "no"}
	for _, key := range []string{tn.TestKey, tn.LiveKey} {
		if code, _, _ := call(t, e.Handler, "POST", "/emails", key, body, nil); code != 403 {
			t.Fatal(code)
		}
	}
	// An accepted message from before the config change must also fail at send.
	e.Accept.ReservedSenderDomain = ""
	e.Sender.ReservedSenderDomain = reserved
	e.Exec(`UPDATE domains SET verified_at=now() WHERE id=$1`, id)
	raw, _ := json.Marshal(body)
	accepted, err := e.Accept.Send(e.Ctx, tn.Live, "", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Sender.Handle(e.Ctx, accepted.EmailID); err != nil {
		t.Fatal(err)
	}
	if n := e.Count(`SELECT count(*) FROM localmock.ses_outbound`); n != 0 {
		t.Fatalf("worker sent %d messages from a reserved identity", n)
	}
	if e.Count(`SELECT count(*) FROM recipients WHERE email_id=$1 AND status='failed' AND status_detail LIKE 'reserved_sender:%'`, accepted.EmailID) != 1 {
		t.Fatal("worker did not record the reserved-sender rejection")
	}
}

func TestAuthProofExpiryPurposeAndCredentialGeneration(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "expire@example.test"})
	token := e.AuthToken("expire@example.test")
	if code, _, _ := authRequest(t, e, "password-reset/complete", map[string]any{"token": token, "password": "owner-password-123"}); code != 400 {
		t.Fatal("purpose confused", code)
	}
	e.Exec(`UPDATE auth_challenges SET created_at=now()-interval '31 minutes',expires_at=now()-interval '1 minute'`)
	if code, _, _ := authRequest(t, e, "signup/complete", map[string]any{"token": token, "password": "owner-password-123", "consent": consent()}); code != 400 {
		t.Fatal("expired token", code)
	}
	_, _, _ = signupConsole(t, e, "generation@example.test", "owner-password-123")
	allowResend(e)
	_, _, _ = authRequest(t, e, "password-reset/request", map[string]any{"email": "generation@example.test"})
	token = e.AuthToken("generation@example.test")
	e.Exec(`UPDATE users SET credential_version=credential_version+1 WHERE email='generation@example.test'`)
	if code, _, _ := authRequest(t, e, "password-reset/complete", map[string]any{"token": token, "password": "changed-password-123"}); code != 400 {
		t.Fatal("stale credential generation", code)
	}
}

func TestIDNALoginAndSignupLocaleAreCanonical(t *testing.T) {
	e := testenv.New(t)
	email := "owner@bücher.example"
	if c, _, _ := authRequest(t, e, "signup", map[string]any{"email": email, "locale": "sv"}); c != 202 {
		t.Fatal(c)
	}
	token := e.AuthToken("owner@xn--bcher-kva.example")
	c, out, _ := authRequest(t, e, "signup/complete", map[string]any{"token": token, "password": "owner-password-123", "consent": consent()})
	if c != 201 {
		t.Fatal(c, out)
	}
	if e.Count(`SELECT count(*) FROM users WHERE locale='sv'`) != 1 || e.Count(`SELECT count(*) FROM organizations WHERE currency='SEK'`) != 1 {
		t.Fatal("verified locale lost")
	}
	for _, email := range []string{email, "owner@xn--bcher-kva.example"} {
		if c, b, _ := authRequest(t, e, "login", map[string]any{"email": email, "password": "owner-password-123"}); c != 200 {
			t.Fatal(c, b)
		}
	}
}

func TestAuthMailEventIdentityCannotSuppressOtherRecipient(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "event@example.test"})
	_ = e.AuthToken("event@example.test")
	var id, attempt uuid.UUID
	var mid string
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id,attempt_id,provider_message_id FROM auth_mail_outbox`).Scan(&id, &attempt, &mid); err != nil {
		t.Fatal(err)
	}
	makeEvent := func() sesevent.Event {
		return sesevent.Event{EventType: "Complaint", Mail: sesevent.Mail{Source: e.AuthMail.From, Destination: []string{"event@example.test"}, MessageID: mid, Tags: map[string][]string{"hedvi_auth": {id.String()}, "hedvi_auth_attempt": {attempt.String()}}}, Complaint: &sesevent.Complaint{ComplainedRecipients: []sesevent.Recipient{{EmailAddress: "event@example.test"}}}}
	}
	for _, change := range []func(*sesevent.Event){func(ev *sesevent.Event) { ev.Mail.Source = "attacker@example.test" }, func(ev *sesevent.Event) { ev.Mail.Destination = []string{"wrong@example.test"} }, func(ev *sesevent.Event) { ev.Complaint.ComplainedRecipients[0].EmailAddress = "wrong@example.test" }, func(ev *sesevent.Event) { ev.Mail.Tags["hedvi_project"] = []string{uuid.NewString()} }, func(ev *sesevent.Event) { ev.EventType = "unknown" }, func(ev *sesevent.Event) { ev.Mail.Tags["hedvi_auth"] = []string{id.String(), id.String()} }} {
		ev := makeEvent()
		change(&ev)
		if err := e.Events.Apply(e.Ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if e.Count(`SELECT count(*) FROM auth_mail_suppressions`) != 0 {
		t.Fatal("mismatched event suppressed address")
	}
	if err := e.Events.Apply(e.Ctx, makeEvent()); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM auth_mail_suppressions WHERE reason='complaint' AND expires_at IS NULL`) != 1 {
		t.Fatal("valid complaint ignored")
	}
	allowResend(e)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "event@example.test"})
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("suppressed address mailed")
	}
}

func TestAuthAdmissionCannotExhaustAPIPoolBehindGlobalLock(t *testing.T) {
	e := testenv.New(t)
	tx, err := e.WorkerPool.Begin(e.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(e.Ctx)
	if err := authmail.Lock(e.Ctx, tx); err != nil {
		t.Fatal(err)
	}
	s := &authmail.Service{Pool: e.APIPool, Hasher: e.Hasher, Sealer: e.Sealer}
	var wg sync.WaitGroup
	wg.Add(30)
	for i := range 30 {
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = s.Request(ctx, "signup", "mail"+strings.Repeat("a", i)+"@example.test", "en", "192.0.2.1")
		}(i)
	}
	// Ensure both bounded admission slots have had time to reach the lock.
	deadline := time.Now().Add(time.Second)
	for e.APIPool.Stat().AcquiredConns() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if e.APIPool.Stat().AcquiredConns() > 2 {
		t.Fatal("auth work occupied all API connections")
	}
	ctx, cancel := context.WithTimeout(e.Ctx, 250*time.Millisecond)
	defer cancel()
	var one int
	if err := e.APIPool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		t.Fatal("unrelated API query starved", err)
	}
	if err := tx.Rollback(e.Ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestOwnershipMigrationDoesNotVerifyLegacyAccounts(t *testing.T) {
	e := testenv.New(t)
	_, uid, _, _ := signupForSecurity(t, e)
	cfg := e.WorkerPool.Config()
	cfg.ConnConfig.User = "hedvi_owner"
	cfg.ConnConfig.Password = "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	_, err = owner.Exec(e.Ctx, `DROP TABLE legal_receipts,auth_mail_outbox,auth_challenges,auth_mail_suppressions,auth_mail_budgets CASCADE;
 -- Remove the later owner trigger while reconstructing the pre-0009 schema.
 DROP TRIGGER IF EXISTS revoke_owner_credentials ON users;
 DROP FUNCTION IF EXISTS hedvi_auth_pending_count();ALTER TABLE users DROP COLUMN email_verified_at,DROP COLUMN credential_version;
 DELETE FROM schema_migrations WHERE version='0009_account_ownership'`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(e.Ctx, owner); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND email_verified_at IS NULL`, uid) != 1 {
		t.Fatal("legacy ownership inferred")
	}
	if e.Count(`SELECT count(*) FROM sessions WHERE revoked_at IS NULL`) != 0 || e.Count(`SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`) != 0 {
		t.Fatal("legacy credentials survived migration")
	}
}
