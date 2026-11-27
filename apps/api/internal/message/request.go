// Package message validates Resend-compatible send requests and builds MIME.
package message

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"hedvi.dev/api/internal/address"
)

// StringList accepts a JSON string or an array of strings, like Resend.
type StringList []string

func (l *StringList) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*l = nil
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*l = StringList{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(b, &arr); err != nil {
		return errors.New("must be a string or an array of strings")
	}
	*l = arr
	return nil
}

type AttachmentRequest struct {
	Content     *string `json:"content"`
	Filename    string  `json:"filename"`
	Path        *string `json:"path"`
	ContentType string  `json:"content_type"`
	ContentID   string  `json:"content_id"`
}

type Tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// SendRequest is the documented subset of Resend's POST /emails body.
// Unknown fields are rejected by the decoder, never silently ignored.
type SendRequest struct {
	From        string              `json:"from"`
	To          StringList          `json:"to"`
	Cc          StringList          `json:"cc"`
	Bcc         StringList          `json:"bcc"`
	ReplyTo     StringList          `json:"reply_to"`
	Subject     *string             `json:"subject"`
	HTML        *string             `json:"html"`
	Text        *string             `json:"text"`
	Headers     map[string]string   `json:"headers"`
	Attachments []AttachmentRequest `json:"attachments"`
	Tags        []Tag               `json:"tags"`
	// Known Resend fields that Hedvi does not support yet. They are accepted
	// only when empty so SDKs that send nulls keep working.
	ScheduledAt *string         `json:"scheduled_at"`
	Template    json.RawMessage `json:"template"`
	TopicID     *string         `json:"topic_id"`
}

// ValidationError maps to Resend's 422 validation_error.
type ValidationError struct {
	Name    string // Resend error name, e.g. validation_error, missing_required_field
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func invalid(format string, a ...any) error {
	return &ValidationError{Name: "validation_error", Message: fmt.Sprintf(format, a...)}
}

func missing(field string) error {
	return &ValidationError{Name: "missing_required_field", Message: fmt.Sprintf("Missing `%s` field.", field)}
}

const CompatibilityURL = "https://hedvi.dev/docs/compatibility"

// Decode parses a request body strictly.
func Decode(body []byte) (*SendRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req SendRequest
	if err := dec.Decode(&req); err != nil {
		var syntax *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		msg := err.Error()
		switch {
		case errors.As(err, &syntax):
			return nil, invalid("Request body is not valid JSON.")
		case errors.As(err, &typeErr):
			return nil, invalid("Field `%s` has the wrong type.", typeErr.Field)
		case strings.HasPrefix(msg, "json: unknown field "):
			field := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
			return nil, invalid("Field `%s` is not supported by Hedvi. See %s", field, CompatibilityURL)
		default:
			return nil, invalid("Invalid request body: %s", msg)
		}
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return nil, invalid("Request body must contain a single JSON object.")
	}
	return &req, nil
}

type Limits struct {
	MaxRecipients   int
	MaxPayloadBytes int // decoded html + text + attachments
	MaxAttachments  int
	MaxHeaders      int
	MaxTags         int
}

var DefaultLimits = Limits{MaxRecipients: 50, MaxPayloadBytes: 10 << 20, MaxAttachments: 10, MaxHeaders: 30, MaxTags: 10}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	ContentID   string `json:"content_id,omitempty"`
	Data        []byte `json:"data"`
}

// Message is a validated, canonical message. Its JSON form is the stored
// payload and the basis of the idempotency hash.
type Message struct {
	Version     int          `json:"v"`
	From        string       `json:"from"`
	To          []string     `json:"to"`
	Cc          []string     `json:"cc,omitempty"`
	Bcc         []string     `json:"bcc,omitempty"`
	ReplyTo     []string     `json:"reply_to,omitempty"`
	Subject     string       `json:"subject"`
	HTML        string       `json:"html,omitempty"`
	Text        string       `json:"text,omitempty"`
	Headers     []Header     `json:"headers,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	Tags        []Tag        `json:"tags,omitempty"`

	// Derived, not serialized.
	FromAddr   address.Address     `json:"-"`
	Recipients []address.Recipient `json:"-"`
}

var (
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	headerNamePattern = regexp.MustCompile(`^[!-9;-~]{1,76}$`)
	contentIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)
)

// Headers the customer may not set; Hedvi writes them or they break MIME.
var reservedHeaders = map[string]bool{
	"from": true, "to": true, "cc": true, "bcc": true, "subject": true, "reply-to": true, "sender": true,
	"date": true, "message-id": true, "mime-version": true, "content-type": true,
	"content-transfer-encoding": true, "content-disposition": true, "return-path": true, "received": true,
	"dkim-signature": true, "arc-seal": true, "arc-message-signature": true, "arc-authentication-results": true,
	"authentication-results": true, "received-spf": true, "errors-to": true,
	"content-id": true, "content-description": true, "content-location": true, "disposition-notification-to": true,
}

// Attachment extensions SES refuses, plus common executable formats.
var blockedExtensions = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`ade adp app asp bas bat cer chm cmd com cpl crt csh der exe fxp gadget hlp hta inf ins isp
		its js jse ksh lib lnk mad maf mag mam maq mar mas mat mau mav maw mda mdb mde mdt mdw mdz msc msh msh1
		msh2 mshxml msh1xml msh2xml msi msp mst ops pcd pif plg prf prg reg scf scr sct shb shs sys ps1 ps1xml
		ps2 ps2xml psc1 psc2 tmp url vb vbe vbs vps vsmacros vss vst vsw vxd ws wsc wsf wsh xnk jar iso img appx`) {
		blockedExtensions[e] = true
	}
}

// Validate turns a request into a canonical message.
func (r *SendRequest) Validate(l Limits) (*Message, error) {
	if r.ScheduledAt != nil && *r.ScheduledAt != "" {
		return nil, invalid("`scheduled_at` is not supported yet. Send immediately or schedule in your app. See %s", CompatibilityURL)
	}
	if len(r.Template) > 0 && string(r.Template) != "null" {
		return nil, invalid("`template` is not supported yet. Render the email in your app and send `html`/`text`. See %s", CompatibilityURL)
	}
	if r.TopicID != nil && *r.TopicID != "" {
		return nil, invalid("`topic_id` is not supported. See %s", CompatibilityURL)
	}
	if strings.TrimSpace(r.From) == "" {
		return nil, missing("from")
	}
	from, err := address.Parse(r.From)
	if err != nil {
		return nil, &ValidationError{Name: "invalid_from_address", Message: "Invalid `from` field. The email address needs to follow the `email@example.com` or `Name <email@example.com>` format."}
	}
	if len(r.To) == 0 {
		return nil, missing("to")
	}
	if n := len(r.To) + len(r.Cc) + len(r.Bcc); n > l.MaxRecipients {
		return nil, invalid("Too many recipients: %d. The maximum across `to`, `cc` and `bcc` is %d.", n, l.MaxRecipients)
	}
	if len(r.ReplyTo) > l.MaxRecipients {
		return nil, invalid("Too many reply-to addresses (maximum %d).", l.MaxRecipients)
	}
	to, err := address.ParseList("to", r.To)
	if err != nil {
		return nil, invalid("Invalid `%s`.", err)
	}
	cc, err := address.ParseList("cc", r.Cc)
	if err != nil {
		return nil, invalid("Invalid `%s`.", err)
	}
	bcc, err := address.ParseList("bcc", r.Bcc)
	if err != nil {
		return nil, invalid("Invalid `%s`.", err)
	}
	replyTo, err := address.ParseList("reply_to", r.ReplyTo)
	if err != nil {
		return nil, invalid("Invalid `%s`.", err)
	}
	if r.Subject == nil {
		return nil, missing("subject")
	}
	subject := *r.Subject
	if strings.IndexFunc(subject, unicode.IsControl) >= 0 || len(subject) > 989 || !utf8.ValidString(subject) {
		return nil, invalid("Invalid `subject`: at most 989 bytes without control characters.")
	}
	m := &Message{Version: 1, From: from.Header(), Subject: subject, FromAddr: from}
	if r.HTML != nil {
		m.HTML = *r.HTML
	}
	if r.Text != nil {
		m.Text = *r.Text
	}
	if m.HTML == "" && m.Text == "" {
		return nil, missing("html` or `text")
	}
	if !utf8.ValidString(m.HTML) || !utf8.ValidString(m.Text) {
		return nil, invalid("`html` and `text` must be valid UTF-8.")
	}
	for _, list := range []struct {
		src []address.Address
		dst *[]string
	}{{to, &m.To}, {cc, &m.Cc}, {bcc, &m.Bcc}, {replyTo, &m.ReplyTo}} {
		for _, a := range list.src {
			*list.dst = append(*list.dst, a.Header())
		}
	}
	m.Recipients = address.Collect(to, cc, bcc)

	if len(r.Headers) > l.MaxHeaders {
		return nil, invalid("Too many custom headers (maximum %d).", l.MaxHeaders)
	}
	seenHeaders := make(map[string]bool, len(r.Headers))
	for name, value := range r.Headers {
		lower := strings.ToLower(name)
		if seenHeaders[lower] {
			return nil, invalid("Duplicate custom header %q.", name)
		}
		seenHeaders[lower] = true
		if !headerNamePattern.MatchString(name) {
			return nil, invalid("Invalid header name %q.", name)
		}
		if reservedHeader(lower) {
			return nil, invalid("Header %q is set by Hedvi and cannot be overridden. Use the matching field instead.", name)
		}
		if strings.IndexFunc(value, unicode.IsControl) >= 0 || len(name)+2+len(value) > 998 || !utf8.ValidString(value) {
			return nil, invalid("Invalid value for header %q: single line, at most 998 bytes.", name)
		}
		m.Headers = append(m.Headers, Header{Name: name, Value: value})
	}
	sort.Slice(m.Headers, func(i, j int) bool { return strings.ToLower(m.Headers[i].Name) < strings.ToLower(m.Headers[j].Name) })

	if len(r.Tags) > l.MaxTags {
		return nil, invalid("Too many tags (maximum %d).", l.MaxTags)
	}
	for _, t := range r.Tags {
		if !tagPattern.MatchString(t.Name) || !tagPattern.MatchString(t.Value) {
			return nil, invalid("Tag names and values may only contain ASCII letters, numbers, underscores or dashes, and be at most 256 characters.")
		}
		m.Tags = append(m.Tags, t)
	}

	if len(r.Attachments) > l.MaxAttachments {
		return nil, invalid("Too many attachments (maximum %d).", l.MaxAttachments)
	}
	size := len(m.HTML) + len(m.Text)
	for i, a := range r.Attachments {
		if a.Path != nil && *a.Path != "" {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf(
				"attachments[%d].path is not supported: Hedvi does not fetch remote files. Send the file as base64 `content`. See %s", i, CompatibilityURL)}
		}
		if a.Content == nil || *a.Content == "" {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d].content is required.", i)}
		}
		name := strings.TrimSpace(a.Filename)
		if name == "" || len(name) > 255 || strings.HasSuffix(name, ".") || strings.IndexFunc(a.Filename, unicode.IsControl) >= 0 || strings.ContainsAny(name, "/\\\r\n\x00\"") {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d].filename is required and must be a plain file name.", i)}
		}
		ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
		if blockedExtensions[ext] {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d]: .%s files are not allowed.", i, ext)}
		}
		data, err := base64.StdEncoding.DecodeString(stripSpace(*a.Content))
		if err != nil {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d].content must be base64.", i)}
		}
		ct := a.ContentType
		if ct == "" {
			ct = mime.TypeByExtension("." + ext)
		}
		if ct == "" {
			ct = "application/octet-stream"
		}
		mediaType, _, parseErr := mime.ParseMediaType(ct)
		if parseErr != nil || !strings.Contains(mediaType, "/") || strings.IndexFunc(ct, unicode.IsControl) >= 0 || len(ct) > 255 || mime.FormatMediaType(mediaType, nil) == "" {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d].content_type is invalid.", i)}
		}
		if a.ContentID != "" && !contentIDPattern.MatchString(a.ContentID) {
			return nil, &ValidationError{Name: "invalid_attachment", Message: fmt.Sprintf("attachments[%d].content_id is invalid.", i)}
		}
		size += len(data)
		m.Attachments = append(m.Attachments, Attachment{Filename: name, ContentType: ct, ContentID: a.ContentID, Data: data})
	}
	if size > l.MaxPayloadBytes {
		return nil, invalid("Email is too large: %d bytes. The maximum for html, text and attachments together is %d bytes.", size, l.MaxPayloadBytes)
	}
	return m, nil
}

func reservedHeader(lower string) bool {
	return reservedHeaders[lower] || strings.HasPrefix(lower, "x-ses-") || strings.HasPrefix(lower, "x-amz-") || strings.HasPrefix(lower, "x-hedvi-") || strings.HasPrefix(lower, "resent-")
}

func stripSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s)
}

// AttachmentBytes is the encoded (base64) size of attachments as sent, used
// for the data budget.
func (m *Message) AttachmentBytes() int64 {
	var n int64
	for _, a := range m.Attachments {
		n += int64(base64.StdEncoding.EncodedLen(len(a.Data)))
	}
	return n
}

// Canonical returns the stored JSON form.
func (m *Message) Canonical() ([]byte, error) { return json.Marshal(m) }

// Load parses a stored payload.
func Load(b []byte) (*Message, error) {
	var m Message
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m.Version != 1 {
		return nil, fmt.Errorf("unsupported payload version %d", m.Version)
	}
	for _, h := range m.Headers {
		if reservedHeader(strings.ToLower(h.Name)) {
			return nil, errors.New("stored message contains a reserved header")
		}
	}
	if _, err := address.ParseList("reply_to", m.ReplyTo); err != nil {
		return nil, err
	}
	from, err := address.Parse(m.From)
	if err != nil {
		return nil, err
	}
	m.FromAddr = from
	to, err1 := address.ParseList("to", m.To)
	cc, err2 := address.ParseList("cc", m.Cc)
	bcc, err3 := address.ParseList("bcc", m.Bcc)
	if err := errors.Join(err1, err2, err3); err != nil {
		return nil, err
	}
	m.Recipients = address.Collect(to, cc, bcc)
	return &m, nil
}
