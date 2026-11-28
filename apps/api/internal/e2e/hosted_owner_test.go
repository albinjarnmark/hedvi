package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/testenv"
)

type ownerEnv struct {
	e             *testenv.Env
	h             http.Handler
	normal, grant *http.Cookie
	uid           uuid.UUID
	secret        string
	signInSecret  string
}

func newOwnerFactor(uid uuid.UUID) console.OwnerFactor {
	secret := auth.NewTOTPSecret()
	return console.OwnerFactor{UserID: uid, Email: "security@example.test", Environment: "test", Secret: secret, Code: auth.TOTPNow(secret, time.Now())}
}

func ownerFixture(t *testing.T) (*ownerEnv, *http.Cookie) {
	t.Helper()
	e := testenv.New(t)
	original, uid, _, _ := signupForSecurity(t, e)
	secret := auth.NewTOTPSecret()
	enc, err := e.Sealer.Seal([]byte(secret), []byte("totp:"+uid.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE users SET totp_enabled_at=now(),totp_secret_enc=$2 WHERE id=$1`, uid, enc)
	factor := newOwnerFactor(uid)
	if err := console.GrantStaff(e.Ctx, e.WorkerPool, e.Sealer, "test", uid, "security@example.test", "Independent verification of exact synthetic account", true, factor); err != nil {
		t.Fatal(err)
	}
	h := http.NewServeMux()
	srv := *e.Console
	srv.Pool = e.WorkerPool
	srv.StaffPool = e.WorkerPool
	srv.Limiter = permissiveAuthLimiter{}
	srv.OwnerRoutes(h)
	s := &ownerEnv{e: e, h: h, uid: uid, secret: factor.Secret, signInSecret: secret}
	local := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, permissiveAuthLimiter{})
	code, body, headers := authCall(t, local, nil, "/console/v1/auth/login", map[string]string{"email": "security@example.test", "password": "long-test-password-123", "totp_code": auth.TOTPNow(secret, time.Now())})
	if code != 200 {
		t.Fatal("signin", code, body)
	}
	s.normal = (&http.Response{Header: headers}).Cookies()[0]
	return s, original
}
func (s *ownerEnv) call(method, path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "/console/v1/admin/"+path, bytes.NewReader(raw))
	r.RemoteAddr = "unparseable"
	r.Header.Set("X-Hedvi-Console", "1")
	for _, c := range cookies {
		if c != nil {
			r.AddCookie(c)
		}
	}
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	return w
}
func (s *ownerEnv) do(t *testing.T, status int, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	w := s.call(method, path, body, s.normal, s.grant)
	if w.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w
}
func (s *ownerEnv) unlock(t *testing.T) {
	t.Helper()
	w := s.do(t, 200, "POST", "unlock", map[string]string{"code": auth.TOTPNow(s.secret, time.Now().Add(30*time.Second))})
	s.grant = w.Result().Cookies()[0]
}
func TestOwnerAuthoritySessionsAndRedactedProjection(t *testing.T) {
	s, bootstrapSession := ownerFixture(t)
	e := s.e
	method, path := "GET", "summary"
	if w := s.call(method, path, nil, bootstrapSession); w.Code != 401 {
		t.Fatal("pre-bootstrap session survived", w.Code)
	}
	s.do(t, 403, method, path, nil)
	if _, err := e.APIPool.Exec(e.Ctx, `UPDATE users SET is_staff=true WHERE id=$1`, s.uid); err == nil {
		t.Fatal("public role can appoint owner")
	}
	if _, err := e.APIPool.Exec(e.Ctx, `SELECT * FROM owner_sessions`); err == nil {
		t.Fatal("public role can read owner sessions")
	}
	if _, err := e.APIPool.Exec(e.Ctx, `SELECT owner_secret_enc FROM instance_owner`); err == nil {
		t.Fatal("public role can inspect owner binding")
	}
	r := httptest.NewRequest("POST", "/console/v1/admin/unlock", strings.NewReader("{}"))
	r.AddCookie(s.normal)
	w := httptest.NewRecorder()
	s.h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	s.do(t, 403, "POST", "unlock", map[string]string{"code": auth.TOTPNow(s.signInSecret, time.Now())})
	s.unlock(t)
	s.do(t, 403, "POST", "unlock", map[string]string{"code": auth.TOTPNow(s.secret, time.Now())})
	e.Exec(`INSERT INTO alerts(kind,severity,dedupe_key,message,data) VALUES('diagnostic-private-value','warning','synthetic-test','sensitive-body-value','{"key":"sensitive-secret-value"}')`)
	tn := e.NewTenant("free")
	msg, err := e.Accept.Send(e.Ctx, tn.Live, "projection-fixture", liveBody(tn, "synthetic-recipient@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`INSERT INTO send_attempts(id,org_id,email_id,worker_id,state,error_code,error_message) VALUES($1,$2,$3,'worker','rejected','sensitive-provider-value','sensitive-raw-response')`, uuid.New(), tn.OrgID, msg.EmailID)
	w = s.do(t, 200, method, path, nil)
	if x := w.Body.String(); strings.Contains(x, "sensitive-") || strings.Contains(x, "diagnostic-private-value") || strings.Contains(x, "synthetic-recipient@example.test") || !strings.Contains(x, "Send rejected") || !strings.Contains(x, "Operations alert") || !strings.Contains(x, "security@example.test") {
		t.Fatal("unexpected operational projection")
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("cachable owner page")
	}
	_, _, otherHeaders := signupConsole(t, e, "ordinary@example.test", "another-long-test-password")
	other := (&http.Response{Header: otherHeaders}).Cookies()[0]
	if w := s.call(method, path, nil, other, s.grant); w.Code != 404 {
		t.Fatal("ordinary account used owner token", w.Code)
	}
	if w := s.call(method, path, nil, s.grant); w.Code != 401 {
		t.Fatal("owner token alone accepted", w.Code)
	}
	if _, err := e.APIPool.Exec(e.Ctx, `INSERT INTO audit_log(actor_kind,action,target_type,target_id) VALUES('staff','forged','user','invalid')`); err == nil {
		t.Fatal("public role can forge staff audit")
	}
	if code, _, _ := authCall(t, e.Handler, s.normal, "/console/v1/auth/logout", map[string]any{}); code != 200 {
		t.Fatal("logout", code)
	}
	s.do(t, 401, method, path, nil)
	if e.Count(`SELECT count(*) FROM owner_sessions`) != 0 {
		t.Fatal("owner session survived logout")
	}
}
func TestOwnerCredentialRotationAndRevoke(t *testing.T) {
	s, _ := ownerFixture(t)
	e := s.e
	s.unlock(t)
	e.Exec(`UPDATE users SET credential_version=credential_version+1 WHERE id=$1`, s.uid)
	s.do(t, 401, "GET", "summary", nil)
	if e.Count(`SELECT count(*) FROM owner_sessions`) != 0 {
		t.Fatal("credential rotation retained privilege")
	}
	replacement := auth.NewTOTPSecret()
	enc, err := e.Sealer.Seal([]byte(replacement), []byte("totp:"+s.uid.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE users SET totp_secret_enc=$2,totp_last_step=0 WHERE id=$1`, s.uid, enc)
	status, out, h := authCall(t, e.Handler, nil, "/console/v1/auth/login", map[string]string{"email": "security@example.test", "password": "long-test-password-123", "totp_code": auth.TOTPNow(replacement, time.Now())})
	if status != 200 {
		t.Fatal("new ordinary sign-in", status, out)
	}
	s.normal = (&http.Response{Header: h}).Cookies()[0]
	s.do(t, 404, "POST", "unlock", map[string]string{"code": auth.TOTPNow(s.secret, time.Now().Add(30*time.Second))})
	if err := console.RevokeStaff(e.Ctx, e.WorkerPool, s.uid, "Retiring the synthetic test instance owner"); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM instance_owner`) != 0 || e.Count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, s.uid) != 0 {
		t.Fatal("revocation incomplete")
	}
}
func TestOwnerGrantConcurrencyRevokeAndLiveReview(t *testing.T) {
	s, _ := ownerFixture(t)
	s.unlock(t)
	e := s.e
	tn := e.NewTenant("free")
	e.Exec(`UPDATE projects SET sending_limits_manual=false WHERE id=$1`, tn.ProjectID)
	gid := uuid.New()
	grant := map[string]any{"org_id": tn.OrgID, "request_id": gid, "plan": "flex", "reason": "Synthetic pilot membership"}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.call("POST", "memberships", grant, s.normal, s.grant).Code }()
	}
	wg.Wait()
	close(results)
	for code := range results {
		if code != 200 {
			t.Fatal("concurrent grant", code)
		}
	}
	if e.Count(`SELECT count(*) FROM credit_lots WHERE complimentary_grant=$1 AND granted=10000 AND data_granted=262144000 AND stripe_invoice IS NULL`, gid) != 1 || e.Count(`SELECT count(*) FROM audit_log WHERE action='membership.granted' AND target_id=$1 AND ip IS NULL`, gid.String()) != 1 {
		t.Fatal("grant duplicated or incomplete")
	}
	grant["reason"] = "A different grant intent"
	s.do(t, 409, "POST", "memberships", grant)
	grant["request_id"] = uuid.New()
	s.do(t, 409, "POST", "memberships", grant)
	if e.Count(`SELECT count(*) FROM billing_orders`) != 0 {
		t.Fatal("complimentary membership wrote Stripe order")
	}
	if e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()) WHERE hourly=100 AND daily=1000 AND NOT manual`, tn.ProjectID) != 1 {
		t.Fatal("active complimentary Flex did not enable Flex throughput")
	}
	send, err := e.Accept.Send(e.Ctx, tn.Live, "gifted", liveBody(tn, "gift-recipient@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM quota_reservations r JOIN credit_lots l ON r.credit_lot_id=l.id WHERE r.email_id=$1 AND l.complimentary_grant=$2`, send.EmailID, gid) != 1 {
		t.Fatal("Flex was billed to wrong source")
	}
	revoke := "memberships/" + gid.String() + "/revoke"
	for range 2 {
		s.do(t, 200, "POST", revoke, map[string]string{"reason": "Pilot membership ended"})
	}
	if e.Count(`SELECT count(*) FROM audit_log WHERE action='membership.revoked' AND target_id=$1`, gid.String()) != 1 {
		t.Fatal("duplicate revocation")
	}
	if e.Count(`SELECT count(*) FROM hedvi_live_limits($1,now()) WHERE hourly=50 AND daily=200 AND NOT manual`, tn.ProjectID) != 1 {
		t.Fatal("revoked complimentary Flex retained paid throughput")
	}
	e.Drain()
	if e.Count(`SELECT count(*) FROM credit_lots WHERE complimentary_grant=$1 AND consumed=1 AND reserved=0 AND NOT billing_usable`, gid) != 1 {
		t.Fatal("original Flex reservation did not settle after revocation")
	}
	after, err := e.Accept.Send(e.Ctx, tn.Live, "after-gift", liveBody(tn, "other@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM quota_reservations r JOIN usage_periods p ON r.usage_period_id=p.id WHERE r.email_id=$1 AND p.complimentary_grant IS NULL AND p.plan='free'`, after.EmailID) != 1 {
		t.Fatal("baseline not restored")
	}
	decision := "projects/" + tn.ProjectID.String() + "/decision"
	approve := map[string]any{"action": "approve", "hourly_limit": 25, "daily_limit": 100, "reason": "Reviewed synthetic consent and volume"}
	e.Exec(`UPDATE projects SET sending_status='test_only' WHERE id=$1`, tn.ProjectID)
	s.do(t, 409, "POST", decision, approve)
	e.Exec(`UPDATE projects SET sending_status='pending_review' WHERE id=$1`, tn.ProjectID)
	approve["hourly_limit"] = 101
	s.do(t, 422, "POST", decision, approve)
	approve["hourly_limit"] = 25
	e.Exec(`UPDATE organizations SET risk_status='restricted' WHERE id=$1`, tn.OrgID)
	s.do(t, 409, "POST", decision, approve)
	e.Exec(`UPDATE organizations SET risk_status='new' WHERE id=$1`, tn.OrgID)
	s.do(t, 200, "POST", decision, approve)
	if e.Count(`SELECT count(*) FROM projects WHERE id=$1 AND sending_status='approved' AND hourly_limit=25 AND daily_limit=100`, tn.ProjectID) != 1 {
		t.Fatal("approved limits missing")
	}
	e.Exec(`UPDATE projects SET status_changed_at=now()-interval '5 days' WHERE id=$1`, tn.ProjectID)
	s.do(t, 200, "POST", decision, map[string]any{"action": "approve", "use_plan_limits": true, "reason": "Restore current plan limits without resetting risk history"})
	if e.Count(`SELECT count(*) FROM projects p CROSS JOIN LATERAL hedvi_live_limits(p.id,now()) l WHERE p.id=$1 AND NOT l.manual AND l.hourly=50 AND l.daily=200 AND p.status_changed_at<now()-interval '4 days'`, tn.ProjectID) != 1 {
		t.Fatal("owner could not restore automatic limits while retaining risk history")
	}
	s.do(t, 200, "POST", decision, map[string]string{"action": "pause", "reason": "Paused pending external confirmation"})
	if _, err := e.Accept.Send(e.Ctx, tn.Live, "paused", liveBody(tn, "no-send@example.test")); err == nil {
		t.Fatal("sending remained open after pause")
	}
}
func TestPaidProPermanentlySupersedesComplimentaryPro(t *testing.T) {
	s, _ := ownerFixture(t)
	s.unlock(t)
	e := s.e
	tn := e.NewTenant("free")
	gid := uuid.New()
	grant := map[string]any{"org_id": tn.OrgID, "request_id": gid, "plan": "pro_50k", "reason": "Review complimentary Pro accounting"}
	s.do(t, 200, "POST", "memberships", grant)
	first, err := e.Accept.Send(e.Ctx, tn.Live, "gift-period", liveBody(tn, "pro@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM quota_reservations r JOIN usage_periods p ON p.id=r.usage_period_id WHERE r.email_id=$1 AND p.included=50000 AND p.complimentary_grant=$2`, first.EmailID, gid) != 1 {
		t.Fatal("wrong gift period")
	}
	oid := uuid.New()
	e.Exec(`INSERT INTO billing_customers(org_id,currency,billing_profile) VALUES($1,'sek','{"name":"test","line1":"test","line2":"","city":"Test","postal_code":"11111","state":"","country":"SE"}')`, tn.OrgID)
	e.Exec(`INSERT INTO billing_orders(id,org_id,request_id,sku,currency,price_id,amount,state,country,return_project_id) VALUES($1,$2,$3,'pro_100k','sek','fixture_price',24900,'active','SE',$4)`, oid, tn.OrgID, uuid.New(), tn.ProjectID)
	e.Exec(`INSERT INTO billing_invoices(id,org_id,order_id,state) VALUES('fixture_invoice',$1,$2,'granted')`, tn.OrgID, oid)
	e.Exec(`INSERT INTO usage_periods(org_id,period_start,period_end,plan,included,data_included,stripe_invoice) VALUES($1,now()-interval '1 hour',now()+interval '27 days','pro_100k',100000,2147483648,'fixture_invoice')`, tn.OrgID)
	if e.Count(`SELECT count(*) FROM complimentary_memberships WHERE id=$1 AND revoked_at IS NOT NULL AND revoke_reason='paid_subscription'`, gid) != 1 {
		t.Fatal("overlapping Pro gift not superseded")
	}
	e.Exec(`UPDATE usage_periods SET billing_usable=false WHERE stripe_invoice='fixture_invoice'`)
	grant["request_id"] = uuid.New()
	s.do(t, 409, "POST", "memberships", grant)
	e.Drain()
	if e.Count(`SELECT count(*) FROM usage_periods WHERE complimentary_grant=$1 AND consumed=1 AND NOT billing_usable`, gid) != 1 {
		t.Fatal("gift reopened or lost committed reservation")
	}
	if e.Count(`SELECT count(*) FROM organizations o WHERE id=$1 AND hedvi_effective_plan(id,now())='free'`, tn.OrgID) != 1 {
		t.Fatal("refund reopened gift")
	}
}

func TestOwnerUnlockBudgetSessionBindingExpiryAndRebootstrap(t *testing.T) {
	s, _ := ownerFixture(t)
	e := s.e
	for range 5 {
		s.do(t, 403, "POST", "unlock", map[string]string{"code": "xxxxxx"})
	}
	if e.Count(`SELECT count(*) FROM instance_owner WHERE user_id=$1 AND locked_until>now() AND failures=5`, s.uid) != 1 {
		t.Fatal("invalid proofs did not lock the durable owner binding")
	}
	s.do(t, 403, "POST", "unlock", map[string]string{"code": auth.TOTPNow(s.secret, time.Now().Add(30*time.Second))})
	if e.Count(`SELECT count(*) FROM owner_sessions`) != 0 {
		t.Fatal("locked owner issued a capability")
	}
	e.Exec(`UPDATE instance_owner SET locked_until=now()-interval '1 second',window_at=now()-interval '16 minutes' WHERE user_id=$1`, s.uid)
	s.unlock(t)
	// A distinct, genuinely MFA-marked ordinary session for the very same owner
	// must not inherit the browser capability bound to the original session.
	var id uuid.UUID
	second := &http.Cookie{Name: s.normal.Name, Value: uuid.NewString()}
	if err := e.WorkerPool.QueryRow(e.Ctx, `INSERT INTO sessions(user_id,token_hash,expires_at,mfa_verified_at) VALUES($1,$2,now()+interval '1 hour',now()) RETURNING id`, s.uid, e.Hasher.Sum("session", second.Value)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if w := s.call("GET", "summary", nil, second, s.grant); w.Code != 403 {
		t.Fatal("owner capability transferred between sessions", w.Code)
	}
	e.Exec(`UPDATE owner_sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1`, s.uid)
	s.do(t, 403, "GET", "summary", nil)
	if err := console.GrantStaff(e.Ctx, e.WorkerPool, e.Sealer, "test", s.uid, "security@example.test", "Reverified the existing independent synthetic owner", true, newOwnerFactor(s.uid)); err != nil {
		t.Fatal(err)
	}
	if e.Count(`SELECT count(*) FROM owner_sessions`) != 0 || e.Count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NULL`, s.uid) != 0 {
		t.Fatal("rebootstrap failed to invalidate ordinary or elevated sessions")
	}
	s.do(t, 401, "GET", "summary", nil)
}

func TestComplimentaryGrantLimitsAndFilteredReview(t *testing.T) {
	s, _ := ownerFixture(t)
	s.unlock(t)
	e := s.e
	one, two := e.NewTenant("free"), e.NewTenant("free")
	// Removing a gift cannot reset the rolling grant limit.
	for i := 0; i < 4; i++ {
		id := uuid.New()
		s.do(t, 200, "POST", "memberships", map[string]any{"request_id": id, "org_id": one.OrgID, "plan": "flex", "reason": "Synthetic acceptance review"})
		s.do(t, 200, "POST", "memberships/"+id.String()+"/revoke", map[string]string{"reason": "Synthetic acceptance ended"})
	}
	s.do(t, 429, "POST", "memberships", map[string]any{"request_id": uuid.New(), "org_id": one.OrgID, "plan": "flex", "reason": "Synthetic acceptance review"})
	id := uuid.New()
	s.do(t, 200, "POST", "memberships", map[string]any{"request_id": id, "org_id": two.OrgID, "plan": "flex", "reason": "Different synthetic account"})
	reply := s.do(t, 200, "GET", "summary?org="+two.OrgID.String()+"&project="+two.ProjectID.String(), nil)
	var data struct{ Grants, Reviews []struct{ ID uuid.UUID } }
	if err := json.Unmarshal(reply.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Grants) != 1 || data.Grants[0].ID != id || len(data.Reviews) != 1 || data.Reviews[0].ID != two.ProjectID {
		t.Fatal("owner scope filters returned the wrong records")
	}
}

func TestFutureProInvoiceClosesGiftAtCycleStart(t *testing.T) {
	s, _ := ownerFixture(t)
	s.unlock(t)
	e := s.e
	tn := e.NewTenant("free")
	gid, oid := uuid.New(), uuid.New()
	s.do(t, 200, "POST", "memberships", map[string]any{"org_id": tn.OrgID, "request_id": gid, "plan": "pro_50k", "reason": "Synthetic time-bound membership"})
	e.Exec(`INSERT INTO billing_customers(org_id,currency,billing_profile) VALUES($1,'sek','{"name":"test","line1":"test","line2":"","city":"Test","postal_code":"11111","state":"","country":"SE"}')`, tn.OrgID)
	e.Exec(`INSERT INTO billing_orders(id,org_id,request_id,sku,currency,price_id,amount,state,country,return_project_id) VALUES($1,$2,$3,'pro_100k','sek','fixture_price',24900,'active','SE',$4)`, oid, tn.OrgID, uuid.New(), tn.ProjectID)
	e.Exec(`INSERT INTO billing_invoices(id,org_id,order_id,state) VALUES('upcoming_invoice',$1,$2,'granted')`, tn.OrgID, oid)
	e.Exec(`INSERT INTO usage_periods(org_id,period_start,period_end,plan,included,data_included,stripe_invoice) VALUES($1,now()+interval '2 days',now()+interval '33 days','pro_100k',100000,2147483648,'upcoming_invoice')`, tn.OrgID)
	if e.Count(`SELECT count(*) FROM complimentary_memberships g JOIN usage_periods gift ON gift.complimentary_grant=g.id JOIN usage_periods paid ON paid.stripe_invoice='upcoming_invoice' WHERE g.id=$1 AND g.revoked_at IS NULL AND g.ends_at=paid.period_start AND gift.period_end=paid.period_start AND gift.billing_usable`, gid) != 1 {
		t.Fatal("upcoming payment prematurely closed or overlapped gift")
	}
	if e.Count(`SELECT count(*) FROM organizations WHERE id=$1 AND hedvi_effective_plan(id,now())='pro_50k' AND hedvi_effective_plan(id,now()+interval '3 days')='pro_100k'`, tn.OrgID) != 1 {
		t.Fatal("cycle boundary did not switch allowance")
	}
}

func TestOwnerAccountSearchIsBoundedLiteralAndSessionScoped(t *testing.T) {
	s, _ := ownerFixture(t)
	e := s.e
	s.do(t, 403, "POST", "search", map[string]string{"query": "security@"})
	s.unlock(t)
	s.do(t, 422, "POST", "search", map[string]string{"query": "a"})
	s.do(t, 422, "POST", "search", map[string]string{"query": strings.Repeat("a", 129)})
	var result struct {
		Organizations []struct {
			ID         uuid.UUID
			Name       string
			OwnerEmail string `json:"owner_email"`
		}
	}
	search := func(q string) {
		t.Helper()
		r := s.do(t, 200, "POST", "search", map[string]string{"query": q})
		if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	search("SECURITY@")
	if len(result.Organizations) != 1 || result.Organizations[0].OwnerEmail != "security@example.test" {
		t.Fatal("owner email prefix did not resolve organization")
	}
	id := result.Organizations[0].ID
	e.Exec(`UPDATE organizations SET name='A%_Example' WHERE id=$1`, id)
	e.Exec(`INSERT INTO organizations(name) SELECT 'Alpha Match '||n FROM generate_series(1,30)n`)
	search("a%_")
	if len(result.Organizations) != 1 || result.Organizations[0].ID != id {
		t.Fatal("organization name search treated wildcards as metacharacters")
	}
	search("alpha match")
	if len(result.Organizations) != 20 {
		t.Fatal("organization search failed bounded result contract", len(result.Organizations))
	}
	search("unregistered-name")
	if len(result.Organizations) != 0 {
		t.Fatal("unexpected match")
	}
}
