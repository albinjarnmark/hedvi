package webhooks

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

const secretPrefix = "whsec_"

// NewSecret returns a Standard Webhooks signing secret.
func NewSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(b)
}

func secretBytes(secret string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, secretPrefix))
}

// Sign returns the webhook-signature header value for id, timestamp and body.
func Sign(secret, id string, ts time.Time, body []byte) (string, error) {
	key, err := secretBytes(secret)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id + "." + strconv.FormatInt(ts.Unix(), 10) + "."))
	m.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(m.Sum(nil)), nil
}

// ReplayTolerance is the documented replay window for receivers.
const ReplayTolerance = 5 * time.Minute

var ErrInvalidSignature = errors.New("invalid webhook signature")

// Verify checks a signature header as a receiver would. It is used by tests
// and the documentation examples.
func Verify(secret, id, timestamp, signatures string, body []byte, now time.Time) error {
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	ts := time.Unix(sec, 0)
	if now.Sub(ts) > ReplayTolerance || ts.Sub(now) > ReplayTolerance {
		return errors.New("webhook timestamp outside tolerance")
	}
	want, err := Sign(secret, id, ts, body)
	if err != nil {
		return err
	}
	for _, s := range strings.Fields(signatures) {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return ErrInvalidSignature
}
