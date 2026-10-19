package auth

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Proxies lists the reverse proxies whose X-Forwarded-For and
// X-Forwarded-Proto headers are believed. Anyone can send those headers,
// so they only count on a request whose direct peer is one of these. The
// zero value trusts no one: every request is judged by its own connection.
type Proxies struct {
	nets []netip.Prefix
}

// ParseProxies reads a comma-separated list of addresses and CIDR ranges,
// such as "10.0.0.0/8, 127.0.0.1, ::1". An empty list trusts no one.
func ParseProxies(list string) (Proxies, error) {
	var p Proxies
	for item := range strings.SplitSeq(list, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			pre, err := netip.ParsePrefix(item)
			if err != nil {
				return Proxies{}, fmt.Errorf("trusted proxy %q: %w", item, err)
			}
			p.nets = append(p.nets, unmapPrefix(pre.Masked()))
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return Proxies{}, fmt.Errorf("trusted proxy %q: not an address or CIDR range", item)
		}
		addr = addr.Unmap().WithZone("")
		p.nets = append(p.nets, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return p, nil
}

func unmapPrefix(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-96)
	}
	return p
}

// Any reports whether any proxy is trusted.
func (p Proxies) Any() bool { return len(p.nets) > 0 }

// String lists the trusted ranges, for the startup log.
func (p Proxies) String() string {
	s := make([]string, len(p.nets))
	for i, n := range p.nets {
		s[i] = n.String()
	}
	return strings.Join(s, ",")
}

// Trusts reports whether remoteAddr (host:port, or a bare host) is a
// trusted proxy.
func (p Proxies) Trusts(remoteAddr string) bool {
	if len(p.nets) == 0 {
		return false
	}
	addr, ok := parseHost(remoteAddr)
	return ok && p.contains(addr)
}

func (p Proxies) contains(addr netip.Addr) bool {
	for _, n := range p.nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}

func parseHost(remoteAddr string) (netip.Addr, bool) {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// ClientIP is the address a request came from. It is the direct peer
// unless that peer is a trusted proxy; then it is the nearest address in
// X-Forwarded-For that isn't one of ours, read from the right, since only
// the entries our own proxies appended can be believed. It's shown on the
// sessions list, written to the audit log and used to key rate limits.
func (p Proxies) ClientIP(h http.Header, remoteAddr string) string {
	peer := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		peer = host
	}
	if !p.Trusts(remoteAddr) {
		return peer
	}
	var hops []string
	for _, v := range h.Values("X-Forwarded-For") {
		for hop := range strings.SplitSeq(v, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, ok := parseHost(hops[i])
		if !ok {
			return hops[i] // garbage our proxy passed on: report it as is
		}
		if !p.contains(addr) || i == 0 {
			return addr.String()
		}
	}
	return peer
}

// HTTPS reports whether the browser reached us over HTTPS: either this
// server terminated TLS itself, or a trusted proxy says it did.
func (p Proxies) HTTPS(r *http.Request) bool {
	return r.TLS != nil || p.ForwardedHTTPS(r.Header, r.RemoteAddr)
}

// ForwardedHTTPS reports whether a trusted proxy says the caller used
// HTTPS.
func (p Proxies) ForwardedHTTPS(h http.Header, remoteAddr string) bool {
	return p.Trusts(remoteAddr) && strings.EqualFold(strings.TrimSpace(h.Get("X-Forwarded-Proto")), "https")
}

// Forwarded reports whether a trusted proxy passed this request on, as
// opposed to a client connecting straight to us.
func (p Proxies) Forwarded(h http.Header, remoteAddr string) bool {
	return p.Trusts(remoteAddr) && (h.Get("X-Forwarded-For") != "" || h.Get("X-Forwarded-Proto") != "")
}
