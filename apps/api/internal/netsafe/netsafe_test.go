package netsafe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestAllowedIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.20.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1",
		"0.0.0.0", "::1", "fd00:ec2::254", "fe80::1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe", "fec0::1", "::ffff:0:0:a00:1", "::ffff:0:a00:1", "2001:10::1", "2001:20::1", "2001:2::1", "3fff::1", "224.0.0.1"} {
		if AllowedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if !AllowedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestValidateURL(t *testing.T) {
	for _, bad := range []string{"http://example.com/x", "https://127.0.0.1/x", "https://[::1]/", "https://169.254.169.254/latest/meta-data",
		"https://example.com:25/hook", "https://user:pw@example.com/", "https://localhost/hook", "https://metadata.google.internal/", "ftp://example.com", "/relative"} {
		if _, err := ValidateURL(bad, Options{}); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if _, err := ValidateURL("https://hooks.example.com/hedvi", Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientRefusesLoopbackAtDialTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), "POST", srv.URL, nil)
	_, err := NewClient(Options{}).Do(req)
	if err == nil || !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected blocked dial, got %v", err)
	}
	resp, err := NewClient(Options{AllowPrivate: true, AllowHTTP: true}).Do(req)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("local override failed: %v", err)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	req, _ := http.NewRequest("POST", redirect.URL, nil)
	resp, err := NewClient(Options{AllowPrivate: true, AllowHTTP: true}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("redirect was followed: %d", resp.StatusCode)
	}
}
