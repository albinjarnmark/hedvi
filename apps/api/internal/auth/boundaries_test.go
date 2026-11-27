package auth

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestClientIPTrustBoundary(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("100.64.0.2/32")}
	for _, tc := range []struct{ peer, xff, want string }{
		{"198.51.100.4:4000", "1.1.1.1", "198.51.100.4"},
		{"10.1.1.2:4000", "1.1.1.1, 198.51.100.4, 10.1.1.3", "198.51.100.4"},
		{"[::ffff:100.64.0.2]:1234", "2001:db8::5", "2001:db8::5"},
		{"10.1.1.2:4000", "198.51.100.4, garbage", "10.1.1.2"},
		{"10.1.1.2:4000", "", "10.1.1.2"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		got := ClientIP(r, trusted)
		if got == nil || got.String() != tc.want {
			t.Fatalf("peer=%s XFF=%s got=%v want=%s", tc.peer, tc.xff, got, tc.want)
		}
	}
}

func TestProxyAttestationCannotBeForgedOrReplayedOnOtherRoute(t *testing.T) {
	key := bytes.Repeat([]byte{8}, 32)
	now := time.Now()
	r := httptest.NewRequest("POST", "/console/v1/auth/login?x=1", nil)
	r.Header.Set("X-Hedvi-Client-IP", "198.51.100.45")
	r.Header.Set("X-Hedvi-Proxy-Time", strconv.FormatInt(now.Unix(), 10))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(r.Header.Get("X-Hedvi-Proxy-Time") + "\nPOST\n/console/v1/auth/login\n198.51.100.45"))
	r.Header.Set("X-Hedvi-Proxy-Signature", hex.EncodeToString(mac.Sum(nil)))
	if got := AttestedClientIP(r, key, now); got == nil || got.String() != "198.51.100.45" {
		t.Fatal(got)
	}
	for _, tc := range []string{"ip", "path", "method", "signature", "expired", "future", "duplicate"} {
		t.Run(tc, func(t *testing.T) {
			copy := r.Clone(context.Background())
			at := now
			switch tc {
			case "ip":
				copy.Header.Set("X-Hedvi-Client-IP", "1.1.1.1")
			case "path":
				copy.URL.Path = "/console/v1/auth/signup"
			case "method":
				copy.Method = "GET"
			case "signature":
				copy.Header.Set("X-Hedvi-Proxy-Signature", strings.Repeat("0", 64))
			case "expired":
				at = now.Add(31 * time.Second)
			case "future":
				at = now.Add(-31 * time.Second)
			case "duplicate":
				copy.Header.Add("X-Hedvi-Client-IP", "198.51.100.45")
			}
			if got := AttestedClientIP(copy, key, at); got != nil {
				t.Fatalf("accepted %s: %v", tc, got)
			}
		})
	}
}

func TestPasswordGateBoundsAllHashingPaths(t *testing.T) {
	first, err := passwordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := passwordSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := HashPasswordContext(ctx, "correct horse battery staple"); !errors.Is(err, ErrPasswordBusy) {
		t.Fatal(err)
	}
	if err := DummyCheckContext(ctx, "test"); !errors.Is(err, ErrPasswordBusy) {
		t.Fatal(err)
	}
	encoded := "$argon2id$v=19$m=65536,t=2,p=1$c2l4dGVlbi1ieXRlLXNhbA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := CheckPasswordContext(ctx, encoded, "test"); !errors.Is(err, ErrPasswordBusy) {
		t.Fatal(err)
	}
}

func TestCorruptPasswordHashCannotPanicOrAllocateUnboundedMemory(t *testing.T) {
	for _, hash := range []string{"$argon2id$v=19$m=65536,t=2,p=0$AA$AA", "$argon2id$v=20$m=65536,t=2,p=1$AA$AA", "$argon2id$v=19$m=4294967295,t=2,p=1$AA$AA", "$argon2id$v=19$m=65536,t=2,p=1$AA$"} {
		if ok, err := CheckPasswordContext(context.Background(), hash, "password"); ok || err != nil {
			t.Fatalf("accepted malformed hash: %v %v", ok, err)
		}
	}
}
