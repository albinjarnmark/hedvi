package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/authmail"
	"hedvi.dev/api/internal/legal"
	"hedvi.dev/api/internal/testenv"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func consent() legal.Consent {
	return legal.Consent{Version: legal.Current().Version, Terms: true, AUP: true, PrivacyNotice: true, DPA: true, SubprocessorsNotice: true, CookiesNotice: true}
}
func signupConsole(t *testing.T, e *testenv.Env, email, password string) (int, map[string]any, http.Header) {
	t.Helper()
	code, out, _ := call(t, e.Handler, "POST", "/console/v1/auth/signup", "", map[string]any{"email": email}, map[string]string{"X-Hedvi-Console": "1"})
	if code != 202 {
		t.Fatal(code, out)
	}
	token := e.AuthToken(email)
	code, out, h := call(t, e.Handler, "POST", "/console/v1/auth/signup/complete", "", map[string]any{"token": token, "password": password, "name": "Tester", "organization": "Test", "consent": consent()}, map[string]string{"X-Hedvi-Console": "1"})
	if code != 201 {
		t.Fatal(code, out)
	}
	// Existing non-auth regressions count customer simulator messages only.
	e.Exec(`DELETE FROM localmock.ses_outbound WHERE tags ? 'hedvi_auth'`)
	return code, out, h
}
func authRequest(t *testing.T, e *testenv.Env, path string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	return call(t, e.Handler, "POST", "/console/v1/auth/"+path, "", body, map[string]string{"X-Hedvi-Console": "1"})
}
func allowResend(e *testenv.Env) {
	e.Exec(`UPDATE auth_mail_budgets SET last_at=now()-interval '2 minutes' WHERE key LIKE 'email:%'`)
}

func TestSignupRequiresMailOwnershipAndRejectsPreHijack(t *testing.T) {
	e := testenv.New(t)
	if code, _, _ := authRequest(t, e, "signup", map[string]any{"email": "owner@example.test", "password": "attacker-password-123"}); code != 422 {
		t.Fatal(code)
	}
	code, newBody, _ := authRequest(t, e, "signup", map[string]any{"email": "owner@example.test"})
	if code != 202 {
		t.Fatal(code)
	}
	if e.Count(`SELECT count(*) FROM users`) != 0 || e.Count(`SELECT count(*) FROM organizations`) != 0 || e.Count(`SELECT count(*) FROM sessions`) != 0 {
		t.Fatal("unproved request created credentials")
	}
	if _, err := e.APIPool.Exec(e.Ctx, `SELECT * FROM auth_mail_outbox`); err == nil {
		t.Fatal("API can read encrypted delivery payloads")
	}
	token := e.AuthToken("owner@example.test")
	bad := consent()
	bad.Version = "old"
	body := map[string]any{"token": token, "password": "owner-selected-password-123", "consent": bad}
	if code, _, _ := authRequest(t, e, "signup/complete", body); code != 422 {
		t.Fatal(code)
	}
	body["consent"] = consent()
	code, out, h := authRequest(t, e, "signup/complete", body)
	if code != 201 {
		t.Fatal(code, out)
	}
	if len((&http.Response{Header: h}).Cookies()) != 1 || e.Count(`SELECT count(*) FROM users WHERE email_verified_at IS NOT NULL`) != 1 {
		t.Fatal("completion not atomic")
	}
	if code, _, _ := authRequest(t, e, "signup/complete", body); code != 400 {
		t.Fatal("replay", code)
	}
	allowResend(e)
	code, existingBody, _ := authRequest(t, e, "signup", map[string]any{"email": "owner@example.test"})
	if code != 202 {
		t.Fatal(code)
	}
	a, _ := json.Marshal(newBody)
	b, _ := json.Marshal(existingBody)
	if !bytes.Equal(a, b) {
		t.Fatal("address existence exposed")
	}
	if code, _, _ := authRequest(t, e, "login", map[string]any{"email": "owner@example.test", "password": "attacker-password-123"}); code != 401 {
		t.Fatal(code)
	}
}

func TestConcurrentSignupProofSingleUse(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "race@example.test"})
	token := e.AuthToken("race@example.test")
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, _ := json.Marshal(map[string]any{"token": token, "password": "owner-password-1234", "consent": consent()})
			r := httptest.NewRequest("POST", "/console/v1/auth/signup/complete", bytes.NewReader(b))
			r.RemoteAddr = "192.0.2." + string(rune('1'+i)) + ":1234"
			r.Header.Set("X-Hedvi-Console", "1")
			w := httptest.NewRecorder()
			e.Handler.ServeHTTP(w, r)
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for c := range results {
		if c == 201 {
			won++
		} else if c != 400 && c != 503 {
			t.Fatal(c)
		}
	}
	if won != 1 || e.Count(`SELECT count(*) FROM users`) != 1 || e.Count(`SELECT count(*) FROM sessions`) != 1 {
		t.Fatal("multiple proof winners", won)
	}
}

func TestRecoveryRevokesSessionsPreservesMFAAndInvalidatesQueuedProofs(t *testing.T) {
	e := testenv.New(t)
	cookie, uid, _, _ := signupForSecurity(t, e)
	secret := auth.NewTOTPSecret()
	enc, err := e.Sealer.Seal([]byte(secret), []byte("totp:"+uid.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE users SET totp_secret_enc=$2,totp_enabled_at=now(),totp_last_step=123,email_verified_at=NULL WHERE id=$1`, uid, enc)
	allowResend(e)
	if c, _, _ := authRequest(t, e, "password-reset/request", map[string]any{"email": "security@example.test"}); c != 202 {
		t.Fatal(c)
	}
	token := e.AuthToken("security@example.test")
	allowResend(e)
	if c, _, _ := authRequest(t, e, "password-reset/request", map[string]any{"email": "security@example.test"}); c != 202 {
		t.Fatal(c)
	}
	if c, b, _ := authRequest(t, e, "password-reset/complete", map[string]any{"token": token, "password": "new-owner-password-123"}); c != 200 {
		t.Fatal(c, b)
	}
	if e.Count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, uid) != 0 {
		t.Fatal("session survived reset")
	}
	if cookieRequest(e.Handler, cookie, "GET", "/console/v1/me", nil) != 401 {
		t.Fatal("old cookie survived")
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND email_verified_at IS NOT NULL AND totp_secret_enc=$2 AND totp_last_step=123 AND totp_enabled_at IS NOT NULL AND credential_version=1`, uid, enc) != 1 {
		t.Fatal("MFA/ownership state changed incorrectly")
	}
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE state='queued'`) != 0 {
		t.Fatal("queued old proof survived reset")
	}
	if c, _, _ := authRequest(t, e, "password-reset/complete", map[string]any{"token": token, "password": "another-new-password"}); c != 400 {
		t.Fatal(c)
	}
	if c, b, _ := authRequest(t, e, "login", map[string]any{"email": "security@example.test", "password": "new-owner-password-123"}); c != 401 || b["mfa_required"] != true {
		t.Fatal(c, b)
	}
}

func TestAuthMailDurableBudgetsAndPhysicalQueueCeiling(t *testing.T) {
	e := testenv.New(t)
	s := &authmail.Service{Pool: e.APIPool, Hasher: e.Hasher, Sealer: e.Sealer}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Request(context.Background(), "signup", "same@example.test", "en", "192.0.2.1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if e.Count(`SELECT count(*) FROM auth_mail_outbox`) != 1 {
		t.Fatal("cooldown raced")
	}
	e.Exec(`DELETE FROM auth_challenges`)
	e.Exec(`WITH ids AS (SELECT uuidv7() id FROM generate_series(1,1000)), ins AS (
 INSERT INTO auth_challenges(id,purpose,token_hash,email_hash,email_enc,created_at,expires_at)
 SELECT id,'signup',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'x',now()-interval '2 hours',now()-interval '90 minutes' FROM ids RETURNING id)
 INSERT INTO auth_mail_outbox(id,payload_enc) SELECT id,'x' FROM ins`)
	if err := s.Request(e.Ctx, "signup", "different@example.test", "en", "192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM auth_mail_outbox`) != 1000 {
		t.Fatal("expired queued ciphertext bypassed physical cap")
	}
}

func TestAuthMailUnknownNeverAutomaticallyRetries(t *testing.T) {
	e := testenv.New(t)
	e.Fault = "unknown_after_accept"
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "unknown@example.test"})
	token := e.AuthToken("unknown@example.test")
	if e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE state='unknown' AND payload_enc IS NULL`) != 1 {
		t.Fatal("unknown outcome retained retryable payload")
	}
	e.Fault = ""
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("automatic duplicate auth mail")
	}
	if c, b, _ := authRequest(t, e, "signup/complete", map[string]any{"token": token, "password": "owner-password-1234", "consent": consent()}); c != 201 {
		t.Fatal(c, b)
	}
}

func TestExistingSignupNoticeRetriesWithoutRedeemableProof(t *testing.T) {
	e := testenv.New(t)
	_, _, _ = signupConsole(t, e, "existing@example.test", "owner-password-1234")
	allowResend(e)
	_, _, _ = authRequest(t, e, "signup", map[string]any{"email": "existing@example.test"})
	e.Fault = "throttle"
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM auth_mail_outbox WHERE state='queued'`) != 1 {
		t.Fatal("notice not retryable")
	}
	e.Fault = ""
	e.Exec(`UPDATE auth_mail_outbox SET available_at=now()-interval '1 second' WHERE state='queued'`)
	if err := e.AuthMail.RunOnce(e.Ctx); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("notice retry canceled")
	}
	var raw []byte
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT raw FROM localmock.ses_outbound LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	parts, err := authmail.ReadTextParts(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range parts {
		if strings.Contains(content, "#token=") || strings.Contains(content, "expires in 30 minutes") {
			t.Fatal("account-exists notice disclosed a credential or claimed proof expiry")
		}
	}
	if e.Count(`SELECT count(*) FROM auth_challenges WHERE consumed_at IS NULL AND ready_at IS NOT NULL`) != 0 {
		t.Fatal("account notice became an ownership proof")
	}
}

func TestAuthIngressGroupsIPv6BeforeDecode(t *testing.T) {
	e := testenv.New(t)
	for i := range 6 {
		r := httptest.NewRequest("POST", "/console/v1/auth/password-reset/complete", bytes.NewBufferString("invalid json"))
		r.RemoteAddr = "[2001:db8:1:1::" + string(rune('1'+i)) + "]:1234"
		r.Header.Set("X-Hedvi-Console", "1")
		w := httptest.NewRecorder()
		e.Handler.ServeHTTP(w, r)
		if i < 5 && w.Code != 422 {
			t.Fatal(w.Code)
		}
		if i == 5 && w.Code != 429 {
			t.Fatal("IPv6 ingress bypass", w.Code)
		}
	}
}

func TestLegalOriginAndVersionAreBoundToReceipt(t *testing.T) {
	e := testenv.New(t)
	code, out, _ := call(t, e.Handler, "GET", "/console/v1/auth/legal", "", nil, nil)
	if code != 200 {
		t.Fatal(code, out)
	}
	raw, _ := json.Marshal(out)
	var manifest legal.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, d := range manifest.Documents {
		if !strings.HasPrefix(d.URL, "https://legal.example.test/legal/versions/"+legal.Current().Version+"/") {
			t.Fatal(d.URL)
		}
	}
	signupConsole(t, e, "receipt@example.test", "owner-password-123")
	var origin, version string
	var documents []byte
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT document_origin,manifest_version,documents FROM legal_receipts`).Scan(&origin, &version, &documents); err != nil {
		t.Fatal(err)
	}
	if origin != "https://legal.example.test" || version != legal.Current().Version {
		t.Fatal(origin, version)
	}
	var receipt []legal.Document
	if err := json.Unmarshal(documents, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, d := range receipt {
		if !strings.HasPrefix(d.URL, "/legal/versions/"+version+"/") || len(d.SHA256) != 64 {
			t.Fatal(d)
		}
	}
}
