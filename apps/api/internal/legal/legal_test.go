package legal

import (
	"strings"
	"testing"
)

func validManifest() Manifest {
	m := Current()
	m.Adopted = true
	m.Version = Current().Version
	for i := range m.Documents {
		m.Documents[i].Version = "1.0"
		m.Documents[i].SHA256 = strings.Repeat("ab", 32)
	}
	return m
}
func TestAdoptedManifestFailsClosed(t *testing.T) {
	if err := validManifest().validate(true); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Manifest){
		func(m *Manifest) { m.Documents = nil }, func(m *Manifest) { m.Documents = m.Documents[:2] }, func(m *Manifest) { m.Documents[1] = m.Documents[0] }, func(m *Manifest) { m.Version = "" }, func(m *Manifest) { m.Version = "../x" }, func(m *Manifest) { m.Documents[0].SHA256 = "" }, func(m *Manifest) { m.Documents[0].Version = "" }, func(m *Manifest) { m.Documents[0].URL = "https://evil.example/terms" }, func(m *Manifest) { m.Documents[3].Acknowledgement = "consent" }, func(m *Manifest) { m.Adopted = false },
	} {
		m := validManifest()
		change(&m)
		if m.validate(true) == nil {
			t.Fatal("malformed manifest accepted", m)
		}
	}
}

func TestEmbeddedLegalArtifactsMatchReceiptDigests(t *testing.T) {
	if err := Current().validateEmbedded(); err != nil {
		t.Fatal(err)
	}
	m := Current()
	m.Documents[0].SHA256 = strings.Repeat("00", 32)
	if m.validateEmbedded() == nil {
		t.Fatal("content/digest mismatch accepted")
	}
}

func TestLegalOriginPresentationPreservesReceiptVersion(t *testing.T) {
	m := ForOrigin("https://hedvi.dev/")
	if m.Documents[0].URL != "https://hedvi.dev/legal/versions/"+m.Version+"/terms" {
		t.Fatal(m.Documents[0].URL)
	}
	if !strings.HasPrefix(Current().Documents[0].URL, "/legal/versions/") {
		t.Fatal("canonical receipt mutated")
	}
}
