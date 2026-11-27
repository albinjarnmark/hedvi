package message

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func mustValidate(t *testing.T, body string) *Message {
	t.Helper()
	req, err := Decode([]byte(body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	m, err := req.Validate(DefaultLimits)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return m
}

func validationName(t *testing.T, body string) string {
	t.Helper()
	req, err := Decode([]byte(body))
	if err == nil {
		_, err = req.Validate(DefaultLimits)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected validation error for %s, got %v", body, err)
	}
	return ve.Name
}

func TestRejectsUnknownAndUnsupportedFields(t *testing.T) {
	base := `"from":"a@x.se","to":"b@y.se","subject":"s","text":"t"`
	for body, want := range map[string]string{
		`{` + base + `,"react":"<div/>"}`:                                                        "validation_error",
		`{` + base + `,"scheduled_at":"in 1 hour"}`:                                              "validation_error",
		`{` + base + `,"template":{"id":"x"}}`:                                                   "validation_error",
		`{"to":"b@y.se","subject":"s","text":"t"}`:                                               "missing_required_field",
		`{"from":"nope","to":"b@y.se","subject":"s","text":"t"}`:                                 "invalid_from_address",
		`{"from":"a@x.se","to":"b@y.se","text":"t"}`:                                             "missing_required_field",
		`{"from":"a@x.se","to":"b@y.se","subject":"s"}`:                                          "missing_required_field",
		`{` + base + `,"attachments":[{"path":"https://169.254.169.254/x","filename":"a.pdf"}]}`: "invalid_attachment",
		`{` + base + `,"attachments":[{"content":"aGk=","filename":"run.exe"}]}`:                 "invalid_attachment",
		`{` + base + `,"headers":{"Bcc":"x@y.se"}}`:                                              "validation_error",
		`{` + base + `,"headers":{"X-Ok":"a\r\nBcc: x@y.se"}}`:                                   "validation_error",
		`{` + base + `,"tags":[{"name":"bad tag","value":"x"}]}`:                                 "validation_error",
	} {
		if got := validationName(t, body); got != want {
			t.Errorf("%s: got %s, want %s", body, got, want)
		}
	}
	// Explicit nulls from SDKs are fine.
	mustValidate(t, `{`+base+`,"scheduled_at":null,"template":null,"topic_id":null,"cc":null}`)
}

func TestMIMEHidesBccAndEncodesParts(t *testing.T) {
	att := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("PDF"), 100))
	m := mustValidate(t, `{"from":"Åsa <asa@example.se>","to":["a@x.se"],"cc":"c@x.se","bcc":["secret@x.se"],
		"reply_to":"r@x.se","subject":"Kvitto för order","html":"<p>Hej <img src=\"cid:logo\"></p>","text":"Hej",
		"headers":{"X-Entity-Ref-ID":"123","List-Unsubscribe":"<mailto:u@x.se>"},
		"attachments":[{"content":"`+att+`","filename":"kvitto.pdf"},{"content":"aGk=","filename":"logo.png","content_id":"logo"}]}`)
	if len(m.Recipients) != 3 {
		t.Fatalf("recipients %+v", m.Recipients)
	}
	raw, err := BuildMIME(m, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte("secret@x.se")) || bytes.Contains(raw, []byte("\nBcc:")) {
		t.Fatal("Bcc recipient leaked into the MIME message")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	if subj, _ := dec.DecodeHeader(msg.Header.Get("Subject")); subj != "Kvitto för order" {
		t.Fatalf("subject %q", subj)
	}
	if msg.Header.Get("X-Entity-Ref-Id") != "123" {
		t.Fatalf("custom header missing: %v", msg.Header)
	}
	mt, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if mt != "multipart/mixed" {
		t.Fatalf("root type %s", mt)
	}
	types := walk(t, msg.Body, params["boundary"])
	want := "multipart/alternative[text/plain,multipart/related[text/html,image/png]],application/pdf"
	if types != want {
		t.Fatalf("structure %s, want %s", types, want)
	}
}

func walk(t *testing.T, r io.Reader, boundary string) string {
	t.Helper()
	mr := multipart.NewReader(r, boundary)
	var parts []string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		mt, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if strings.HasPrefix(mt, "multipart/") {
			parts = append(parts, mt+"["+walk(t, p, params["boundary"])+"]")
			continue
		}
		if p.Header.Get("Content-Transfer-Encoding") == "base64" {
			b, _ := io.ReadAll(p)
			if _, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(b), "\r\n", "")); err != nil {
				t.Fatalf("bad base64 in %s", mt)
			}
		}
		parts = append(parts, mt)
	}
	return strings.Join(parts, ",")
}

func TestCanonicalRoundTrip(t *testing.T) {
	m := mustValidate(t, `{"from":"a@x.se","to":["b@y.se","B@y.se"],"bcc":"c@y.se","subject":"s","text":"t"}`)
	b, _ := m.Canonical()
	back, err := Load(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Recipients) != 2 || back.Recipients[1].Field != "bcc" {
		t.Fatalf("recipients %+v", back.Recipients)
	}
	b2, _ := back.Canonical()
	if !bytes.Equal(b, b2) {
		t.Fatal("canonical form is not stable")
	}
}

func TestSinglePartAndSizeLimit(t *testing.T) {
	m := mustValidate(t, `{"from":"a@x.se","to":"b@y.se","subject":"s","text":"line1\nline2"}`)
	raw, _ := BuildMIME(m, time.Now())
	if !bytes.Contains(raw, []byte("Content-Type: text/plain; charset=utf-8")) || !bytes.Contains(raw, []byte("line1\r\nline2")) {
		t.Fatalf("unexpected single part:\n%s", raw)
	}
	big := strings.Repeat("a", DefaultLimits.MaxPayloadBytes+1)
	if got := validationName(t, `{"from":"a@x.se","to":"b@y.se","subject":"s","text":"`+big+`"}`); got != "validation_error" {
		t.Fatal(got)
	}
}

func TestPublicInputBoundsAndReservedHeaders(t *testing.T) {
	base := `"from":"a@x.se","to":"b@y.se","subject":"s","text":"t"`
	for _, name := range []string{"x-SeS-MESSAGE-TAGS", "X-SeS-custom", "X-AmZ-extra", "X-Hedvi-test", "Resent-From", "Received-SPF", "Errors-To", "Content-ID", "Content-Description", "Content-Location", "Disposition-Notification-To"} {
		if got := validationName(t, `{`+base+`,"headers":{"`+name+`":"value"}}`); got != "validation_error" {
			t.Fatal(got)
		}
	}
	for _, suffix := range []string{
		`,"headers":{"X-Sample":"value\u001f"}`,
		`,"headers":{"X-Sample":"` + strings.Repeat("a", 990) + `"}`,
		`,"subject":"bad\u0000"`,
		`,"to":"=?utf-8?q?name=0Aport?= <a@x.se>"`,
		`,"from":"` + strings.Repeat("a", 129) + ` <a@x.se>"`,
		`,"reply_to":[` + strings.Repeat(`"r@x.se",`, 50) + `"r@x.se"]`,
		`,"attachments":[{"filename":"document.exe.","content":"aGk="}]`,
		`,"attachments":[{"filename":"document.txt","content_type":"broken","content":"aGk="}]`,
	} {
		validationName(t, "{"+base+suffix+"}")
	}
	for _, suffix := range []string{"}", "[]", "]"} {
		if _, err := Decode([]byte("{" + base + "}" + suffix)); err == nil {
			t.Fatal("trailing JSON accepted")
		}
	}
	m := mustValidate(t, `{`+base+`,"attachments":[{"filename":"résumé.txt","content_type":"text/plain ; charset=utf-8","content":"aGk="}]}`)
	raw, err := BuildMIME(m, time.Now())
	if err != nil || !bytes.Contains(raw, []byte("Content-Type: text/plain; name")) {
		t.Fatal("missing attachment media type", err)
	}
}
