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

func (f fakeResolver) LookupTXT(_ context.Context, n string) ([]string, error) {
	if v, ok := f.txt[strings.TrimSuffix(n, ".")]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeResolver) LookupCNAME(_ context.Context, n string) (string, error) {
	if v, ok := f.cname[n]; ok {
		return v, nil
	}
	return "", notFound(n)
}
func (f fakeResolver) LookupMX(_ context.Context, n string) ([]*net.MX, error) {
	if v, ok := f.mx[n]; ok {
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
