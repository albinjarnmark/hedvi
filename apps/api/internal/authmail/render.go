package authmail

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
)

//go:embed templates/account.html
var accountHTML string

//go:embed templates/hedvi-logo.png
var accountLogo []byte

var accountTemplate = template.Must(template.New("account-mail").Parse(accountHTML))

type accountCopy struct {
	Locale, Subject, Preheader, Eyebrow, Heading, Intro, Action, Expires, Fallback, Ignore, URL string
}

func (w *Worker) render(j *job) ([]byte, error) {
	u, err := url.Parse(w.AppURL)
	if err != nil {
		return nil, err
	}
	c := accountCopy{Locale: "en", Subject: "Finish creating your Hedvi account", Eyebrow: "Your Hedvi account", Heading: "Welcome to Hedvi.",
		Intro: "Verify your email address, choose your password, and start building with Hedvi.", Action: "Create your account",
		Expires: "This secure link expires in 30 minutes and can only be used once.", Fallback: "Button not working? Copy and paste this link into your browser:",
		Ignore: "If you did not request this email, you can safely ignore it."}
	u.Path = "/verify-email"
	if j.Purpose == "reset" {
		u.Path = "/reset-password"
		c.Subject, c.Heading, c.Action = "Reset your Hedvi password", "A fresh start.", "Reset password"
		c.Intro = "Choose a new password for your Hedvi account. Two-factor authentication stays enabled if you have set it up."
	}
	if j.Existing {
		u.Path = "/forgot-password"
		c.Subject, c.Heading, c.Action = "Your Hedvi account", "You're already with us.", "Recover your account"
		c.Intro, c.Expires = "An account already uses this email address. Sign in with your existing password, or request a secure password reset.", ""
	}
	if j.Data.Locale == "sv" {
		c.Locale, c.Subject, c.Eyebrow, c.Heading = "sv", "Slutför ditt Hedvi-konto", "Ditt Hedvi-konto", "Välkommen till Hedvi."
		c.Intro, c.Action = "Verifiera din e-postadress, välj ditt lösenord och kom igång med Hedvi.", "Skapa ditt konto"
		c.Expires, c.Fallback = "Den säkra länken gäller i 30 minuter och kan bara användas en gång.", "Fungerar inte knappen? Kopiera länken och klistra in den i webbläsaren:"
		c.Ignore = "Om du inte begärde det här mailet kan du ignorera det."
		if j.Purpose == "reset" {
			c.Subject, c.Heading, c.Action = "Återställ ditt Hedvi-lösenord", "En ny start.", "Återställ lösenord"
			c.Intro = "Välj ett nytt lösenord för ditt Hedvi-konto. Tvåstegsverifieringen är fortsatt aktiverad om du har ställt in den."
		}
		if j.Existing {
			c.Subject, c.Heading, c.Action = "Ditt Hedvi-konto", "Du är redan med oss.", "Återställ kontot"
			c.Intro, c.Expires = "Ett konto använder redan den här e-postadressen. Logga in med ditt befintliga lösenord eller begär en säker lösenordsåterställning.", ""
		}
	}
	u.RawPath, u.RawQuery, u.Fragment = "", "locale="+c.Locale, "token="+j.Data.Token
	if j.Existing {
		u.Fragment = ""
	}
	c.URL, c.Preheader = u.String(), c.Intro
	plain := c.Heading + "\n\n" + c.Intro + "\n\n" + c.Action + ":\n" + c.URL + "\n\n"
	if c.Expires != "" {
		plain += c.Expires + "\n\n"
	}
	plain += c.Ignore + "\nHedvi\n"
	var html bytes.Buffer
	if err := accountTemplate.Execute(&html, c); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	related := multipart.NewWriter(&out)
	from := (&mail.Address{Name: "Hedvi", Address: w.From}).String()
	to := (&mail.Address{Address: j.Data.Email}).String()
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=%q\r\n\r\n", from, to, mime.QEncoding.Encode("utf-8", c.Subject), related.Boundary())
	var alternatives bytes.Buffer
	alt := multipart.NewWriter(&alternatives)
	for _, part := range []struct{ kind, body string }{{"text/plain", plain}, {"text/html", html.String()}} {
		p, err := alt.CreatePart(textproto.MIMEHeader{"Content-Type": {part.kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return nil, err
		}
		q := quotedprintable.NewWriter(p)
		if _, err := q.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err := q.Close(); err != nil {
			return nil, err
		}
	}
	if err := alt.Close(); err != nil {
		return nil, err
	}
	part, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": alt.Boundary()})}})
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(alternatives.Bytes()); err != nil {
		return nil, err
	}
	logo, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {"image/png"}, "Content-ID": {"<hedvi-logo>"}, "Content-Disposition": {"inline; filename=hedvi.png"}, "Content-Transfer-Encoding": {"base64"}})
	if err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(accountLogo)
	for len(encoded) > 0 {
		n := min(76, len(encoded))
		if _, err := fmt.Fprintf(logo, "%s\r\n", encoded[:n]); err != nil {
			return nil, err
		}
		encoded = encoded[n:]
	}
	if err := related.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
