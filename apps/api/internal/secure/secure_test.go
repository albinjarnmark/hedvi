package secure

import (
	"bytes"
	"testing"
)

func TestSealerBindsAssociatedData(t *testing.T) {
	s, err := NewSealer(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := s.Seal([]byte("hello"), []byte("payloads/org-a/1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Open(sealed, []byte("payloads/org-a/1")); err != nil || string(got) != "hello" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := s.Open(sealed, []byte("payloads/org-b/1")); err == nil {
		t.Fatal("ciphertext opened under another tenant's key path")
	}
}

func TestHasherIsPurposeBound(t *testing.T) {
	h := NewHasher([]byte("pepper-pepper-pepper"))
	sum := h.Sum("api_key", "secret")
	if !h.Equal("api_key", "secret", sum) {
		t.Fatal("expected match")
	}
	if h.Equal("session", "secret", sum) {
		t.Fatal("hash must differ per purpose")
	}
}
