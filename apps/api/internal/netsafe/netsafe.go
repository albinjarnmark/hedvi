// Package netsafe builds HTTP clients for customer-controlled URLs
// (webhooks). It blocks loopback, private, link-local (including cloud
// metadata), CGNAT, multicast and documentation ranges. The check runs on the
// address actually dialed, so DNS rebinding cannot bypass it, and redirects
// are never followed.
package netsafe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

var ErrBlocked = errors.New("destination address is not allowed")

var globalIPv6 = netip.MustParsePrefix("2000::/3")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/32", "2001:db8::/32", "2002::/16",
		"::ffff:0:0:0/96", "fec0::/10", "2001:10::/28", "2001:20::/28", "2001:2::/48", "3fff::/20", "fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// AllowedIP reports whether an address may be contacted.
func AllowedIP(a netip.Addr) bool {
	a = a.Unmap()
	// Only currently allocated global unicast space is eligible for IPv6
	// egress. Reserved and translation formats outside it fail closed.
	if a.Is6() && !globalIPv6.Contains(a) {
		return false
	}
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

type Options struct {
	AllowPrivate bool // local development only
	AllowHTTP    bool // local development only
	Timeout      time.Duration
}

// ValidateURL checks a webhook URL before it is stored.
func ValidateURL(raw string, o Options) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("URL must be absolute, e.g. https://example.com/webhooks/hedvi")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && o.AllowHTTP:
	default:
		return nil, errors.New("URL must use https")
	}
	if u.User != nil {
		return nil, errors.New("URL must not contain credentials")
	}
	if len(raw) > 2048 {
		return nil, errors.New("URL is too long")
	}
	if !o.AllowPrivate && u.Port() != "" && u.Port() != "443" && u.Port() != "8443" {
		return nil, errors.New("webhooks must use HTTPS port 443 or 8443")
	}
	host := u.Hostname()
	if a, err := netip.ParseAddr(host); err == nil && !o.AllowPrivate && !AllowedIP(a) {
		return nil, ErrBlocked
	}
	if !o.AllowPrivate && (strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") ||
		strings.HasSuffix(strings.ToLower(host), ".internal") || strings.HasSuffix(strings.ToLower(host), ".local")) {
		return nil, ErrBlocked
	}
	return u, nil
}

// NewClient returns a client that refuses blocked destinations at dial time.
func NewClient(o Options) *http.Client {
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Second
	}
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			if o.AllowPrivate {
				return nil
			}
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlocked, address)
			}
			if !AllowedIP(ap.Addr()) || (ap.Port() != 443 && ap.Port() != 8443) {
				return fmt.Errorf("%w: %s", ErrBlocked, ap.Addr())
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil, // never route customer URLs through an environment proxy
		DialContext:           func(ctx context.Context, n, a string) (net.Conn, error) { return dialer.DialContext(ctx, n, a) },
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: o.Timeout,
		MaxIdleConns:          100,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
