// Package address parses and normalizes email addresses.
//
// Recipient contract (docs/architecture/quota.md):
//   - An address may carry a display name: "Ada <ada@example.com>".
//   - Domains are converted to ASCII (IDNA) and lowercased.
//   - Local parts must be ASCII; SMTPUTF8 is not supported yet.
//   - The normalized form lowercases the whole address and is used for
//     deduplication, suppression and quota. The original casing of the local
//     part is kept for delivery.
//   - The same normalized address in several fields counts once. Priority is
//     To, then Cc, then Bcc, so a Bcc recipient is never exposed in headers
//     because it also appeared in To.
package address

import (
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
)

type Address struct {
	Name       string
	Email      string // local@ascii-domain
	Normalized string
	Domain     string // ascii, lowercase
}

const (
	maxAddressLen = 254
	maxLocalLen   = 64
)

var dotAtom = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(?:\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$")
var quotedLocalEnd = regexp.MustCompile(`"[ ]*@`)

var idnaProfile = idna.New(idna.MapForLookup(), idna.BidiRule(), idna.StrictDomainName(true), idna.Transitional(false))

func Parse(raw string) (Address, error) {
	if len(raw) > 1024 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return Address{}, errors.New("address is too long or contains control characters")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Address{}, errors.New("address is empty")
	}
	if quotedLocalEnd.MatchString(raw) {
		return Address{}, errors.New("quoted local parts are not supported")
	}
	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return Address{}, fmt.Errorf("invalid address %q", raw)
	}
	at := strings.LastIndexByte(parsed.Address, '@')
	if at <= 0 || at == len(parsed.Address)-1 {
		return Address{}, fmt.Errorf("invalid address %q", raw)
	}
	local, domain := parsed.Address[:at], parsed.Address[at+1:]
	if strings.HasPrefix(domain, "[") {
		return Address{}, fmt.Errorf("address literals are not allowed: %q", raw)
	}
	if !dotAtom.MatchString(local) || len(parsed.Name) > 128 || strings.IndexFunc(parsed.Name, unicode.IsControl) >= 0 {
		return Address{}, errors.New("invalid local part or display name (maximum 128 bytes)")
	}
	if len(local) > maxLocalLen {
		return Address{}, fmt.Errorf("local part too long: %q", raw)
	}
	ascii, err := idnaProfile.ToASCII(strings.TrimSuffix(domain, "."))
	if err != nil || !strings.Contains(ascii, ".") {
		return Address{}, fmt.Errorf("invalid domain in %q", raw)
	}
	ascii = strings.ToLower(ascii)
	email := local + "@" + ascii
	if len(email) > maxAddressLen {
		return Address{}, fmt.Errorf("address too long: %q", raw)
	}
	return Address{Name: parsed.Name, Email: email, Normalized: strings.ToLower(email), Domain: ascii}, nil
}

// Header renders the address for a MIME header, encoding non-ASCII names.
func (a Address) Header() string {
	if a.Name == "" {
		return a.Email
	}
	plain := true
	for _, r := range a.Name {
		if r > 127 || !(r == ' ' || r == '-' || r == '.' || r == '\'' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			plain = false
			break
		}
	}
	if plain && !strings.HasPrefix(a.Name, " ") && !strings.HasSuffix(a.Name, " ") && !strings.Contains(a.Name, ".") {
		return a.Name + " <" + a.Email + ">"
	}
	return (&mail.Address{Name: a.Name, Address: a.Email}).String()
}

type Field string

const (
	FieldTo  Field = "to"
	FieldCc  Field = "cc"
	FieldBcc Field = "bcc"
)

type Recipient struct {
	Address
	Field Field
}

// ParseList parses each entry and reports the first error with its field.
func ParseList(field string, raws []string) ([]Address, error) {
	out := make([]Address, 0, len(raws))
	for i, r := range raws {
		a, err := Parse(r)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, i, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// Collect merges To, Cc and Bcc into unique delivery recipients.
func Collect(to, cc, bcc []Address) []Recipient {
	seen := make(map[string]bool, len(to)+len(cc)+len(bcc))
	var out []Recipient
	add := func(list []Address, f Field) {
		for _, a := range list {
			if seen[a.Normalized] {
				continue
			}
			seen[a.Normalized] = true
			out = append(out, Recipient{Address: a, Field: f})
		}
	}
	add(to, FieldTo)
	add(cc, FieldCc)
	add(bcc, FieldBcc)
	return out
}
