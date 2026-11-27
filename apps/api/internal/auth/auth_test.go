package auth

import (
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery staple") || CheckPassword(h, "wrong") {
		t.Fatal("password check failed")
	}
}

func TestTOTPRFC6238Vector(t *testing.T) {
	// RFC 6238 test secret "12345678901234567890", T=59 -> 94287082 (8 digits); 6 digits = 287082.
	secret := totpEncoding.EncodeToString([]byte("12345678901234567890"))
	if got := TOTPNow(secret, time.Unix(59, 0)); got != "287082" {
		t.Fatalf("got %s", got)
	}
	step, ok := VerifyTOTP(secret, "287082", time.Unix(59, 0), 0)
	if !ok {
		t.Fatal("valid code rejected")
	}
	if _, ok := VerifyTOTP(secret, "287082", time.Unix(59, 0), step); ok {
		t.Fatal("replayed code accepted")
	}
}
