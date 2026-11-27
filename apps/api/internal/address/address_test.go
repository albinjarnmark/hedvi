package address

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in, email, norm, name string
	}{
		{"ada@Example.COM", "ada@example.com", "ada@example.com", ""},
		{"Ada Lovelace <Ada@Example.com>", "Ada@example.com", "ada@example.com", "Ada Lovelace"},
		{"kund@räksmörgås.se", "kund@xn--rksmrgs-5wao1o.se", "kund@xn--rksmrgs-5wao1o.se", ""},
		{"=?utf-8?q?=C3=85sa?= <asa@example.se>", "asa@example.se", "asa@example.se", "Åsa"},
	}
	for _, c := range cases {
		a, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if a.Email != c.email || a.Normalized != c.norm || a.Name != c.name {
			t.Fatalf("%q => %+v", c.in, a)
		}
	}
	for _, bad := range []string{"", "nope", "a@b", "a@[127.0.0.1]", "åsa@example.se", "a@example.com\r\nBcc: x@y.z", "@example.com"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestCollectDeduplicatesWithFieldPriority(t *testing.T) {
	to, _ := ParseList("to", []string{"a@x.se", "B@x.se"})
	cc, _ := ParseList("cc", []string{"b@x.se", "c@x.se"})
	bcc, _ := ParseList("bcc", []string{"A@X.SE", "d@x.se", "c@x.se"})
	got := Collect(to, cc, bcc)
	want := map[string]Field{"a@x.se": FieldTo, "b@x.se": FieldTo, "c@x.se": FieldCc, "d@x.se": FieldBcc}
	if len(got) != len(want) {
		t.Fatalf("got %d recipients, want %d: %+v", len(got), len(want), got)
	}
	for _, r := range got {
		if want[r.Normalized] != r.Field {
			t.Fatalf("%s field %s, want %s", r.Normalized, r.Field, want[r.Normalized])
		}
	}
}

func TestHeaderEncodesNonASCIINames(t *testing.T) {
	a, _ := Parse("Åsa Öberg <asa@example.se>")
	if got := a.Header(); got != "=?utf-8?q?=C3=85sa_=C3=96berg?= <asa@example.se>" {
		t.Fatalf("header %q", got)
	}
}

func TestRejectsQuotedMailboxLocalPart(t *testing.T) {
	for _, raw := range []string{`"name"@example.org`, `Display <"name with space"@example.org>`, `Display <"a@b"@example.org>`, `Display <".a."@example.org>`} {
		if _, err := Parse(raw); err == nil {
			t.Fatal("accepted quoted local part")
		}
	}
	if _, err := Parse(`"Last, First" <first.last+one@example.org>`); err != nil {
		t.Fatal(err)
	}
}
