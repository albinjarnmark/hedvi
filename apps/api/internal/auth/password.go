// Package auth implements console authentication primitives: argon2id
// password hashing and RFC 6238 TOTP for multi-factor authentication.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommendation: m=64 MiB, t=2, p=1 or stronger).
const (
	argonTime      = 2
	argonMemory    = 64 * 1024
	argonThreads   = 1
	argonKeyLen    = 32
	MinPasswordLen = 12
	MaxPasswordLen = 256
)

// All hashing paths share this process-wide limit, including dummy checks.
// At most 128 MiB of Argon2 working memory is active at a time.
var passwordSlots = make(chan struct{}, 2)
var ErrPasswordBusy = errors.New("password verification capacity unavailable")

func passwordSlot(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	select {
	case passwordSlots <- struct{}{}:
		return func() { <-passwordSlots }, nil
	case <-ctx.Done():
		return nil, ErrPasswordBusy
	}
}

func HashPassword(password string) (string, error) {
	return HashPasswordContext(context.Background(), password)
}

func HashPasswordContext(ctx context.Context, password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	release, err := passwordSlot(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func CheckPassword(encoded, password string) bool {
	ok, _ := CheckPasswordContext(context.Background(), encoded, password)
	return ok
}

func CheckPasswordContext(ctx context.Context, encoded, password string) (bool, error) {
	if len(password) > MaxPasswordLen {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, nil
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, nil
	}
	// Current stored hashes use this profile. Reject corrupt/hostile parameter
	// values instead of panicking or allocating more memory than the gate budgets.
	if m != argonMemory || t != argonTime || p != argonThreads {
		return false, nil
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) != 16 || len(want) != argonKeyLen {
		return false, nil
	}
	release, err := passwordSlot(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// DummyCheck spends comparable time for unknown users to blunt user
// enumeration by timing.
func DummyCheck(password string) {
	_ = DummyCheckContext(context.Background(), password)
}

func DummyCheckContext(ctx context.Context, password string) error {
	if len(password) > MaxPasswordLen {
		return nil
	}
	release, err := passwordSlot(ctx)
	if err != nil {
		return err
	}
	defer release()
	argon2.IDKey([]byte(password), []byte("hedvi-dummy-salt"), argonTime, argonMemory, argonThreads, argonKeyLen)
	return nil
}

func ValidatePassword(pw string) error {
	if len(pw) < MinPasswordLen {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	if len(pw) > MaxPasswordLen {
		return errors.New("password is too long")
	}
	return nil
}

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a 160-bit base32 secret.
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return totpEncoding.EncodeToString(b)
}

func TOTPURL(secret, account string) string {
	v := url.Values{"secret": {secret}, "issuer": {"Hedvi"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}
	return "otpauth://totp/" + url.PathEscape("Hedvi:"+account) + "?" + v.Encode()
}

func totpAt(secret string, step int64) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1_000_000
	return fmt.Sprintf("%06d", code), nil
}

// VerifyTOTP accepts the current step ±1. It returns the matched step, which
// callers persist and require to increase, preventing code replay.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / 30
	for _, s := range []int64{step - 1, step, step + 1} {
		if s <= lastStep {
			continue
		}
		want, err := totpAt(secret, s)
		if err == nil && subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return s, true
		}
	}
	return 0, false
}

// TOTPNow is used by tests and the local seed command.
func TOTPNow(secret string, now time.Time) string {
	c, _ := totpAt(secret, now.Unix()/30)
	return c
}
