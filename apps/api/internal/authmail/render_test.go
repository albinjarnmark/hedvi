package authmail

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
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
				if !bytes.Contains(bytes.ToLower(raw), []byte("content-id: <hedvi-brand@inline.hedvi.dev>")) || !bytes.Contains(raw, []byte("inline; filename=hedvi.png")) {
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
					if !strings.Contains(body, "https://app.example.com"+path+"?locale=en") {
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
				if !strings.Contains(parts["text/html"], `src="cid:hedvi-brand@inline.hedvi.dev"`) || strings.Contains(parts["text/html"], `src="http`) {
					t.Fatal("logo must be inline, without external tracking")
				}
				if preview := os.Getenv("HEDVI_ACCOUNT_MAIL_PREVIEW_DIR"); preview != "" {
					if err := os.MkdirAll(preview, 0o755); err != nil {
						t.Fatal(err)
					}
					html := strings.ReplaceAll(parts["text/html"], "cid:hedvi-brand@inline.hedvi.dev", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(accountLogo))
					if err := os.WriteFile(filepath.Join(preview, locale+"-"+purpose+".html"), []byte(html), 0o644); err != nil {
						t.Fatal(err)
					}
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

func TestAccountLogoTravelsWithHTMLAndKeepsDarkContrast(t *testing.T) {
	w := &Worker{From: "accounts@auth.example.com", AppURL: "https://app.example.com"}
	raw, err := w.render(&job{Purpose: "signup", Data: payload{Email: "person@example.com", Token: "fixture.proof"}})
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	kind, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/alternative" {
		t.Fatal("outer body must offer plaintext and a complete HTML alternative")
	}
	alt := multipart.NewReader(m.Body, params["boundary"])
	plain, err := alt.NextPart()
	if err != nil || !strings.HasPrefix(plain.Header.Get("Content-Type"), "text/plain") {
		t.Fatal("first alternative must be plaintext")
	}
	related, err := alt.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	kind, params, err = mime.ParseMediaType(related.Header.Get("Content-Type"))
	if err != nil || kind != "multipart/related" || params["type"] != "text/html" {
		t.Fatal("HTML alternative must own its inline image")
	}
	parts := multipart.NewReader(related, params["boundary"])
	html, err := parts.NextPart()
	if err != nil || !strings.HasPrefix(html.Header.Get("Content-Type"), "text/html") {
		t.Fatal("related root must be HTML")
	}
	logo, err := parts.NextPart()
	if err != nil || logo.Header.Get("Content-ID") != "<hedvi-brand@inline.hedvi.dev>" {
		t.Fatal("inline logo reference mismatch")
	}
	image, err := png.Decode(base64.NewDecoder(base64.StdEncoding, logo))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, a := image.At(0, 0).RGBA()
	if a != 65535 || r > 0x2000 || g > 0x2000 || b > 0x2000 {
		t.Fatal("logo needs its own opaque dark background when Gmail recolors the surrounding message")
	}
	if _, err = parts.NextPart(); err != io.EOF {
		t.Fatal("unexpected related attachment")
	}
}
