package keys

import (
	"strings"
	"testing"

	"hedvi.dev/api/internal/secure"
)

func TestGenerateParseVerify(t *testing.T) {
	h := secure.NewHasher([]byte("0123456789abcdef"))
	g := Generate(h, "test")
	if !strings.HasPrefix(g.Plaintext, "hv_test_") {
		t.Fatalf("unexpected prefix %q", g.Plaintext)
	}
	lookup, secret, err := Parse(g.Plaintext)
	if err != nil || lookup != g.LookupID {
		t.Fatalf("parse: %v %q", err, lookup)
	}
	if !Verify(h, secret, g.Hash) {
		t.Fatal("verify failed")
	}
	if Verify(h, secret+"x", g.Hash) {
		t.Fatal("verify accepted a wrong secret")
	}
	for _, bad := range []string{"", "re_123", "hv_test_abc_def", "hv_prod_" + lookup + "_" + secret} {
		if _, _, err := Parse(bad); err == nil {
			t.Fatalf("parse accepted %q", bad)
		}
	}
}
