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
	Locale, Subject, Preheader, Eyebrow, Heading, Intro, Action, Expires, Fallback, Ignore, URL, LinkLabel string
}

func (w *Worker) render(j *job) ([]byte, error) {
	u, err := url.Parse(w.AppURL)
	if err != nil {
		return nil, err
	}
	c := accountCopy{Locale: "en", Subject: "Finish creating your Hedvi account", Eyebrow: "Your Hedvi account", Heading: "Welcome to Hedvi.",
		Intro: "Verify your email address, choose your password, and start building with Hedvi.", Action: "Create your account",
		Expires: "This secure link expires in 30 minutes and can only be used once.", Fallback: "If the button doesn't work, open this link:",
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
	u.RawPath, u.RawQuery, u.Fragment = "", "locale="+c.Locale, "token="+j.Data.Token
	if j.Existing {
		u.Fragment = ""
	}
	c.URL, c.Preheader = u.String(), c.Intro
	c.LinkLabel = u.Host + u.Path
	return w.renderCopy(c, j.Data.Email)
}

func (w *Worker) renderCopy(c accountCopy, recipient string) ([]byte, error) {
	plain := c.Heading + "\n\n" + c.Intro + "\n\n" + c.Action + ":\n" + c.URL + "\n\n"
	if c.Expires != "" {
		plain += c.Expires + "\n\n"
	}
	plain += c.Ignore + "\nHedvi\n"
	var html bytes.Buffer
	if err := accountTemplate.Execute(&html, c); err != nil {
		return nil, err
	}
	// The outer alternatives let clients choose plaintext or the complete
	// related HTML package. Use an RFC 2392 message-ID-shaped image CID.
	var relatedBody bytes.Buffer
	related := multipart.NewWriter(&relatedBody)
	writeText := func(writer *multipart.Writer, kind, body string) error {
		p, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {kind + "; charset=utf-8"}, "Content-Transfer-Encoding": {"quoted-printable"}})
		if err != nil {
			return err
		}
		q := quotedprintable.NewWriter(p)
		if _, err := q.Write([]byte(body)); err != nil {
			return err
		}
		return q.Close()
	}
	if err := writeText(related, "text/html", html.String()); err != nil {
		return nil, err
	}
	logo, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {"image/png; name=hedvi.png"}, "Content-ID": {"<hedvi-brand@inline.hedvi.dev>"}, "Content-Disposition": {"inline; filename=hedvi.png"}, "Content-Transfer-Encoding": {"base64"}})
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
	var out bytes.Buffer
	alt := multipart.NewWriter(&out)
	from := (&mail.Address{Name: "Hedvi", Address: w.From}).String()
	to := (&mail.Address{Address: recipient}).String()
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", from, to, mime.QEncoding.Encode("utf-8", c.Subject), alt.Boundary())
	if err := writeText(alt, "text/plain", plain); err != nil {
		return nil, err
	}
	part, err := alt.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType("multipart/related", map[string]string{"boundary": related.Boundary(), "type": "text/html"})}})
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(relatedBody.Bytes()); err != nil {
		return nil, err
	}
	if err := alt.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
