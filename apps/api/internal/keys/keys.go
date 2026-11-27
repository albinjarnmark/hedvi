// Package keys generates and parses API keys.
//
// Format: hv_<mode>_<lookup>_<secret>. The mode in the prefix is a visual aid
// only; whether a key is test or live is always read from the database row.
package keys

import (
	"errors"
	"strings"

	"hedvi.dev/api/internal/secure"
)

const hashPurpose = "api_key"

type Generated struct {
	Plaintext string // shown to the user exactly once
	LookupID  string
	Hash      []byte
	Hint      string
}

func Generate(h *secure.Hasher, mode string) Generated {
	lookup := secure.RandomToken(10) // 16 chars
	secret := secure.RandomToken(25) // 40 chars, 200 bits
	return Generated{
		Plaintext: "hv_" + mode + "_" + lookup + "_" + secret,
		LookupID:  lookup,
		Hash:      h.Sum(hashPurpose, secret),
		Hint:      "hv_" + mode + "_" + lookup[:4] + "…" + secret[len(secret)-4:],
	}
}

var ErrMalformed = errors.New("malformed api key")

// Parse splits a presented key into its lookup id and secret.
func Parse(presented string) (lookupID, secret string, err error) {
	parts := strings.Split(presented, "_")
	if len(parts) != 4 || parts[0] != "hv" || (parts[1] != "test" && parts[1] != "live") ||
		len(parts[2]) != 16 || len(parts[3]) != 40 {
		return "", "", ErrMalformed
	}
	return parts[2], parts[3], nil
}

func Verify(h *secure.Hasher, secret string, hash []byte) bool {
	return h.Equal(hashPurpose, secret, hash)
}
