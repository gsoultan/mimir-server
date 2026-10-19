package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseProxies(t *testing.T) {
	p, err := ParseProxies(" 10.0.0.0/8, 127.0.0.1 ,::1,, ::ffff:192.168.1.0/120")
	if err != nil {
		t.Fatal(err)
	}
	for addr, want := range map[string]bool{
		"10.1.2.3:80":         true,
		"127.0.0.1:5000":      true,
		"127.0.0.2:5000":      false,
		"[::1]:443":           true,
		"[::ffff:10.9.9.9]:1": true,
		"192.168.1.77:1":      true,
		"192.168.2.1:1":       false,
		"203.0.113.9:1234":    false,
		"not an address":      false,
		"10.0.0.1":            true,
		"[fe80::1%eth0]:1":    false,
		"[2001:db8::1]:8080":  false,
	} {
		if got := p.Trusts(addr); got != want {
			t.Errorf("Trusts(%q) = %v, want %v", addr, got, want)
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "example.com", "1.2.3"} {
		if _, err := ParseProxies(bad); err == nil {
			t.Errorf("ParseProxies(%q) accepted", bad)
		}
	}
	if none, _ := ParseProxies(""); none.Any() || none.Trusts("127.0.0.1:1") {
		t.Fatal("an empty list trusts someone")
	}
}

func TestClientIPIgnoresUntrustedHeaders(t *testing.T) {
	spoof := http.Header{"X-Forwarded-For": {"1.2.3.4"}}
	// No proxies trusted: the header is the client's own claim.
	var none Proxies
	if ip := none.ClientIP(spoof, "203.0.113.9:5555"); ip != "203.0.113.9" {
		t.Fatalf("untrusted peer: %s", ip)
	}
	if ip := none.ClientIP(http.Header{}, "[::1]:443"); ip != "::1" {
		t.Fatal(ip)
	}

	p, _ := ParseProxies("10.0.0.0/8")
	// A trusted proxy's peer: take the nearest hop that isn't ours, so a
	// client can't put an address in front of the one our proxy recorded.
	h := http.Header{"X-Forwarded-For": {"1.2.3.4, 198.51.100.7", "10.0.0.2"}}
	if ip := p.ClientIP(h, "10.0.0.1:443"); ip != "198.51.100.7" {
		t.Fatalf("trusted proxy chain: %s", ip)
	}
	// The same headers from someone else count for nothing.
	if ip := p.ClientIP(h, "198.51.100.99:443"); ip != "198.51.100.99" {
		t.Fatalf("untrusted peer with headers: %s", ip)
	}
	// A trusted proxy that adds no header: the proxy is the client.
	if ip := p.ClientIP(http.Header{}, "10.0.0.1:443"); ip != "10.0.0.1" {
		t.Fatal(ip)
	}
	// Every hop ours: the first one.
	if ip := p.ClientIP(http.Header{"X-Forwarded-For": {"10.0.0.5, 10.0.0.6"}}, "10.0.0.1:443"); ip != "10.0.0.5" {
		t.Fatal(ip)
	}
}

func TestHTTPS(t *testing.T) {
	p, _ := ParseProxies("10.0.0.1")
	req := func(remote, proto string, tlsOn bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if proto != "" {
			r.Header.Set("X-Forwarded-Proto", proto)
		}
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		} else {
			r.TLS = nil
		}
		return r
	}
	cases := []struct {
		remote, proto string
		tls, want     bool
	}{
		{"203.0.113.9:1", "", true, true},        // we terminated TLS
		{"203.0.113.9:1", "https", false, false}, // anyone can say https
		{"10.0.0.1:1", "https", false, true},     // our proxy says https
		{"10.0.0.1:1", "http", false, false},     // our proxy says http
		{"10.0.0.1:1", "", false, false},         // our proxy says nothing
		{"203.0.113.9:1", "http", true, true},    // TLS wins over a header
	}
	for _, c := range cases {
		if got := p.HTTPS(req(c.remote, c.proto, c.tls)); got != c.want {
			t.Errorf("HTTPS(%s, %q, tls=%v) = %v", c.remote, c.proto, c.tls, got)
		}
	}
}
