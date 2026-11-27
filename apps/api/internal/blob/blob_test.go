package blob

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"hedvi.dev/api/internal/secure"
)

func TestEncryptedFSRoundTripAndTamperResistance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	sealer, _ := secure.NewSealer(bytes.Repeat([]byte{1}, 32))
	store := Encrypted{Inner: FS{Root: root}, Sealer: sealer}
	orgA, orgB, id := uuid.New(), uuid.New(), uuid.New()
	keyA, keyB := PayloadKey(orgA, id), PayloadKey(orgB, id)

	if err := store.Put(ctx, keyA, []byte("secret payload")); err != nil {
		t.Fatal(err)
	}
	raw, _ := FS{Root: root}.Get(ctx, keyA)
	if bytes.Contains(raw, []byte("secret payload")) {
		t.Fatal("payload stored in plaintext")
	}
	got, err := store.Get(ctx, keyA)
	if err != nil || string(got) != "secret payload" {
		t.Fatalf("get: %q %v", got, err)
	}
	// Copying a ciphertext to another tenant's key must not decrypt.
	_ = FS{Root: root}.Put(ctx, keyB, raw)
	if _, err := store.Get(ctx, keyB); err == nil {
		t.Fatal("ciphertext decrypted under another tenant key")
	}
	if org, err := OrgOfKey(keyA); err != nil || org != orgA {
		t.Fatalf("OrgOfKey = %v %v", org, err)
	}
	var listed []string
	_ = store.List(ctx, PayloadPrefix, time.Now().Add(time.Minute), func(k string) error {
		listed = append(listed, k)
		return nil
	})
	if len(listed) != 2 {
		t.Fatalf("listed %v", listed)
	}
	if err := store.Delete(ctx, keyA); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, keyA); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
	if err := store.Put(ctx, "../etc/passwd", nil); err == nil {
		t.Fatal("accepted traversal key")
	}
}

func TestPayloadKeyRejectsMissingPrefixAndCleansFailedWrites(t *testing.T) {
	org, id := uuid.New(), uuid.New()
	for _, bad := range []string{org.String() + "/" + id.String(), PayloadPrefix + org.String() + "/junk", PayloadPrefix + org.String() + "/" + id.String() + "/extra"} {
		if _, err := OrgOfKey(bad); err == nil {
			t.Fatal("accepted invalid payload key")
		}
	}
	fs := FS{Root: t.TempDir()}
	if err := os.MkdirAll(fs.path("key"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(t.Context(), "key", []byte("payload")); err == nil {
		t.Fatal("expected rename to existing directory to fail")
	}
	entries, err := os.ReadDir(fs.Root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("orphaned temporary write: %v, %v", entries, err)
	}
}

func TestStoredPayloadReadBound(t *testing.T) {
	const limit int64 = 5
	short := int64(1)
	large := int64(10)
	for _, length := range []*int64{nil, &short, &large} {
		if _, err := readStoredPayload(bytes.NewBufferString("abcdef"), length, limit); err == nil {
			t.Fatal("accepted oversized object")
		}
	}
	got, err := readStoredPayload(bytes.NewBufferString("abcde"), nil, limit)
	if err != nil || string(got) != "abcde" {
		t.Fatalf("at limit: %q %v", got, err)
	}
}
