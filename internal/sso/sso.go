// Package sso signs people in with an outside account: Google and
// company identity providers through OpenID Connect, and GitHub through
// its OAuth apps, which have no ID tokens. It turns a provider's answer
// into a checked identity; package api decides which account that is.
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/gsoultan/mimir-server/internal/netguard"
)

// Kind is the sort of provider. The values match mimirv1.SignInKind.
type Kind int

const (
	Google Kind = 1
	GitHub Kind = 2
	OIDC   Kind = 3
)

// GoogleIssuer is Google's OpenID Connect issuer; GitHubIssuer names
// GitHub's accounts, which have no issuer of their own.
const (
	GoogleIssuer = "https://accounts.google.com"
	GitHubIssuer = "https://github.com"
)

// Provider is one way to sign in, as an owner set it up.
type Provider struct {
	ID           string `json:"id"`
	Kind         Kind   `json:"kind"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer,omitempty"` // OIDC only
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	GroupsClaim  string `json:"groups_claim,omitempty"` // OIDC only
	TrustEmails  bool   `json:"trust_emails,omitempty"` // OIDC only
	Rules        []Rule `json:"rules,omitempty"`
}

// Rule gives someone new an account in a team: by their email's domain,
// or by one of their groups.
type Rule struct {
	Domain string `json:"domain,omitempty"`
	Group  string `json:"group,omitempty"`
	TeamID string `json:"team_id"`
	Role   int    `json:"role"`
}

// IssuerURL is who vouches for the provider's people. Identities are
// keyed on it, so the same subject at two providers is two people.
func (p Provider) IssuerURL() string {
	switch p.Kind {
	case Google:
		return GoogleIssuer
	case GitHub:
		return GitHubIssuer
	}
	return strings.TrimRight(p.Issuer, "/")
}

// Match returns the first rule that fits someone, and whether one did.
func (p Provider) Match(email string, groups []string) (Rule, bool) {
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	for _, r := range p.Rules {
		if r.Domain != "" && strings.EqualFold(r.Domain, domain) {
			return r, true
		}
		if r.Group != "" {
			for _, g := range groups {
				if g == r.Group {
					return r, true
				}
			}
		}
	}
	return Rule{}, false
}

// wantsGroups reports whether any rule looks at groups, so GitHub is only
// asked to show organizations when they matter.
func (p Provider) wantsGroups() bool {
	for _, r := range p.Rules {
		if r.Group != "" {
			return true
		}
	}
	return false
}

// Claims is who the provider says signed in.
type Claims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
}

// Client talks to providers. Its zero value is not usable; use New.
type Client struct {
	http *http.Client
	// GitHub's addresses, which tests point elsewhere.
	GitHubWeb, GitHubAPI string

	mu        sync.Mutex
	providers map[string]cached // by issuer
}

type cached struct {
	p    *oidc.Provider
	when time.Time
}

// discoveryTTL is how long a provider's discovery document is reused.
// Its signing keys are refreshed on their own when a token needs a new one.
const discoveryTTL = time.Hour

// New returns a client whose connections can't reach the cloud's
// instance metadata. Company providers often run on the local network,
// so private addresses stay open.
func New() *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: netguard.Control(netguard.Metadata)}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = dialer.DialContext
	return &Client{
		http:      &http.Client{Transport: tr, Timeout: 20 * time.Second},
		GitHubWeb: "https://github.com",
		GitHubAPI: "https://api.github.com",
		providers: map[string]cached{},
	}
}

func (c *Client) ctx(ctx context.Context) context.Context {
	ctx = oidc.ClientContext(ctx, c.http)
	return context.WithValue(ctx, oauth2.HTTPClient, c.http)
}

// discover returns an issuer's OpenID Connect provider, cached.
func (c *Client) discover(ctx context.Context, issuer string) (*oidc.Provider, error) {
	c.mu.Lock()
	hit, ok := c.providers[issuer]
	c.mu.Unlock()
	if ok && time.Since(hit.when) < discoveryTTL {
		return hit.p, nil
	}
	p, err := oidc.NewProvider(c.ctx(ctx), issuer)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.providers[issuer] = cached{p, time.Now()}
	c.mu.Unlock()
	return p, nil
}

// Check reaches a provider's discovery document, so a wrong issuer shows
// up when it's saved rather than at someone's sign-in.
func (c *Client) Check(ctx context.Context, p Provider) error {
	if p.Kind == GitHub {
		return nil
	}
	_, err := c.discover(ctx, p.IssuerURL())
	return err
}

func (c *Client) config(ctx context.Context, p Provider, redirectURL string) (*oauth2.Config, error) {
	cfg := &oauth2.Config{ClientID: p.ClientID, ClientSecret: p.ClientSecret, RedirectURL: redirectURL}
	if p.Kind == GitHub {
		cfg.Endpoint = oauth2.Endpoint{AuthURL: c.GitHubWeb + "/login/oauth/authorize", TokenURL: c.GitHubWeb + "/login/oauth/access_token"}
		cfg.Scopes = []string{"read:user", "user:email"}
		if p.wantsGroups() {
			cfg.Scopes = append(cfg.Scopes, "read:org")
		}
		return cfg, nil
	}
	op, err := c.discover(ctx, p.IssuerURL())
	if err != nil {
		return nil, err
	}
	cfg.Endpoint = op.Endpoint()
	cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	return cfg, nil
}

// Start is what one sign-in carries from leaving for the provider to
// coming back: checked against the answer, never shown to the provider
// except as the state and the nonce.
type Start struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// NewStart makes fresh random values for one sign-in.
func NewStart() Start {
	return Start{State: oauth2.GenerateVerifier(), Nonce: oauth2.GenerateVerifier(), Verifier: oauth2.GenerateVerifier()}
}

// AuthURL is where to send the browser to sign in.
func (c *Client) AuthURL(ctx context.Context, p Provider, redirectURL string, st Start) (string, error) {
	cfg, err := c.config(ctx, p, redirectURL)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(st.Verifier)}
	if p.Kind != GitHub {
		opts = append(opts, oidc.Nonce(st.Nonce))
	}
	return cfg.AuthCodeURL(st.State, opts...), nil
}

// Finish trades the code the provider sent back for who signed in.
func (c *Client) Finish(ctx context.Context, p Provider, redirectURL, code string, st Start) (Claims, error) {
	cfg, err := c.config(ctx, p, redirectURL)
	if err != nil {
		return Claims{}, err
	}
	tok, err := cfg.Exchange(c.ctx(ctx), code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("sso: exchange code: %w", err)
	}
	if p.Kind == GitHub {
		return c.github(ctx, p, tok)
	}
	return c.oidc(ctx, p, cfg, tok, st.Nonce)
}

func (c *Client) oidc(ctx context.Context, p Provider, cfg *oauth2.Config, tok *oauth2.Token, nonce string) (Claims, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Claims{}, errors.New("sso: no id_token in the answer")
	}
	op, err := c.discover(ctx, p.IssuerURL())
	if err != nil {
		return Claims{}, err
	}
	idt, err := op.Verifier(&oidc.Config{ClientID: p.ClientID}).Verify(c.ctx(ctx), raw)
	if err != nil {
		return Claims{}, fmt.Errorf("sso: verify id_token: %w", err)
	}
	if idt.Nonce != nonce {
		return Claims{}, errors.New("sso: id_token nonce doesn't match")
	}
	var all map[string]any
	if err := idt.Claims(&all); err != nil {
		return Claims{}, fmt.Errorf("sso: read id_token: %w", err)
	}
	out := Claims{Issuer: p.IssuerURL(), Subject: idt.Subject}
	out.Email, _ = all["email"].(string)
	out.EmailVerified = verified(all["email_verified"])
	out.Name, _ = all["name"].(string)
	claim := p.GroupsClaim
	if claim == "" {
		claim = "groups"
	}
	out.Groups = strList(all[claim])
	// Some providers keep the address out of the ID token; their userinfo
	// endpoint has it.
	if out.Email == "" && op.UserInfoEndpoint() != "" {
		info, err := op.UserInfo(c.ctx(ctx), cfg.TokenSource(c.ctx(ctx), tok))
		if err == nil && info.Subject == out.Subject {
			out.Email, out.EmailVerified = info.Email, info.EmailVerified
		}
	}
	return out, nil
}

// verified reads email_verified, which some providers send as a string.
func verified(v any) bool {
	switch v := v.(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

func strList(v any) []string {
	switch v := v.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{v}
	}
	return nil
}

func (c *Client) github(ctx context.Context, p Provider, tok *oauth2.Token) (Claims, error) {
	var u struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.githubGet(ctx, tok, "/user", &u); err != nil {
		return Claims{}, err
	}
	if u.ID == 0 {
		return Claims{}, errors.New("sso: GitHub sent no user id")
	}
	out := Claims{Issuer: GitHubIssuer, Subject: strconv.FormatInt(u.ID, 10), Name: u.Name}
	if out.Name == "" {
		out.Name = u.Login
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.githubGet(ctx, tok, "/user/emails", &emails); err != nil {
		return Claims{}, err
	}
	for _, e := range emails {
		if e.Primary {
			out.Email, out.EmailVerified = e.Email, e.Verified
		}
	}
	if p.wantsGroups() {
		var orgs []struct {
			Login string `json:"login"`
		}
		if err := c.githubGet(ctx, tok, "/user/orgs?per_page=100", &orgs); err != nil {
			return Claims{}, err
		}
		for _, o := range orgs {
			out.Groups = append(out.Groups, o.Login)
		}
	}
	return out, nil
}

func (c *Client) githubGet(ctx context.Context, tok *oauth2.Token, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GitHubAPI+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("sso: GitHub %s: %w", path, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sso: GitHub %s: %s", path, res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(into)
}

// CheckIssuer says what's wrong with an OIDC issuer address, if anything:
// it has to be https, except on this machine for trying things out.
func CheckIssuer(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Give the provider's issuer address, such as https://login.example.com.")
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"):
		return nil
	}
	return errors.New("The issuer address needs https.")
}
