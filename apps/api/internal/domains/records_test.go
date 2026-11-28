package domains

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeResolver struct {
	txt   map[string][]string
	cname map[string]string
	mx    map[string][]*net.MX
	ns    map[string][]*net.NS
}

type unavailableMailFromDNS struct{ fakeResolver }

func (unavailableMailFromDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	return nil, &net.DNSError{Name: name, Err: "timed out", IsTimeout: true}
}

func TestMAILFromLookupFailureDoesNotPermitImport(t *testing.T) {
	records := ExpectedRecords("example.com", "bounce", "feedback-smtp.eu-north-1.amazonses.com", nil, uuid.New())
	a, temporary := Check(t.Context(), unavailableMailFromDNS{}, "example.com", records)
	if a.MailFromSafety != "unknown" || !temporary || records[0].Status != "temporary_failure" {
		t.Fatal(a, records)
	}
}

func (f fakeResolver) LookupTXT(_ context.Context, n string) ([]string, error) {
	if v, ok := f.txt[strings.TrimSuffix(n, ".")]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeResolver) LookupCNAME(_ context.Context, n string) (string, error) {
	if v, ok := f.cname[strings.TrimSuffix(n, ".")]; ok {
		return v, nil
	}
	return "", notFound(n)
}
func (f fakeResolver) LookupMX(_ context.Context, n string) ([]*net.MX, error) {
	if v, ok := f.mx[strings.TrimSuffix(n, ".")]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeResolver) LookupNS(_ context.Context, n string) ([]*net.NS, error) {
	if v, ok := f.ns[n]; ok {
		return v, nil
	}
	return nil, notFound(n)
}

func TestNormalizeName(t *testing.T) {
	for in, want := range map[string]string{"Example.COM.": "example.com", "mail.räksmörgås.se": "mail.xn--rksmrgs-5wao1o.se", "demo.hedvi.test": "demo.hedvi.test"} {
		if got, err := NormalizeName(in); err != nil || got != want {
			t.Errorf("%s => %s, %v", in, got, err)
		}
	}
	for _, bad := range []string{"com", "co.uk", "1.2.3.4", "-bad.com", "no_underscore.com", ""} {
		if _, err := NormalizeName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestCheckDetectsExistingPoliciesWithoutRequiringChanges(t *testing.T) {
	claimID := uuid.New()
	records := ExpectedRecords("kund.se", "send", "feedback-smtp.eu-north-1.amazonses.com", []string{"aaa", "bbb", "ccc"}, claimID)
	r := fakeResolver{
		cname: map[string]string{"aaa._domainkey.kund.se": "aaa.dkim.amazonses.com.", "bbb._domainkey.kund.se": "bbb.dkim.amazonses.com.",
			"ccc._domainkey.kund.se": "wrong.example.com."},
		mx: map[string][]*net.MX{"send.kund.se": {{Host: "feedback-smtp.eu-north-1.amazonses.com.", Pref: 10}}},
		txt: map[string][]string{"send.kund.se": {"v=spf1 include:amazonses.com ~all"}, "kund.se": {"v=spf1 include:_spf.google.com ~all", "v=spf1 a ~all"},
			"_dmarc.kund.se":              {"v=DMARC1; p=quarantine; rua=mailto:d@kund.se"},
			"_hedvi-verification.kund.se": {"hedvi-domain=" + claimID.String()}},
		ns: map[string][]*net.NS{"kund.se": {{Host: "ns1.loopia.se."}}},
	}
	a, temp := Check(context.Background(), r, "kund.se", records)
	if temp {
		t.Fatal("unexpected temporary failure")
	}
	if Overall(records, nil) != "partially_verified" && Overall(records, nil) != "pending" {
		t.Fatalf("overall %s", Overall(records, nil))
	}
	if records[2].Status != "pending" || records[2].Found[0] != "wrong.example.com" {
		t.Fatalf("wrong CNAME not reported: %+v", records[2])
	}
	if a.NSProvider != "Loopia" || a.DMARCPolicy != "quarantine" || len(a.RootSPF) != 2 {
		t.Fatalf("analysis %+v", a)
	}
	var codes []string
	for _, f := range a.Findings {
		codes = append(codes, f.Code)
	}
	if !strings.Contains(strings.Join(codes, ","), "multiple_root_spf") || !strings.Contains(strings.Join(codes, ","), "dmarc_kept") {
		t.Fatalf("findings %v", codes)
	}
	records[2].Status = "verified"
	r.cname["ccc._domainkey.kund.se"] = "ccc.dkim.amazonses.com."
	Check(context.Background(), r, "kund.se", records)
	if got := Overall(records, nil); got != "verified" {
		t.Fatalf("overall %s", got)
	}
	f := false
	if got := Overall(records, &f); got == "verified" {
		t.Fatal("provider status must be able to veto verification")
	}
}

func TestNextCheckBacksOffAndStops(t *testing.T) {
	start := time.Now()
	n1, ok1 := NextCheck(start, 0, start)
	n2, _ := NextCheck(start, 10, start)
	_, ok3 := NextCheck(start, 3, start.Add(73*time.Hour))
	if !ok1 || n1.Sub(start) != 30*time.Second || n2.Sub(start) != time.Hour || ok3 {
		t.Fatal("unexpected schedule")
	}
}

func TestMailFromRequiresDedicatedHostAndExactMX(t *testing.T) {
	const host = "bounce.example.com"
	const expected = "feedback-smtp.eu-north-1.amazonses.com"
	ours := &net.MX{Host: expected + ".", Pref: 10}
	other := &net.MX{Host: "mail.protection.outlook.com.", Pref: 5}
	const spf = "v=spf1 include:amazonses.com ~all"
	for _, tc := range []struct {
		name, cname, safety, mxStatus string
		mx                            []*net.MX
		txt                           []string
	}{
		{name: "unused host", safety: "clear", mxStatus: "pending"},
		{name: "existing Hedvi records", mx: []*net.MX{ours}, txt: []string{spf, "unrelated verification"}, safety: "clear", mxStatus: "verified"},
		{name: "Microsoft mail", mx: []*net.MX{other}, safety: "conflict", mxStatus: "pending"},
		{name: "mixed MX", mx: []*net.MX{ours, other}, safety: "conflict", mxStatus: "pending"},
		{name: "wrong priority", mx: []*net.MX{{Host: expected + ".", Pref: 20}}, safety: "conflict", mxStatus: "pending"},
		{name: "Google SPF", txt: []string{"v=spf1 include:_spf.google.com ~all"}, safety: "conflict", mxStatus: "pending"},
		{name: "multiple SPF policies", mx: []*net.MX{ours}, txt: []string{spf, "v=spf1 -all"}, safety: "conflict", mxStatus: "verified"},
		{name: "alias", cname: "mail.example.net.", safety: "conflict", mxStatus: "pending"},
		{name: "implicit MX via address", cname: host + ".", safety: "conflict", mxStatus: "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := fakeResolver{mx: map[string][]*net.MX{"example.com": {other}}, txt: map[string][]string{"example.com": {"v=spf1 include:_spf.google.com ~all"}}, cname: map[string]string{}}
			if tc.mx != nil {
				r.mx[host] = tc.mx
			}
			if tc.txt != nil {
				r.txt[host] = tc.txt
			}
			if tc.cname != "" {
				r.cname[host] = tc.cname
			}
			records := ExpectedRecords("example.com", "bounce", expected, nil, uuid.New())
			a, _ := Check(t.Context(), r, "example.com", records)
			if a.MailFromSafety != tc.safety || records[0].Status != tc.mxStatus {
				t.Fatalf("MAIL FROM safety=%s MX=%s", a.MailFromSafety, records[0].Status)
			}
		})
	}
}
