// Package domains manages sending domains: SES identities, the DNS records a
// customer must publish, live checks and guidance that never overwrites an
// existing SPF or DMARC policy.
package domains

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

// Record is Resend-compatible (record, name, type, ttl, status, value,
// priority) plus Hedvi guidance fields.
type Record struct {
	Record   string `json:"record"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      string `json:"ttl"`
	Status   string `json:"status"`
	Value    string `json:"value"`
	Priority *int   `json:"priority,omitempty"`
	// Hedvi extensions.
	FQDN     string   `json:"fqdn"`
	Purpose  string   `json:"purpose"`
	Required bool     `json:"required"`
	Found    []string `json:"found,omitempty"`
}

// Analysis captures what Hedvi found in the customer's existing DNS.
type Analysis struct {
	MailFromSafety string    `json:"mail_from_safety,omitempty"`
	NSProvider     string    `json:"ns_provider"`
	Nameservers    []string  `json:"nameservers,omitempty"`
	ProviderTips   []string  `json:"provider_tips,omitempty"`
	RootSPF        []string  `json:"root_spf,omitempty"`
	DMARC          string    `json:"dmarc,omitempty"`
	DMARCPolicy    string    `json:"dmarc_policy,omitempty"`
	Findings       []Finding `json:"findings"`
	CheckedAt      time.Time `json:"checked_at"`
}

type Finding struct {
	Level   string `json:"level"` // info, warning, error
	Code    string `json:"code"`
	Message string `json:"message"`
}

var labelPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeName validates a sending domain and returns its ASCII form.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	ascii, err := idna.Lookup.ToASCII(name)
	if err != nil || len(ascii) > 253 || !strings.Contains(ascii, ".") {
		return "", fmt.Errorf("%q is not a valid domain name", raw)
	}
	for _, l := range strings.Split(ascii, ".") {
		if !labelPattern.MatchString(l) {
			return "", fmt.Errorf("%q is not a valid domain name", raw)
		}
	}
	if net.ParseIP(ascii) != nil {
		return "", errors.New("IP addresses cannot be sending domains")
	}
	if strings.HasSuffix(ascii, ".test") || strings.HasSuffix(ascii, ".example") || strings.HasSuffix(ascii, ".localhost") {
		return ascii, nil // reserved names, only useful with the local DNS mock
	}
	etld1, err := publicsuffix.EffectiveTLDPlusOne(ascii)
	if err != nil || etld1 == "" {
		return "", fmt.Errorf("%q is a public suffix; use a domain you own", raw)
	}
	if ps, icann := publicsuffix.PublicSuffix(ascii); ps == ascii && icann {
		return "", fmt.Errorf("%q is a public suffix; use a domain you own", raw)
	}
	return ascii, nil
}

func reservedName(name string) bool {
	for _, suffix := range []string{".test", ".example", ".localhost", ".invalid", ".local"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func ValidMailFromLabel(l string) bool { return labelPattern.MatchString(l) }

func intp(i int) *int { return &i }

// ExpectedRecords lists what the customer must publish.
func ExpectedRecords(domain, mailFrom, mailFromMX string, dkimTokens []string, claimID uuid.UUID) []Record {
	var rs []Record
	for _, t := range dkimTokens {
		rs = append(rs, Record{Record: "DKIM", Name: t + "._domainkey", FQDN: t + "._domainkey." + domain, Type: "CNAME",
			TTL: "Auto", Status: "not_started", Value: t + ".dkim.amazonses.com", Required: true,
			Purpose: "Signs your email so receivers can verify it came from your domain (DKIM)."})
	}
	rs = append(rs,
		Record{Record: "SPF", Name: mailFrom, FQDN: mailFrom + "." + domain, Type: "MX", TTL: "Auto", Status: "not_started",
			Value: mailFromMX, Priority: intp(10), Required: true,
			Purpose: "Custom MAIL FROM: lets bounces return to Hedvi for this subdomain. It does not affect mail you receive on your main domain."},
		Record{Record: "SPF", Name: mailFrom, FQDN: mailFrom + "." + domain, Type: "TXT", TTL: "Auto", Status: "not_started",
			Value: "v=spf1 include:amazonses.com ~all", Required: true,
			Purpose: "SPF for the MAIL FROM subdomain only. Your existing SPF record on " + domain + " stays unchanged."},
	)
	return withOwnershipRecord(rs, domain, claimID)
}

// OwnershipRecord proves DNS control for this claim, independently of SES's
// account-scoped DKIM identity. A recreated identity must not reuse old proof.
func OwnershipRecord(domain string, claimID uuid.UUID) Record {
	return Record{Record: "Ownership", Name: "_hedvi-verification", FQDN: "_hedvi-verification." + domain,
		Type: "TXT", TTL: "Auto", Status: "not_started", Value: "hedvi-domain=" + claimID.String(), Required: true,
		Purpose: "Proves that you control this domain for this Hedvi project. Keep this TXT record while sending. A new domain claim needs its own value, even if DKIM records already exist."}
}

// Legacy rows and interrupted provisioning still need to show the current
// challenge in the DNS guide. Never keep another claim's ownership record.
func withOwnershipRecord(records []Record, domain string, claimID uuid.UUID) []Record {
	expected := OwnershipRecord(domain, claimID)
	out := make([]Record, 0, len(records)+1)
	for _, record := range records {
		if record.Record != "Ownership" {
			out = append(out, record)
			continue
		}
		if record.FQDN == expected.FQDN && record.Type == expected.Type && record.Value == expected.Value {
			expected.Status, expected.Found = record.Status, record.Found
		}
	}
	return append(out, expected)
}

// knownProviders maps nameserver suffixes to DNS providers. It is a hint
// only; the customer can choose another provider in the guide.
var knownProviders = []struct{ suffix, name string }{
	{".ns.cloudflare.com", "Cloudflare"}, {".awsdns-", "Amazon Route 53"}, {".loopia.se", "Loopia"},
	{".one.com", "One.com"}, {".domaincontrol.com", "GoDaddy"}, {".registrar-servers.com", "Namecheap"},
	{".googledomains.com", "Google Cloud DNS / Squarespace"}, {".digitalocean.com", "DigitalOcean"},
	{".vercel-dns.com", "Vercel"}, {".binero.se", "Binero"}, {".simply.com", "Simply.com"},
	{".gandi.net", "Gandi"}, {".ovh.net", "OVHcloud"}, {".hostinger.com", "Hostinger"}, {".glesys.se", "GleSYS"},
	{".azure-dns.", "Azure DNS"}, {".netlify.com", "Netlify"}, {".hetzner.com", "Hetzner"}, {".ns1.com", "NS1"},
}

var providerTips = map[string][]string{
	"Cloudflare":      {"Set DKIM CNAME records to \"DNS only\" (grey cloud). Proxied CNAMEs break DKIM.", "Enter the Name without your domain; Cloudflare adds it."},
	"Loopia":          {"In Loopia DNS editor, create the subdomain first (e.g. the DKIM name), then add the record with host \"@\" under it."},
	"One.com":         {"Use \"Advanced settings\" → DNS records. Enter the host without your domain."},
	"GoDaddy":         {"Enter the Host without your domain. GoDaddy appends it automatically."},
	"Namecheap":       {"Use \"Advanced DNS\". Enter the Host without your domain."},
	"Amazon Route 53": {"Enter the full record name; Route 53 shows the domain suffix."},
}

func detectProvider(ns []string) string {
	for _, n := range ns {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		for _, p := range knownProviders {
			if strings.Contains(n, p.suffix) || strings.HasSuffix(n, strings.TrimPrefix(p.suffix, ".")) {
				return p.name
			}
		}
	}
	return "Unidentified"
}

// Resolver is the subset of net.Resolver used for checks.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupCNAME(ctx context.Context, name string) (string, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupNS(ctx context.Context, name string) ([]*net.NS, error)
}

// NewResolver returns the system resolver or one bound to a DNS server.
func NewResolver(server string) Resolver {
	if server == "" {
		return &net.Resolver{PreferGo: true}
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 3 * time.Second}
		return d.DialContext(ctx, network, server)
	}}
}

// MockResolver answers for *.test domains as if the customer published the
// expected records correctly. Local development only.
type MockResolver struct {
	Expected []Record
	Fallback Resolver
}

func (m MockResolver) find(name, typ string) []Record {
	var out []Record
	for _, r := range m.Expected {
		if strings.EqualFold(r.FQDN, strings.TrimSuffix(name, ".")) && r.Type == typ {
			out = append(out, r)
		}
	}
	return out
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (m MockResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	if !strings.HasSuffix(strings.TrimSuffix(name, "."), ".test") {
		return m.Fallback.LookupTXT(ctx, name)
	}
	var out []string
	for _, r := range m.find(name, "TXT") {
		out = append(out, r.Value)
	}
	if len(out) == 0 {
		return nil, notFound(name)
	}
	return out, nil
}

func (m MockResolver) LookupCNAME(ctx context.Context, name string) (string, error) {
	if !strings.HasSuffix(strings.TrimSuffix(name, "."), ".test") {
		return m.Fallback.LookupCNAME(ctx, name)
	}
	if rs := m.find(name, "CNAME"); len(rs) > 0 {
		return rs[0].Value + ".", nil
	}
	return "", notFound(name)
}

func (m MockResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	if !strings.HasSuffix(strings.TrimSuffix(name, "."), ".test") {
		return m.Fallback.LookupMX(ctx, name)
	}
	var out []*net.MX
	for _, r := range m.find(name, "MX") {
		out = append(out, &net.MX{Host: r.Value + ".", Pref: uint16(*r.Priority)})
	}
	if len(out) == 0 {
		return nil, notFound(name)
	}
	return out, nil
}

func (m MockResolver) LookupNS(ctx context.Context, name string) ([]*net.NS, error) {
	if !strings.HasSuffix(name, ".test") {
		return m.Fallback.LookupNS(ctx, name)
	}
	return []*net.NS{{Host: "ns1.localdev.test."}}, nil
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}

// Values shown to customers come from arbitrary DNS packets. Only bounded,
// printable UTF-8 belongs in PostgreSQL JSONB. Compare original DNS data
// when deciding whether a required record matches.
func displayDNS(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			r = '�'
		}
		b.WriteRune(r)
		if b.Len() >= 512 {
			break
		}
	}
	return b.String()
}

// Check resolves every expected record and analyses existing SPF/DMARC.
// It mutates the records' status/found fields and returns the analysis.
func Check(ctx context.Context, r Resolver, domain string, records []Record) (Analysis, bool) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	a := Analysis{CheckedAt: time.Now().UTC(), Findings: []Finding{}}
	temporary := false
	var mx, spf *Record
	for i := range records {
		rec := &records[i]
		rec.Found = nil
		if rec.Record == "SPF" {
			switch rec.Type {
			case "MX":
				mx = rec
			case "TXT":
				spf = rec
			}
			continue
		}
		var err error
		switch rec.Type {
		case "CNAME":
			var got string
			got, err = r.LookupCNAME(ctx, rec.FQDN)
			got = strings.TrimSuffix(strings.ToLower(got), ".")
			if err == nil && got != "" && got != strings.ToLower(rec.FQDN) {
				rec.Found = []string{displayDNS(got)}
			}
			rec.Status = statusFor(err, got == strings.ToLower(rec.Value))
		case "TXT":
			// The claim is bound to this absolute name.
			txts, lookupErr := r.LookupTXT(ctx, rec.FQDN+".")
			for _, t := range txts {
				if len(rec.Found) < 16 {
					rec.Found = append(rec.Found, displayDNS(t))
				}
			}
			rec.Status = statusFor(lookupErr, slices.Contains(txts, rec.Value))
		}
		if rec.Status == "temporary_failure" {
			temporary = true
		}
	}

	if mx != nil && spf != nil {
		a.MailFromSafety = inspectMailFrom(ctx, r, mx, spf)
		switch a.MailFromSafety {
		case "conflict":
			a.Findings = append(a.Findings, Finding{"error", "mail_from_conflict", mx.FQDN + " already has mail or address records. Keep them in DNS and choose an unused MAIL FROM subdomain."})
		case "unknown":
			temporary = true
			a.Findings = append(a.Findings, Finding{"warning", "mail_from_lookup_failed", "DNS could not confirm the MAIL FROM records. Check again before publishing."})
		}
	}

	if ns, err := r.LookupNS(ctx, registrable(domain)); err == nil {
		for _, n := range ns {
			if len(a.Nameservers) < 16 {
				a.Nameservers = append(a.Nameservers, displayDNS(strings.TrimSuffix(n.Host, ".")))
			}
		}
		sort.Strings(a.Nameservers)
	}
	a.NSProvider = detectProvider(a.Nameservers)
	a.ProviderTips = providerTips[a.NSProvider]

	if txts, err := r.LookupTXT(ctx, domain); err == nil {
		for _, t := range txts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=spf1") {
				if len(a.RootSPF) < 16 {
					a.RootSPF = append(a.RootSPF, displayDNS(t))
				}
			}
		}
	}
	switch len(a.RootSPF) {
	case 0:
		a.Findings = append(a.Findings, Finding{"info", "no_root_spf",
			"No SPF record on " + domain + ". Hedvi does not need one there: SPF is handled on the MAIL FROM subdomain and DKIM aligns your domain."})
	case 1:
		a.Findings = append(a.Findings, Finding{"info", "root_spf_kept",
			"Your existing SPF record on " + domain + " is kept as is. Do not add amazonses.com to it; Hedvi uses the MAIL FROM subdomain."})
	default:
		a.Findings = append(a.Findings, Finding{"warning", "multiple_root_spf",
			fmt.Sprintf("%s has %d SPF records, which makes SPF fail for all mail from the domain. Merge them into one record. Hedvi will not change them for you.", domain, len(a.RootSPF))})
	}

	dmarcTXT, err := r.LookupTXT(ctx, "_dmarc."+registrableOrSelf(domain))
	for _, t := range dmarcTXT {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1") {
			a.DMARC = displayDNS(t)
			a.DMARCPolicy = displayDNS(tagValue(t, "p"))
		}
	}
	switch {
	case err != nil && !isNotFound(err):
		temporary = true
	case a.DMARC == "":
		a.Findings = append(a.Findings, Finding{"warning", "no_dmarc",
			"No DMARC record found. Gmail and Yahoo expect one from bulk senders. A safe start is TXT _dmarc." + registrableOrSelf(domain) +
				" \"v=DMARC1; p=none; rua=mailto:dmarc@" + registrableOrSelf(domain) + "\". Review reports before tightening the policy."})
	default:
		a.Findings = append(a.Findings, Finding{"info", "dmarc_kept",
			"Existing DMARC policy (p=" + a.DMARCPolicy + ") is kept. Hedvi signs with DKIM for your domain, so aligned mail passes DMARC."})
	}
	return a, temporary
}

func statusFor(err error, ok bool) string {
	switch {
	case err != nil && !isNotFound(err):
		return "temporary_failure"
	case err != nil:
		return "pending"
	case ok:
		return "verified"
	default:
		return "pending"
	}
}

func tagValue(record, tag string) string {
	for _, part := range strings.Split(record, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), tag) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func registrable(domain string) string {
	if r, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return r
	}
	return domain
}

func registrableOrSelf(domain string) string {
	if strings.HasSuffix(domain, ".test") {
		return domain
	}
	return registrable(domain)
}

// Overall derives the domain status from record statuses.
func Overall(records []Record, providerVerified *bool) string {
	required, verified, temp := 0, 0, false
	dkimOK := true
	for _, r := range records {
		if !r.Required {
			continue
		}
		required++
		if r.Status == "verified" {
			verified++
		} else if r.Record == "DKIM" {
			dkimOK = false
		}
		temp = temp || r.Status == "temporary_failure"
	}
	if providerVerified != nil && !*providerVerified {
		verified = min(verified, required-1)
	}
	switch {
	case required > 0 && verified == required:
		return "verified"
	case temp:
		return "temporary_failure"
	case verified > 0 && dkimOK:
		return "partially_verified"
	default:
		return "pending"
	}
}

// NextCheck implements polling with backoff: quick at first, then hourly,
// then every six hours. After 72 hours unverified domains stop polling and
// are marked failed until the customer asks for a new check.
func NextCheck(created time.Time, attempts int, now time.Time) (time.Time, bool) {
	if now.Sub(created) > 72*time.Hour {
		return time.Time{}, false
	}
	steps := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute, time.Hour}
	if attempts < len(steps) {
		return now.Add(steps[attempts]), true
	}
	if attempts < len(steps)+12 {
		return now.Add(time.Hour), true
	}
	return now.Add(6 * time.Hour), true
}
