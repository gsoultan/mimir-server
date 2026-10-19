package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gsoultan/mimir-server/internal/auth"
)

// TestLinkSecurityTrust checks that a worker can't pass the https check by
// sending X-Forwarded-Proto itself: only a trusted proxy's word counts.
func TestLinkSecurityTrust(t *testing.T) {
	proxies, err := auth.ParseProxies("10.0.0.1, 127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Proxies: proxies}
	check := func(remote string, header http.Header, tlsOn bool) string {
		t.Helper()
		var got string
		h := s.WithConn(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = linkSecurity(r.Context(), r.RemoteAddr)
		}))
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = remote
		r.Header = header
		if tlsOn {
			r.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		} else {
			r.TLS = nil
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	https := http.Header{"X-Forwarded-Proto": {"https"}}
	if got := check("203.0.113.9:4000", https, false); got != "plain" {
		t.Fatalf("spoofed https from a stranger: %s", got)
	}
	if got := check("10.0.0.1:4000", https, false); got != "proxy" {
		t.Fatalf("https from our proxy: %s", got)
	}
	if got := check("203.0.113.9:4000", http.Header{}, true); got != "tls" {
		t.Fatalf("direct TLS: %s", got)
	}
	// A plain link our proxy on this machine passed on isn't local.
	if got := check("127.0.0.1:4000", http.Header{"X-Forwarded-Proto": {"http"}, "X-Forwarded-For": {"203.0.113.9"}}, false); got != "plain" {
		t.Fatalf("plain link through a local proxy: %s", got)
	}
	if got := check("127.0.0.1:4000", http.Header{}, false); got != "local" {
		t.Fatalf("direct local link: %s", got)
	}

	// Trusting no one, the same headers count for nothing.
	s.Proxies = auth.Proxies{}
	if got := check("10.0.0.1:4000", https, false); got != "plain" {
		t.Fatalf("https header with no trusted proxies: %s", got)
	}
}
