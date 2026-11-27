package sesevent

import (
	"errors"
	"testing"
)

func TestParseSNSEnvelopeAndRaw(t *testing.T) {
	raw := `{"eventType":"Bounce","mail":{"messageId":"m1","tags":{"hedvi_email":["e1"]}},"bounce":{"bounceType":"Permanent","bouncedRecipients":[{"emailAddress":"a@b.se"}]}}`
	ev, err := Parse([]byte(raw))
	if err != nil || ev.Type() != "Bounce" || ev.Tag("hedvi_email") != "e1" || ev.Bounce.BouncedRecipients[0].EmailAddress != "a@b.se" {
		t.Fatalf("raw: %+v %v", ev, err)
	}
	env := `{"Type":"Notification","Message":` + quote(raw) + `}`
	if ev, err := Parse([]byte(env)); err != nil || ev.Mail.MessageID != "m1" {
		t.Fatalf("envelope: %v", err)
	}
	if _, err := Parse([]byte(`{"Type":"SubscriptionConfirmation"}`)); !errors.Is(err, ErrIgnored) {
		t.Fatal("subscription confirmation not ignored")
	}
	if _, err := Parse([]byte(`{"eventType":"Open","mail":{"messageId":"x"}}`)); !errors.Is(err, ErrIgnored) {
		t.Fatal("open tracking should be ignored")
	}
}

func quote(s string) string {
	out := []byte{'"'}
	for _, c := range []byte(s) {
		if c == '"' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(append(out, '"'))
}
