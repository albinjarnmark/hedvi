// Package secure provides authenticated encryption, keyed hashing and random
// token helpers. Nothing here logs secrets.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
)

const sealVersion byte = 1

// Sealer encrypts small values and blobs with AES-256-GCM. The associated
// data binds a ciphertext to its purpose, e.g. an object key that contains the
// organization id, so ciphertexts cannot be swapped between tenants.
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) < 32 {
		return nil, errors.New("data key must be at least 32 bytes")
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plaintext, associated []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+s.aead.Overhead())
	out = append(out, sealVersion)
	out = append(out, nonce...)
	return s.aead.Seal(out, nonce, plaintext, associated), nil
}

func (s *Sealer) Open(sealed, associated []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(sealed) < 1+ns+s.aead.Overhead() {
		return nil, errors.New("sealed value too short")
	}
	if sealed[0] != sealVersion {
		return nil, fmt.Errorf("unsupported seal version %d", sealed[0])
	}
	return s.aead.Open(nil, sealed[1:1+ns], sealed[1+ns:], associated)
}

// Hasher computes keyed hashes for high-entropy secrets (API keys, session
// tokens). A pepper kept outside the database means a leaked table alone
// cannot be used to verify guesses.
type Hasher struct{ pepper []byte }

func NewHasher(pepper []byte) *Hasher { return &Hasher{pepper: pepper} }

func (h *Hasher) Sum(purpose, value string) []byte {
	m := hmac.New(sha256.New, h.pepper)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(value))
	return m.Sum(nil)
}

func (h *Hasher) Equal(purpose, value string, expected []byte) bool {
	return subtle.ConstantTimeCompare(h.Sum(purpose, value), expected) == 1
}

var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// RandomToken returns n random bytes encoded as lowercase base32.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return b32.EncodeToString(b)
}

func SHA256(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}
