package sso

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestGitHub signs in through a stand-in for GitHub: the primary checked
// address, the numeric id as subject, and organizations only when a rule
// asks for them.
func TestGitHub(t *testing.T) {
	var scopes string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("code") != "the-code" || r.Form.Get("code_verifier") == "" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"gho_x","token_type":"bearer"}`))
	})
	api := func(path string, v any) {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer gho_x" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			json.NewEncoder(w).Encode(v)
		})
	}
	api("/user", map[string]any{"id": 42, "login": "octo"})
	api("/user/emails", []map[string]any{{"email": "old@x.test", "verified": true}, {"email": "Octo@Acme.test", "primary": true, "verified": true}})
	api("/user/orgs", []map[string]any{{"login": "acme"}})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New()
	c.GitHubWeb, c.GitHubAPI = srv.URL, srv.URL
	p := Provider{ID: "github", Kind: GitHub, Name: "GitHub", ClientID: "id", ClientSecret: "secret",
		Rules: []Rule{{Group: "acme", TeamID: "t1", Role: 2}}}
	ctx := context.Background()
	st := NewStart()
	to, err := c.AuthURL(ctx, p, "https://mimir.test/auth/sso/github/callback", st)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(to)
	scopes = u.Query().Get("scope")
	if u.Query().Get("state") != st.State || u.Query().Get("code_challenge") == "" || !strings.Contains(scopes, "read:org") {
		t.Fatalf("auth url = %s", to)
	}
	cl, err := c.Finish(ctx, p, "https://mimir.test/auth/sso/github/callback", "the-code", st)
	if err != nil {
		t.Fatal(err)
	}
	if cl.Issuer != GitHubIssuer || cl.Subject != "42" || cl.Email != "Octo@Acme.test" || !cl.EmailVerified || cl.Name != "octo" || len(cl.Groups) != 1 {
		t.Fatalf("claims = %+v", cl)
	}
	if r, ok := p.Match(cl.Email, cl.Groups); !ok || r.TeamID != "t1" {
		t.Fatalf("Match = %v, %v", r, ok)
	}

	// Without group rules, organizations aren't asked for.
	p.Rules = []Rule{{Domain: "acme.test", TeamID: "t2", Role: 1}}
	to, _ = c.AuthURL(ctx, p, "https://mimir.test/cb", st)
	if strings.Contains(to, "read%3Aorg") {
		t.Fatalf("asked for orgs: %s", to)
	}
	if r, ok := p.Match("someone@ACME.test", nil); !ok || r.TeamID != "t2" {
		t.Fatalf("domain Match = %v, %v", r, ok)
	}
	if _, ok := p.Match("someone@notacme.test", nil); ok {
		t.Fatal("matched another domain")
	}
}

func TestCheckIssuer(t *testing.T) {
	for _, ok := range []string{"https://login.example.com", "https://login.microsoftonline.com/abc/v2.0", "http://localhost:8080/realms/x"} {
		if err := CheckIssuer(ok); err != nil {
			t.Errorf("CheckIssuer(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "login.example.com", "http://login.example.com", "https://x.test/?a=b", "ftp://x.test"} {
		if CheckIssuer(bad) == nil {
			t.Errorf("CheckIssuer(%q) passed", bad)
		}
	}
}
