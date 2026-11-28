package authmail

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
)

// ReadTextParts is for the local simulator tools and tests. It accepts only a
// bounded account message and never fetches remote content or inline resources.
func ReadTextParts(raw []byte) (map[string]string, error) {
	if len(raw) > 256<<10 {
		return nil, errors.New("account message exceeds limit")
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	count := 0
	var walk func(io.Reader, string, string, int) error
	walk = func(body io.Reader, contentType, encoding string, depth int) error {
		count++
		if depth > 8 || count > 32 {
			return errors.New("account MIME nesting exceeds limit")
		}
		if contentType == "" {
			contentType = "text/plain"
		}
		kind, params, err := mime.ParseMediaType(contentType)
		if err != nil {
			return err
		}
		if strings.HasPrefix(kind, "multipart/") {
			parts := multipart.NewReader(body, params["boundary"])
			for {
				part, err := parts.NextPart()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
				err = walk(part, part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), depth+1)
				part.Close()
				if err != nil {
					return err
				}
			}
		}
		if kind != "text/plain" && kind != "text/html" {
			return nil
		}
		if strings.EqualFold(encoding, "quoted-printable") {
			body = quotedprintable.NewReader(body)
		}
		value, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		out[kind] += string(value)
		return nil
	}
	if err := walk(m.Body, m.Header.Get("Content-Type"), m.Header.Get("Content-Transfer-Encoding"), 0); err != nil {
		return nil, err
	}
	if out["text/plain"] == "" {
		return nil, errors.New("account message has no plain text alternative")
	}
	return out, nil
}
