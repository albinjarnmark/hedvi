package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"hedvi.dev/api/internal/app"
	"hedvi.dev/api/internal/auth"
	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/console"
	"hedvi.dev/api/internal/ratelimit"
	"hedvi.dev/api/internal/testenv"
)

// Demonstrate that durable attempt budgets still hold when a cache restarts or
// an attacker distributes guesses across processes and source addresses.
type permissiveAuthLimiter struct{}

func (permissiveAuthLimiter) Allow(context.Context, string, float64, int) ratelimit.Decision {
	return ratelimit.Decision{Allowed: true}
}

func authConsole(e *testenv.Env, cfg *config.Config, limiter ratelimit.Limiter) http.Handler {
	mux := http.NewServeMux()
	s := &console.Server{Cfg: cfg, Pool: e.APIPool, StaffPool: e.WorkerPool, Hasher: e.Hasher, Sealer: e.Sealer, Accept: e.Accept, Domains: e.Domains, Provider: e.Sim, Blobs: e.Blobs, Limiter: limiter, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.Routes(mux)
	return mux
}

func authCall(t *testing.T, h http.Handler, cookie *http.Cookie, path string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("X-Hedvi-Console", "1")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Header()
}

func TestDurablePasswordAndTOTPBudgetsResetAfterExpiry(t *testing.T) {
	e := testenv.New(t)
	_, uid, _, _ := signupForSecurity(t, e)
	h := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, permissiveAuthLimiter{})
	request := map[string]any{"email": "security@example.test", "password": "wrong-password"}
	for range 10 {
		if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 401 {
			t.Fatal(status)
		}
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_logins=10 AND locked_until>now()`, uid) != 1 {
		t.Fatal("password failures were not durably locked")
	}
	request["password"] = "long-test-password-123"
	lockedStatus, lockedOut, _ := authCall(t, h, nil, "/console/v1/auth/login", request)
	unknown := map[string]any{"email": "unknown@example.test", "password": "long-test-password-123"}
	unknownStatus, unknownOut, _ := authCall(t, h, nil, "/console/v1/auth/login", unknown)
	if lockedStatus != 401 || unknownStatus != lockedStatus || lockedOut["message"] != unknownOut["message"] {
		t.Fatal("locked account enumeration", lockedStatus, lockedOut, unknownStatus, unknownOut)
	}
	e.Exec(`UPDATE users SET locked_until=now()-interval '1 second' WHERE id=$1`, uid)
	request["password"] = "wrong-password"
	if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 401 {
		t.Fatal(status)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_logins=1 AND locked_until IS NULL`, uid) != 1 {
		t.Fatal("expired password lock immediately relocked")
	}
	request["password"] = "long-test-password-123"
	secret := auth.NewTOTPSecret()
	enc, err := e.Sealer.Seal([]byte(secret), []byte("totp:"+uid.String()))
	if err != nil {
		t.Fatal(err)
	}
	e.Exec(`UPDATE users SET totp_secret_enc=$2,totp_enabled_at=now() WHERE id=$1`, uid, enc)
	request["totp_code"] = "invalid"
	for range 5 {
		if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 401 {
			t.Fatal(status)
		}
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_mfa=5 AND mfa_locked_until>now()`, uid) != 1 {
		t.Fatal("TOTP failure counter rolled back")
	}
	request["totp_code"] = auth.TOTPNow(secret, time.Now())
	if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 401 {
		t.Fatal("MFA lock bypass", status)
	}
	e.Exec(`UPDATE users SET mfa_locked_until=now()-interval '1 second' WHERE id=$1`, uid)
	request["totp_code"] = "invalid"
	if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 401 {
		t.Fatal(status)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_mfa=1 AND mfa_locked_until IS NULL`, uid) != 1 {
		t.Fatal("expired MFA budget did not reset")
	}
	request["totp_code"] = auth.TOTPNow(secret, time.Now())
	if status, _, _ := authCall(t, h, nil, "/console/v1/auth/login", request); status != 200 {
		t.Fatal("correct factors after expiry", status)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_mfa=0 AND failed_logins=0`, uid) != 1 {
		t.Fatal("successful sign-in did not clear failures")
	}
}

func TestMFAChangesRequirePasswordAndPersistFailedCodes(t *testing.T) {
	e := testenv.New(t)
	cookie, uid, _, _ := signupForSecurity(t, e)
	h := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, permissiveAuthLimiter{})
	if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/setup", map[string]any{}); status != 401 {
		t.Fatal("setup without password", status)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND totp_secret_enc IS NOT NULL`, uid) != 0 {
		t.Fatal("unauthorized setup changed secret")
	}
	status, out, _ := authCall(t, h, cookie, "/console/v1/me/mfa/setup", map[string]any{"password": "long-test-password-123"})
	if status != 200 {
		t.Fatal(status, out)
	}
	secret := out["secret"].(string)
	code := auth.TOTPNow(secret, time.Now())
	if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/enable", map[string]any{"code": code}); status != 401 {
		t.Fatal("enable without password", status)
	}
	for range 5 {
		if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/enable", map[string]any{"password": "long-test-password-123", "code": "bad"}); status != 401 {
			t.Fatal(status)
		}
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND failed_mfa=5 AND mfa_locked_until>now() AND totp_enabled_at IS NULL`, uid) != 1 {
		t.Fatal("setup failures not durably bounded")
	}
	e.Exec(`UPDATE users SET mfa_locked_until=now()-interval '1 second' WHERE id=$1`, uid)
	if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/enable", map[string]any{"password": "long-test-password-123", "code": auth.TOTPNow(secret, time.Now())}); status != 200 {
		t.Fatal(status)
	}
	if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/disable", map[string]any{"code": auth.TOTPNow(secret, time.Now().Add(30*time.Second))}); status != 401 {
		t.Fatal("disable without password", status)
	}
	if e.Count(`SELECT count(*) FROM users WHERE id=$1 AND totp_enabled_at IS NOT NULL`, uid) != 1 {
		t.Fatal("MFA disabled by session alone")
	}
	if status, _, _ := authCall(t, h, cookie, "/console/v1/me/mfa/disable", map[string]any{"password": "long-test-password-123", "code": auth.TOTPNow(secret, time.Now().Add(30*time.Second))}); status != 200 {
		t.Fatal("authorized disable", status)
	}
}

func TestAPIStartupRejectsPrivilegedRolesAndMigrationAccess(t *testing.T) {
	e := testenv.New(t)
	if err := app.CheckAPIRole(e.Ctx, e.APIPool); err != nil {
		t.Fatal(err)
	}
	if err := app.CheckAPIRole(e.Ctx, e.WorkerPool); err == nil {
		t.Fatal("worker accepted as API role")
	}
	cfg := e.WorkerPool.Config()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "hedvi_owner", "hedvi_owner_dev"
	owner, err := pgxpool.NewWithConfig(e.Ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := app.CheckAPIRole(e.Ctx, owner); err == nil {
		t.Fatal("owner accepted as API role")
	}
	for _, pool := range []*pgxpool.Pool{e.APIPool, e.WorkerPool} {
		if _, err := pool.Exec(e.Ctx, `UPDATE schema_migrations SET applied_at=now()`); err == nil {
			t.Fatal("runtime role may modify migration history")
		}
	}
	if _, err := owner.Exec(e.Ctx, `ALTER TABLE emails DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatal(err)
	}
	if err := app.CheckAPIRole(e.Ctx, e.APIPool); err == nil {
		t.Fatal("disabled tenant RLS accepted at startup")
	}
}

func TestDeployedPublicRoleNeverMountsOwnerRoutes(t *testing.T) {
	e := testenv.New(t)
	cookie, _, _, _ := signupForSecurity(t, e)
	deployed := authConsole(e, &config.Config{Env: config.EnvProduction, AWSMode: "aws"}, permissiveAuthLimiter{})
	for _, path := range []string{"/console/v1/admin/unlock", "/console/v1/admin/memberships", "/console/v1/admin/projects/" + uuid.NewString() + "/decision"} {
		if status, _, _ := authCall(t, deployed, cookie, path, map[string]any{}); status != 404 {
			t.Fatalf("deployed public console registered %s: %d", path, status)
		}
	}
}

func TestConsoleRateLimitSharedByUserAndOrganization(t *testing.T) {
	e := testenv.New(t)
	cookie, _, orgID, pid := signupForSecurity(t, e)
	h := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, ratelimit.NewMemory())
	secondUser := uuid.New()
	const secondToken = "another-session-for-rate-regression"
	e.Exec(`INSERT INTO users(id,email,password_hash,email_verified_at) VALUES($1,'second@example.test','unused',now())`, secondUser)
	e.Exec(`INSERT INTO legal_receipts(user_id,manifest_version,documents,user_agent,manifest_adopted,document_origin) SELECT $1,manifest_version,documents,'',manifest_adopted,document_origin FROM legal_receipts LIMIT 1`, secondUser)
	e.Exec(`INSERT INTO memberships(org_id,user_id,role) VALUES($1,$2,'developer')`, orgID, secondUser)
	e.Exec(`INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,now()+interval '1 hour')`, secondUser, e.Hasher.Sum("session", secondToken))
	secondCookie := &http.Cookie{Name: "hedvi_session", Value: secondToken}
	limited := false
	for i := range 40 {
		currentCookie := cookie
		if i >= 20 {
			currentCookie = secondCookie
		}
		status := cookieRequest(h, currentCookie, "GET", "/console/v1/projects/"+pid.String()+"/settings", nil)
		if status == 429 {
			limited = true
			break
		}
		if status != 200 {
			t.Fatal(status)
		}
	}
	if !limited {
		t.Fatal("organization requests were unbounded across different users")
	}
	h = authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock"}, ratelimit.NewMemory())
	limited = false
	for range 40 {
		if status := cookieRequest(h, cookie, "GET", "/console/v1/me", nil); status == 429 {
			limited = true
			break
		} else if status != 200 {
			t.Fatal(status)
		}
	}
	if !limited {
		t.Fatal("session-only routes lack user limit")
	}
}

func TestConsoleRejectsOversizePasswordsNULAndTrailingJSON(t *testing.T) {
	e := testenv.New(t)
	for _, body := range []string{
		`{"email":"a@example.test","password":"long-test-password-123","name":"bad\u0000name"}`,
		`{"email":"a@example.test","password":"long-test-password-123"} {}`,
	} {
		r := httptest.NewRequest("POST", "/console/v1/auth/signup", strings.NewReader(body))
		r.Header.Set("X-Hedvi-Console", "1")
		w := httptest.NewRecorder()
		e.Handler.ServeHTTP(w, r)
		if w.Code != 422 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if status, _, _ := authCall(t, e.Handler, nil, "/console/v1/auth/login", map[string]any{"email": "a@example.test", "password": strings.Repeat("a", 257)}); status != 401 {
		t.Fatal("oversize login", status)
	}
	if e.Count(`SELECT count(*) FROM users`) != 0 {
		t.Fatal("invalid input wrote a user")
	}
}
