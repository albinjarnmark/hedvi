package e2e

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hedvi.dev/api/internal/config"
	"hedvi.dev/api/internal/testenv"
)

func TestRememberMeIsOptInBoundedAndRevocable(t *testing.T) {
	e := testenv.New(t)
	_, uid, _, _ := signupForSecurity(t, e)
	h := authConsole(e, &config.Config{Env: config.EnvTest, AWSMode: "mock", CookieSecure: true}, permissiveAuthLimiter{})
	login := func(remember bool) *http.Cookie {
		t.Helper()
		status, out, headers := authCall(t, h, nil, "/console/v1/auth/login", map[string]any{"email": "security@example.test", "password": "long-test-password-123", "remember_me": remember})
		if status != 200 {
			t.Fatal(status, out)
		}
		c := (&http.Response{Header: headers}).Cookies()[0]
		if c.Name != "__Host-hedvi_session" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
			t.Fatal("cookie boundary", c.Name)
		}
		return c
	}
	me := func(c *http.Cookie) int {
		r := httptest.NewRequest("GET", "/console/v1/me", nil)
		r.AddCookie(c)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	ordinary := login(false)
	if ordinary.MaxAge != 0 || !ordinary.Expires.IsZero() {
		t.Fatal("standard login persisted without opt-in")
	}
	remembered := login(true)
	if remembered.MaxAge != 30*24*60*60 || time.Until(remembered.Expires) < 29*24*time.Hour {
		t.Fatal("remembered cookie does not last 30 days")
	}
	if e.Count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND remembered AND expires_at BETWEEN now()+interval '29 days' AND now()+interval '30 days'`, uid) != 1 {
		t.Fatal("server expiry mismatches cookie")
	}
	e.Exec(`UPDATE sessions SET last_seen_at=now()-interval '4 days' WHERE user_id=$1`, uid)
	if me(ordinary) != 401 || me(remembered) != 200 {
		t.Fatal("remembered login did not survive normal inactivity boundary")
	}
	if status, _, _ := authCall(t, h, remembered, "/console/v1/auth/logout", map[string]any{}); status != 200 {
		t.Fatal("logout", status)
	}
	if me(remembered) != 401 {
		t.Fatal("copied cookie survived logout")
	}
	expired := login(true)
	e.Exec(`UPDATE sessions SET created_at=now()-interval '31 days',expires_at=now()-interval '1 second',last_seen_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, uid)
	if me(expired) != 401 {
		t.Fatal("activity extended absolute session deadline")
	}
}
