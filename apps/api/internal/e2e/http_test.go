package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hedvi.dev/api/internal/testenv"
)

func call(t *testing.T, h http.Handler, method, path, key string, body any, hdr map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func TestResendCompatibleShapes(t *testing.T) {
	e := testenv.New(t)
	tn := e.NewTenant("free")
	code, out, _ := call(t, e.Handler, "POST", "/emails", tn.TestKey, map[string]any{
		"from": "Acme <onboarding@hedvi.dev>", "to": "ada@example.com", "subject": "Hello", "html": "<p>Hi</p>",
		"tags": []map[string]string{{"name": "category", "value": "welcome"}}}, nil)
	if code != 200 || out["id"] == nil || len(out) != 1 {
		t.Fatalf("POST /emails = %d %v", code, out)
	}
	id := out["id"].(string)
	code, got, _ := call(t, e.Handler, "GET", "/emails/"+id, tn.TestKey, nil, nil)
	if code != 200 {
		t.Fatal(code, got)
	}
	for _, k := range []string{"object", "id", "to", "from", "created_at", "subject", "html", "text", "bcc", "cc", "reply_to", "last_event", "scheduled_at", "tags"} {
		if _, ok := got[k]; !ok {
			t.Errorf("GET /emails/{id} lacks %q", k)
		}
	}
	if got["object"] != "email" || got["html"] != "<p>Hi</p>" || got["last_event"] != "test_captured" {
		t.Fatalf("unexpected body %v", got)
	}
	code, list, _ := call(t, e.Handler, "GET", "/emails?limit=1", tn.TestKey, nil, nil)
	if code != 200 || list["object"] != "list" || len(list["data"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, list)
	}
	// Errors follow Resend's {statusCode, name, message}.
	code, er, _ := call(t, e.Handler, "POST", "/emails", tn.TestKey, map[string]any{"from": "a@b.se", "to": "c@d.se", "subject": "s", "text": "t", "react": "x"}, nil)
	if code != 422 || er["name"] != "validation_error" || er["statusCode"].(float64) != 422 || !strings.Contains(er["message"].(string), "react") {
		t.Fatalf("unknown field error = %d %v", code, er)
	}
	if code, er, _ = call(t, e.Handler, "POST", "/emails", "", map[string]any{}, nil); code != 401 || er["name"] != "missing_api_key" {
		t.Fatalf("missing key = %d %v", code, er)
	}
	if code, er, _ = call(t, e.Handler, "GET", "/emails/"+id, "re_123", nil, nil); code != 403 || er["name"] != "invalid_api_key" {
		t.Fatalf("invalid key = %d %v", code, er)
	}
	// Another tenant cannot see the email.
	other := e.NewTenant("free")
	if code, _, _ = call(t, e.Handler, "GET", "/emails/"+id, other.TestKey, nil, nil); code != 404 {
		t.Fatalf("cross-tenant GET = %d", code)
	}
	// Revoked keys stop working immediately.
	e.Exec(`UPDATE api_keys SET revoked_at = now() WHERE id = $1`, tn.Test.KeyID)
	if code, _, _ = call(t, e.Handler, "GET", "/emails/"+id, tn.TestKey, nil, nil); code != 403 {
		t.Fatalf("revoked key = %d", code)
	}
	code, _, h := call(t, e.Handler, "POST", "/emails", tn.LiveKey, map[string]any{"from": "a@" + tn.Domain, "to": "x@example.com", "subject": "s", "text": "t"},
		map[string]string{"Idempotency-Key": "abc"})
	if code != 200 {
		t.Fatal(code)
	}
	if _, _, h = call(t, e.Handler, "POST", "/emails", tn.LiveKey, map[string]any{"from": "a@" + tn.Domain, "to": "x@example.com", "subject": "s", "text": "t"},
		map[string]string{"Idempotency-Key": "abc"}); h.Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay header missing")
	}
}

func TestConsoleSignupIsolationAndCSRF(t *testing.T) {
	e := testenv.New(t)
	h := e.Handler
	csrf := map[string]string{"X-Hedvi-Console": "1"}
	signup := func(email string) (*http.Cookie, map[string]any) {
		_, out, headers := signupConsole(t, e, email, "a-long-enough-password")
		return (&http.Response{Header: headers}).Cookies()[0], out
	}
	withCookie := func(c *http.Cookie, method, path string, body any, hdr map[string]string) (int, map[string]any) {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		req.AddCookie(c)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ca, a := signup("a@kund.se")
	cb, b := signup("b@kund.se")
	pa := a["project"].(map[string]any)["id"].(string)
	pb := b["project"].(map[string]any)["id"].(string)
	if !strings.HasPrefix(a["test_key"].(map[string]any)["key"].(string), "hv_test_") {
		t.Fatal("signup must return a test key")
	}
	if code, _ := withCookie(ca, "POST", "/console/v1/projects/"+pa+"/test-inbox/sample", nil, nil); code != 403 {
		t.Fatalf("missing CSRF header accepted: %d", code)
	}
	if code, _ := withCookie(ca, "POST", "/console/v1/projects/"+pa+"/test-inbox/sample", nil,
		map[string]string{"X-Hedvi-Console": "1", "Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("cross-origin request accepted: %d", code)
	}
	if code, out := withCookie(ca, "POST", "/console/v1/projects/"+pa+"/test-inbox/sample", nil, csrf); code != 201 {
		t.Fatalf("sample = %d %v", code, out)
	}
	if code, inbox := withCookie(ca, "GET", "/console/v1/projects/"+pa+"/test-inbox", nil, nil); code != 200 || len(inbox["data"].([]any)) != 1 {
		t.Fatalf("inbox = %d %v", code, inbox)
	}
	for _, path := range []string{"/overview", "/test-inbox", "/emails", "/api-keys", "/webhooks", "/settings"} {
		if code, _ := withCookie(cb, "GET", "/console/v1/projects/"+pa+path, nil, nil); code != 404 {
			t.Fatalf("user B reached A's %s: %d", path, code)
		}
	}
	if code, _ := withCookie(cb, "GET", "/console/v1/projects/"+pb+"/overview", nil, nil); code != 200 {
		t.Fatal("own project not reachable")
	}
	// Webhook URLs are validated against SSRF on creation.
	if code, out := withCookie(ca, "POST", "/console/v1/projects/"+pa+"/webhooks", map[string]any{"url": "ftp://example.com"}, csrf); code != 422 {
		t.Fatalf("bad webhook URL accepted: %d %v", code, out)
	}
	if code, _ := withCookie(ca, "GET", "/console/v1/admin/reviews", nil, nil); code != 404 {
		t.Fatal("admin reachable by non-staff")
	}
}
