package message

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"strings"
	"time"
)

// BuildMIME renders an RFC 5322 message. Bcc recipients are never written to
// headers; they only appear in the SES envelope.
func BuildMIME(m *Message, date time.Time) ([]byte, error) {
	var buf bytes.Buffer
	h := func(name, value string) { fmt.Fprintf(&buf, "%s: %s\r\n", name, value) }
	h("From", m.From)
	if len(m.To) > 0 {
		h("To", foldList(m.To))
	}
	if len(m.Cc) > 0 {
		h("Cc", foldList(m.Cc))
	}
	if len(m.ReplyTo) > 0 {
		h("Reply-To", foldList(m.ReplyTo))
	}
	h("Subject", encodeHeaderValue(m.Subject))
	h("Date", date.UTC().Format(time.RFC1123Z))
	h("MIME-Version", "1.0")
	for _, ch := range m.Headers {
		h(textproto.CanonicalMIMEHeaderKey(ch.Name), encodeHeaderValue(ch.Value))
	}

	var inline, files []Attachment
	for _, a := range m.Attachments {
		if a.ContentID != "" {
			inline = append(inline, a)
		} else {
			files = append(files, a)
		}
	}

	body := func(w *multipart.Writer) error {
		if m.HTML != "" && m.Text != "" {
			alt, err := nested(w, "multipart/alternative")
			if err != nil {
				return err
			}
			if err := textPart(alt, "text/plain", m.Text); err != nil {
				return err
			}
			if err := htmlPart(alt, m.HTML, inline); err != nil {
				return err
			}
			return alt.Close()
		}
		if m.HTML != "" {
			return htmlPart(w, m.HTML, inline)
		}
		return textPart(w, "text/plain", m.Text)
	}

	if len(files) == 0 && len(inline) == 0 && (m.HTML == "" || m.Text == "") {
		// Single part message.
		ct, content := "text/plain", m.Text
		if m.HTML != "" {
			ct, content = "text/html", m.HTML
		}
		h("Content-Type", ct+"; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		buf.WriteString("\r\n")
		qp := quotedprintable.NewWriter(&buf)
		if _, err := qp.Write([]byte(crlf(content))); err != nil {
			return nil, err
		}
		if err := qp.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	var rootBody bytes.Buffer
	root := multipart.NewWriter(&rootBody)
	rootType := "multipart/mixed"
	switch {
	case len(files) > 0:
		if err := body(root); err != nil {
			return nil, err
		}
		for _, a := range files {
			if err := attachmentPart(root, a, "attachment"); err != nil {
				return nil, err
			}
		}
	case m.HTML != "" && m.Text != "":
		rootType = "multipart/alternative"
		if err := textPart(root, "text/plain", m.Text); err != nil {
			return nil, err
		}
		if err := htmlPart(root, m.HTML, inline); err != nil {
			return nil, err
		}
	default:
		rootType = "multipart/related"
		if err := textOrHTMLWithInline(root, m, inline); err != nil {
			return nil, err
		}
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	h("Content-Type", fmt.Sprintf("%s; boundary=%q", rootType, root.Boundary()))
	buf.WriteString("\r\n")
	buf.Write(rootBody.Bytes())
	return buf.Bytes(), nil
}

func textOrHTMLWithInline(w *multipart.Writer, m *Message, inline []Attachment) error {
	if m.HTML != "" {
		if err := textPart(w, "text/html", m.HTML); err != nil {
			return err
		}
	} else if err := textPart(w, "text/plain", m.Text); err != nil {
		return err
	}
	for _, a := range inline {
		if err := attachmentPart(w, a, "inline"); err != nil {
			return err
		}
	}
	return nil
}

func nested(parent *multipart.Writer, ct string) (*multipart.Writer, error) {
	boundary := multipart.NewWriter(io.Discard).Boundary()
	pw, err := parent.CreatePart(textproto.MIMEHeader{"Content-Type": {fmt.Sprintf("%s; boundary=%q", ct, boundary)}})
	if err != nil {
		return nil, err
	}
	child := multipart.NewWriter(pw)
	return child, child.SetBoundary(boundary)
}

func htmlPart(w *multipart.Writer, html string, inline []Attachment) error {
	if len(inline) == 0 {
		return textPart(w, "text/html", html)
	}
	rel, err := nested(w, "multipart/related")
	if err != nil {
		return err
	}
	if err := textPart(rel, "text/html", html); err != nil {
		return err
	}
	for _, a := range inline {
		if err := attachmentPart(rel, a, "inline"); err != nil {
			return err
		}
	}
	return rel.Close()
}

func textPart(w *multipart.Writer, ct, content string) error {
	pw, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {ct + "; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(pw)
	if _, err := qp.Write([]byte(crlf(content))); err != nil {
		return err
	}
	return qp.Close()
}

func attachmentPart(w *multipart.Writer, a Attachment, disposition string) error {
	mediaType, _, err := mime.ParseMediaType(a.ContentType)
	if err != nil {
		return err
	}
	contentType := mime.FormatMediaType(mediaType, map[string]string{"name": a.Filename})
	if contentType == "" {
		return fmt.Errorf("invalid attachment media type")
	}
	hdr := textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Disposition":       {mime.FormatMediaType(disposition, map[string]string{"filename": a.Filename})},
	}
	if a.ContentID != "" {
		hdr.Set("Content-Id", "<"+a.ContentID+">")
	}
	pw, err := w.CreatePart(hdr)
	if err != nil {
		return err
	}
	enc := base64.StdEncoding.EncodeToString(a.Data)
	for len(enc) > 76 {
		if _, err := pw.Write([]byte(enc[:76] + "\r\n")); err != nil {
			return err
		}
		enc = enc[76:]
	}
	_, err = pw.Write([]byte(enc + "\r\n"))
	return err
}

func foldList(list []string) string { return strings.Join(list, ",\r\n ") }

func encodeHeaderValue(v string) string {
	for _, r := range v {
		if r > 127 {
			return strings.ReplaceAll(mime.QEncoding.Encode("utf-8", v), "?= =?", "?=\r\n =?")
		}
	}
	return v
}

func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
