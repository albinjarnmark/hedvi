package authmail

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

func TestBrandedAccountAlternatives(t *testing.T) {
	for _, locale := range []string{"en", "sv"} {
		for _, purpose := range []string{"signup", "reset", "existing"} {
			t.Run(locale+"/"+purpose, func(t *testing.T) {
				w := &Worker{From: "accounts@auth.example.com", AppURL: "https://app.example.com"}
				j := &job{Purpose: purpose, Existing: purpose == "existing", Data: payload{Email: "person@example.com", Locale: locale, Token: "fixture-only.proof"}}
				raw, err := w.render(j)
				if err != nil {
					t.Fatal(err)
				}
				if len(raw) > 100<<10 {
					t.Fatal("account message grew unexpectedly")
				}
				m, err := mail.ReadMessage(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				sender, err := mail.ParseAddress(m.Header.Get("From"))
				if err != nil || sender.Name != "Hedvi" || sender.Address != w.From {
					t.Fatal("missing branded sender")
				}
				if !bytes.Contains(bytes.ToLower(raw), []byte("content-id: <hedvi-logo>")) || !bytes.Contains(raw, []byte("inline; filename=hedvi.png")) {
					t.Fatal("missing inline brand image")
				}
				parts, err := ReadTextParts(raw)
				if err != nil {
					t.Fatal(err)
				}
				path := "/verify-email"
				if purpose == "reset" {
					path = "/reset-password"
				}
				if purpose == "existing" {
					path = "/forgot-password"
				}
				for _, kind := range []string{"text/plain", "text/html"} {
					body := parts[kind]
					if !strings.Contains(body, "https://app.example.com"+path+"?locale="+locale) {
						t.Fatalf("%s lost account action", kind)
					}
					hasToken := strings.Contains(body, "#token=fixture-only.proof")
					if hasToken == j.Existing {
						t.Fatalf("%s token exposure/omission", kind)
					}
					if j.Existing && strings.Contains(body, "30 minu") {
						t.Fatal("account notice claimed proof expiry")
					}
				}
				if !strings.Contains(parts["text/html"], `src="cid:hedvi-logo"`) || strings.Contains(parts["text/html"], `src="http`) {
					t.Fatal("logo must be inline, without external tracking")
				}
				for _, line := range bytes.Split(raw, []byte("\r\n")) {
					if len(line) > 998 {
						t.Fatal("MIME line exceeds transport limit")
					}
				}
			})
		}
	}
}
