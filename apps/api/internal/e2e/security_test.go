package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/db"
	"hedvi.dev/api/internal/testenv"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestAPIKeysSeparateTestLiveAndDomainScopes(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := map[string]any{"from": "app@" + tn.Domain, "to": "test@example.com", "subject": "s", "text": "private"}
	ids := map[string]string{}
	for mode, key := range map[string]string{"test": tn.TestKey, "live": tn.LiveKey} {
		status, out, _ := call(t, e.Handler, "POST", "/emails", key, body, nil)
		if status != 200 {
			t.Fatal(status, out)
		}
		ids[mode] = out["id"].(string)
	}
	for mode, key := range map[string]string{"test": tn.TestKey, "live": tn.LiveKey} {
		other := "live"
		if mode == "live" {
			other = "test"
		}
		for _, prefix := range []string{"/emails/", "/hedvi/v1/emails/"} {
			if status, _, _ := call(t, e.Handler, "GET", prefix+ids[other], key, nil, nil); status != 404 {
				t.Fatalf("%s key read %s via %s: %d", mode, other, prefix, status)
			}
		}
		status, out, _ := call(t, e.Handler, "GET", "/emails?limit=1", key, nil, nil)
		if status != 200 || len(out["data"].([]any)) != 1 || out["data"].([]any)[0].(map[string]any)["id"] != ids[mode] {
			t.Fatalf("mode list: %d %v", status, out)
		}
	}
	var did uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM domains WHERE project_id=$1`, tn.ProjectID).Scan(&did); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{"GET", "/domains"}, {"POST", "/domains"}, {"GET", "/domains/" + did.String()}, {"POST", "/domains/" + did.String() + "/verify"}, {"DELETE", "/domains/" + did.String()}, {"GET", "/hedvi/v1/usage"}} {
		if status, _, _ := call(t, e.Handler, route.method, route.path, tn.TestKey, map[string]any{"name": "bad.test"}, nil); status != 401 {
			t.Fatalf("test key %s %s: %d", route.method, route.path, status)
		}
	}
	otherDomain := uuid.New()
	e.Exec(`INSERT INTO domains(id,org_id,project_id,name,status) VALUES($1,$2,$3,'other.test','verified')`, otherDomain, tn.OrgID, tn.ProjectID)
	e.Exec(`UPDATE api_keys SET domain_id=$2 WHERE id=$1`, tn.Live.KeyID, otherDomain)
	if status, _, _ := call(t, e.Handler, "GET", "/emails/"+ids["live"], tn.LiveKey, nil, nil); status != 404 {
		t.Fatal("sender-scoped read", status)
	}
	if status, out, _ := call(t, e.Handler, "GET", "/emails", tn.LiveKey, nil, nil); status != 200 || len(out["data"].([]any)) != 0 {
		t.Fatal("sender-scoped list", status, out)
	}
	if status, _, _ := call(t, e.Handler, "DELETE", "/domains/"+did.String(), tn.LiveKey, nil, nil); status != 401 {
		t.Fatal("sender-scoped delete", status)
	}
	if e.Count(`SELECT count(*) FROM domains WHERE id=$1 AND deleted_at IS NULL`, did) != 1 {
		t.Fatal("test key changed domain")
	}
}

func TestIdempotencySeparatesCaptureFromRealSend(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := liveBody(tn, "recipient@example.com")
	test, err := e.Accept.Send(e.Ctx, tn.Test, "shared-key", body)
	if err != nil {
		t.Fatal(err)
	}
	live, err := e.Accept.Send(e.Ctx, tn.Live, "shared-key", body)
	if err != nil {
		t.Fatal(err)
	}
	if live.Replayed || live.EmailID == test.EmailID {
		t.Fatal("live send reused test capture")
	}
	again, err := e.Accept.Send(e.Ctx, tn.Live, "shared-key", body)
	if err != nil || !again.Replayed || again.EmailID != live.EmailID {
		t.Fatal("live replay failed", err)
	}
	e.Drain()
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 1 {
		t.Fatal("expected exactly one real send")
	}
	if e.Count(`SELECT count(*) FROM idempotency_records WHERE key='shared-key'`) != 2 {
		t.Fatal("mode namespaces not separate")
	}
}

func signupForSecurity(t *testing.T, e *testenv.Env) (*http.Cookie, uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	_, out, h := signupConsole(t, e, "security@example.test", "long-test-password-123")
	response := &http.Response{Header: h}
	return response.Cookies()[0], uuid.MustParse(out["user"].(map[string]any)["id"].(string)), uuid.MustParse(out["organization"].(map[string]any)["id"].(string)), uuid.MustParse(out["project"].(map[string]any)["id"].(string))
}
func cookieRequest(h http.Handler, c *http.Cookie, method, path string, body any) int {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("X-Hedvi-Console", "1")
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestTOTPConcurrentLoginConsumesCodeOnce(t *testing.T) {
	e := testenv.New(t)
	_, uid, _, _ := signupForSecurity(t, e)
	secret := auth.NewTOTPSecret()
	enc, err := e.Sealer.Seal([]byte(secret), []byte("totp:"+uid.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE users SET totp_secret_enc=$2,totp_enabled_at=now() WHERE id=$1`, uid, enc)
	code := auth.TOTPNow(secret, time.Now().Add(30*time.Second))
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{"email": "security@example.test", "password": "long-test-password-123", "totp_code": code})
			r := httptest.NewRequest("POST", "/console/v1/auth/login", bytes.NewReader(raw))
			r.Header.Set("X-Hedvi-Console", "1")
			w := httptest.NewRecorder()
			e.Handler.ServeHTTP(w, r)
			results <- w.Code
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for status := range results {
		if status == 200 {
			ok++
		} else if status != 401 && status != 503 { // bounded Argon2 capacity may reject queued attempts
			t.Fatalf("unexpected status %d", status)
		}
	}
	if ok != 1 {
		t.Fatalf("same TOTP accepted %d times", ok)
	}
}

func TestStaffGrantRequiresVerifiedAccountAndRevokesSessions(t *testing.T) {
	e := testenv.New(t)
	cookie, uid, _, _ := signupForSecurity(t, e)
	note := "Verified exact user UUID with known person over established independent channel"
	if err := console.GrantStaff(e.Ctx, e.WorkerPool, uid, "security@example.test", note, true); err == nil {
		t.Fatal("staff without MFA")
	}
	e.Exec(`UPDATE users SET totp_enabled_at=now() WHERE id=$1`, uid)
	for _, c := range []struct {
		id       uuid.UUID
		email    string
		verified bool
	}{{uuid.Nil, "security@example.test", true}, {uid, "different@example.test", true}, {uid, "security@example.test", false}} {
		if err := console.GrantStaff(e.Ctx, e.WorkerPool, c.id, c.email, note, c.verified); err == nil {
			t.Fatal("unverified/mismatched staff grant")
		}
	}
	if err := console.GrantStaff(e.Ctx, e.WorkerPool, uid, "security@example.test", note, true); err != nil {
		t.Fatal(err)
	}
	if status := cookieRequest(e.Handler, cookie, "GET", "/console/v1/me", nil); status != 401 {
		t.Fatal("old staff session survived", status)
	}
	if e.Count(`SELECT count(*) FROM audit_log WHERE action='user.staff_granted' AND target_id=$1`, uid.String()) != 1 {
		t.Fatal("missing audit")
	}
}

func TestConcurrentTestStorageAndProjectLimits(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := liveBody(tn, "recipient@example.com")
	if _, err := e.Accept.Send(e.Ctx, tn.Test, "", body); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE projects SET test_storage_limit_bytes=(SELECT sum(payload_bytes)*2 FROM emails WHERE project_id=$1) WHERE id=$1`, tn.ProjectID)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := e.Accept.Send(e.Ctx, tn.Test, "", body); results <- err }()
	}
	wg.Wait()
	close(results)
	ok := 0
	for err := range results {
		if err == nil {
			ok++
		} else if apiErr(t, err).Status != 422 {
			t.Fatal(err)
		}
	}
	if ok != 1 {
		t.Fatalf("storage allowed %d extra captures, want 1", ok)
	}
	for i := 1; i < 10; i++ {
		if err := db.InOrg(e.Ctx, e.APIPool, tn.OrgID, func(tx pgx.Tx) error {
			_, err := console.NewProject(e.Ctx, tx, tn.OrgID, fmt.Sprintf("P%d", i), config.EnvTest)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := db.InOrg(e.Ctx, e.APIPool, tn.OrgID, func(tx pgx.Tx) error {
		_, err := console.NewProject(e.Ctx, tx, tn.OrgID, "excess", config.EnvTest)
		return err
	})
	if err == nil || apiErr(t, err).Status != 403 {
		t.Fatal("project limit", err)
	}
}

func TestDeletedDomainStopsPreviouslyQueuedMail(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "", liveBody(tn, "recipient@example.com")); err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE domains SET deleted_at=now() WHERE project_id=$1`, tn.ProjectID)
	e.Drain()
	if e.Count(`SELECT count(*) FROM localmock.ses_outbound`) != 0 {
		t.Fatal("deleted sender reached provider")
	}
	if e.Count(`SELECT count(*) FROM emails WHERE send_state='failed'`) != 1 {
		t.Fatal("queued job not settled")
	}
}

func TestLegacyIdempotencyTombstoneAndExpiry(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	body := liveBody(tn, "recipient@example.com")
	result, err := e.Accept.Send(e.Ctx, tn.Test, "legacy-deleted", body)
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`DELETE FROM emails WHERE id=$1`, result.EmailID)
	e.Exec(`UPDATE idempotency_records SET mode='legacy' WHERE key='legacy-deleted'`)
	if _, err = e.Accept.Send(e.Ctx, tn.Test, "legacy-deleted", body); err == nil || apiErr(t, err).Status != 409 {
		t.Fatal("legacy test retry", err)
	}
	if _, err = e.Accept.Send(e.Ctx, tn.Live, "legacy-deleted", body); err == nil || apiErr(t, err).Status != 409 {
		t.Fatal("legacy live retry", err)
	}
	e.Exec(`UPDATE idempotency_records SET expires_at=now()-interval '1 second' WHERE key='legacy-deleted'`)
	result, err = e.Accept.Send(e.Ctx, tn.Live, "legacy-deleted", body)
	if err != nil || result.Replayed {
		t.Fatal("expired tombstone should permit new operation", err)
	}
	e.Exec(`UPDATE idempotency_records SET expires_at=now()-interval '1 second' WHERE key='legacy-deleted'`)
	newer, err := e.Accept.Send(e.Ctx, tn.Live, "legacy-deleted", body)
	if err != nil || newer.EmailID == result.EmailID {
		t.Fatal("expired live idempotency should permit new operation", err)
	}
}

func TestManualWebhookBudgetSharedByTestAndResend(t *testing.T) {
	e := testenv.New(t)
	cookie, _, oid, pid := signupForSecurity(t, e)
	hook := uuid.New()
	e.Exec(`INSERT INTO webhooks(id,org_id,project_id,url,events,secret_enc) VALUES($1,$2,$3,'https://example.com/webhook','{}',$4)`, hook, oid, pid, []byte("unused-fixture"))
	base := "/console/v1/projects/" + pid.String() + "/webhooks/" + hook.String()
	var wg sync.WaitGroup
	results := make(chan int, 25)
	for range 25 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- cookieRequest(e.Handler, cookie, "POST", base+"/test", nil) }()
	}
	wg.Wait()
	close(results)
	ok, limited := 0, 0
	for status := range results {
		if status == 202 {
			ok++
		} else if status == 429 {
			limited++
		} else {
			t.Fatalf("unexpected webhook test %d", status)
		}
	}
	if ok != 20 || limited != 5 {
		t.Fatalf("budget accepted %d limited %d", ok, limited)
	}
	var delivery uuid.UUID
	if err := e.WorkerPool.QueryRow(e.Ctx, `SELECT id FROM webhook_deliveries WHERE webhook_id=$1 LIMIT 1`, hook).Scan(&delivery); err != nil {
		t.Fatal(err)
	}
	if status := cookieRequest(e.Handler, cookie, "POST", base+"/deliveries/"+delivery.String()+"/resend", nil); status != 429 {
		t.Fatal("resend bypassed budget", status)
	}
	if e.Count(`SELECT count(*) FROM webhook_events WHERE org_id=$1`, oid) != 20 {
		t.Fatal("rejected event left persisted work")
	}
}
