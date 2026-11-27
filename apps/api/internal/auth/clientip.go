package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// AttestedClientIP authenticates the optional private Next.js proxy hop. That
// proxy must obtain its client address from a verified ingress, strip incoming
// attestation headers, and keep this key server-side. Never trust the headers
// alone or use a shared public/default secret.
func AttestedClientIP(r *http.Request, key []byte, now time.Time) *netip.Addr {
	if len(key) != 32 {
		return nil
	}
	for _, name := range []string{"X-Hedvi-Client-IP", "X-Hedvi-Proxy-Time", "X-Hedvi-Proxy-Signature"} {
		if len(r.Header.Values(name)) != 1 {
			return nil
		}
	}
	rawIP, stamp := r.Header.Get("X-Hedvi-Client-IP"), r.Header.Get("X-Hedvi-Proxy-Time")
	ip, err := netip.ParseAddr(rawIP)
	if err != nil || ip.Zone() != "" {
		return nil
	}
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || seconds < now.Unix()-30 || seconds > now.Unix()+30 {
		return nil
	}
	sig, err := hex.DecodeString(r.Header.Get("X-Hedvi-Proxy-Signature"))
	if err != nil || len(sig) != sha256.Size {
		return nil
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(stamp + "\n" + r.Method + "\n" + r.URL.EscapedPath() + "\n" + rawIP))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil
	}
	ip = ip.Unmap()
	return &ip
}

// ClientIP trusts forwarding data only when the immediate transport peer is in
// an explicitly configured proxy network. Walk right-to-left to the first
// untrusted hop; attacker-supplied entries to its left are never consulted.
func ClientIP(r *http.Request, trusted []netip.Prefix) *netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	peer = peer.Unmap()
	contains := func(ip netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !contains(peer) {
		return &peer
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return &peer
	}
	chain := strings.Split(strings.Join(values, ","), ",")
	if len(chain) > 32 {
		return &peer
	}
	current := peer
	for i := len(chain) - 1; i >= 0 && contains(current); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil || next.Zone() != "" {
			return &peer
		}
		current = next.Unmap()
	}
	return &current
}
