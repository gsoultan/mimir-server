package server_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/go-jose/go-jose/v4"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
)

// fakeIdP is a small OpenID Connect provider: it signs in whoever the
// test says is next, and checks PKCE like a real one.
type fakeIdP struct {
	*httptest.Server
	t    *testing.T
	key  *rsa.PrivateKey
	mu   sync.Mutex
	next map[string]any // the claims the next sign-in gets
	// codes are the codes handed out, with their nonce and challenge.
	codes map[string][2]string
}

func newIdP(t *testing.T) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{t: t, key: key, codes: map[string][2]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": idp.URL, "authorization_endpoint": idp.URL + "/auth", "token_endpoint": idp.URL + "/token",
			"jwks_uri": idp.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "mimir-test" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		code := rand.Text()
		idp.mu.Lock()
		idp.codes[code] = [2]string{q.Get("nonce"), q.Get("code_challenge")}
		idp.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}}.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		idp.mu.Lock()
		got, ok := idp.codes[r.Form.Get("code")]
		delete(idp.codes, r.Form.Get("code"))
		claims := map[string]any{}
		for k, v := range idp.next {
			claims[k] = v
		}
		idp.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != got[1] {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		now := time.Now()
		claims["iss"], claims["aud"], claims["nonce"] = idp.URL, "mimir-test", got[0]
		claims["iat"], claims["exp"] = now.Unix(), now.Add(time.Hour).Unix()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer", "id_token": idp.sign(claims)})
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

func (idp *fakeIdP) sign(claims map[string]any) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: idp.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		idp.t.Fatal(err)
	}
	b, _ := json.Marshal(claims)
	obj, err := signer.Sign(b)
	if err != nil {
		idp.t.Fatal(err)
	}
	s, _ := obj.CompactSerialize()
	return s
}

func (idp *fakeIdP) as(claims map[string]any) {
	idp.mu.Lock()
	idp.next = claims
	idp.mu.Unlock()
}

// browser follows redirects and keeps cookies, like the real one.
type browser struct {
	*http.Client
	auth mimirv1connect.AuthServiceClient
}

func newBrowser(t *testing.T, srv *httptest.Server) browser {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	return browser{Client: c, auth: mimirv1connect.NewAuthServiceClient(c, srv.URL)}
}

// signInWith goes through the provider and returns where the browser
// ended up.
func (b browser) signInWith(t *testing.T, srv *httptest.Server, id string) *url.URL {
	t.Helper()
	res, err := b.Get(srv.URL + "/auth/sso/" + id + "/start?next=/models")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.Request.URL
}

// crossSite marks the way back from the provider as coming from another
// site, as a browser does.
type crossSite struct{ next http.RoundTripper }

func (c crossSite) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/callback") {
		r = r.Clone(r.Context())
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	}
	return c.next.RoundTrip(r)
}

// TestSignInWithProvider signs people in with OpenID Connect: new people
// land in the team their domain says, existing accounts link by checked
// email, and anyone else is turned away.
func TestSignInWithProvider(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	idp := newIdP(t)
	signins := mimirv1connect.NewSignInServiceClient(srv.Client(), srv.URL, connect.WithInterceptors(own.jar))

	acme, err := own.teams.CreateTeam(ctx, connect.NewRequest(&mimirv1.CreateTeamRequest{Name: "Acme"}))
	if err != nil {
		t.Fatal(err)
	}
	acmeID := acme.Msg.GetTeam().GetId()

	// Nothing to offer yet, and the list is open to anyone.
	anyone := mimirv1connect.NewSignInServiceClient(srv.Client(), srv.URL)
	if opts, err := anyone.ListSignInOptions(ctx, connect.NewRequest(&mimirv1.ListSignInOptionsRequest{})); err != nil || len(opts.Msg.GetOptions()) != 0 {
		t.Fatalf("options = %v, %v", opts, err)
	}

	provider := &mimirv1.SignInProvider{
		Id: "acme", Kind: mimirv1.SignInKind_SIGN_IN_KIND_OIDC, Name: "Acme staff", Enabled: true,
		Issuer: idp.URL, ClientId: "mimir-test", ClientSecret: "s3cret",
		Rules: []*mimirv1.SignInRule{{Domain: "acme.test", TeamId: acmeID, Role: mimirv1.Role_ROLE_EDITOR}},
	}
	// Owners can't be handed out, and rules need a real team.
	bad := []*mimirv1.SignInProvider{
		{Id: "Acme!", Kind: provider.Kind, Name: "x", Issuer: idp.URL, ClientId: "c", ClientSecret: "s"},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: "http://idp.example.com", ClientId: "c", ClientSecret: "s"},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: idp.URL, ClientId: "c"},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: idp.URL, ClientId: "c", ClientSecret: "s", Rules: []*mimirv1.SignInRule{{Domain: "acme.test", TeamId: acmeID, Role: mimirv1.Role_ROLE_OWNER}}},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: idp.URL, ClientId: "c", ClientSecret: "s", Rules: []*mimirv1.SignInRule{{Domain: "acme.test", Role: mimirv1.Role_ROLE_VIEWER}}},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: idp.URL, ClientId: "c", ClientSecret: "s", Rules: []*mimirv1.SignInRule{{Domain: "acme.test", Group: "g", TeamId: acmeID, Role: mimirv1.Role_ROLE_VIEWER}}},
		{Id: "x", Kind: provider.Kind, Name: "x", Issuer: srv.URL + "/nothing-here", ClientId: "c", ClientSecret: "s", Enabled: true},
	}
	for _, p := range bad {
		if _, err := signins.SaveSignInProvider(ctx, connect.NewRequest(&mimirv1.SaveSignInProviderRequest{Provider: p})); code(err) != connect.CodeInvalidArgument {
			t.Errorf("SaveSignInProvider(%v) = %v", p, err)
		}
	}
	if _, err := signins.SaveSignInProvider(ctx, connect.NewRequest(&mimirv1.SaveSignInProviderRequest{Provider: provider})); err != nil {
		t.Fatal(err)
	}
	// Saving again without the secret keeps it; it never comes back out.
	provider.ClientSecret = ""
	if _, err := signins.SaveSignInProvider(ctx, connect.NewRequest(&mimirv1.SaveSignInProviderRequest{Provider: provider})); err != nil {
		t.Fatal(err)
	}
	list, err := signins.ListSignInProviders(ctx, connect.NewRequest(&mimirv1.ListSignInProvidersRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if ps := list.Msg.GetProviders(); len(ps) != 1 || ps[0].GetClientSecret() != "" || !ps[0].GetClientSecretSet() || ps[0].GetRules()[0].GetTeamName() != "Acme" {
		t.Fatalf("providers = %v", ps)
	}
	if opts, _ := anyone.ListSignInOptions(ctx, connect.NewRequest(&mimirv1.ListSignInOptionsRequest{})); len(opts.Msg.GetOptions()) != 1 || opts.Msg.GetOptions()[0].GetName() != "Acme staff" {
		t.Fatalf("options = %v", opts.Msg.GetOptions())
	}

	// Someone new from acme.test gets an editor account in Acme, and lands
	// where they were going.
	idp.as(map[string]any{"sub": "u-1", "email": "Pat@acme.test", "email_verified": true, "name": "Pat"})
	pat := newBrowser(t, srv)
	if at := pat.signInWith(t, srv, "acme"); at.Path != "/models" {
		t.Fatalf("landed on %v", at)
	}
	who, err := pat.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if u := who.Msg.GetUser(); u.GetEmail() != "pat@acme.test" || u.GetName() != "Pat" || u.GetTeamId() != acmeID || u.GetAccess().GetRole() != mimirv1.Role_ROLE_EDITOR {
		t.Fatalf("new person = %v", u)
	}
	// The next time, the same subject is the same account, even with a
	// new address.
	idp.as(map[string]any{"sub": "u-1", "email": "pat.smith@elsewhere.test", "email_verified": true})
	again := newBrowser(t, srv)
	again.signInWith(t, srv, "acme")
	if w, err := again.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil || w.Msg.GetUser().GetId() != who.Msg.GetUser().GetId() {
		t.Fatalf("second sign-in = %v, %v", w, err)
	}
	// A password nobody knows: the form stays shut.
	if _, err := newClients(srv).auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "pat@acme.test", Password: password})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("password sign-in = %v", err)
	}

	refused := func(name string, claims map[string]any) {
		t.Helper()
		idp.as(claims)
		b := newBrowser(t, srv)
		at := b.signInWith(t, srv, "acme")
		if at.Path != "/sign-in" || at.Query().Get("error") == "" {
			t.Errorf("%s: landed on %v", name, at)
		}
		if _, err := b.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: signed in: %v", name, err)
		}
	}
	refused("other domain", map[string]any{"sub": "u-2", "email": "sam@other.test", "email_verified": true})
	refused("unchecked email", map[string]any{"sub": "u-3", "email": "owner@example.com"})

	// An existing account is found by its checked address and tied to the
	// identity from then on.
	idp.as(map[string]any{"sub": "u-4", "email": "owner@example.com", "email_verified": true})
	ob := newBrowser(t, srv)
	ob.signInWith(t, srv, "acme")
	if w, err := ob.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil || w.Msg.GetUser().GetEmail() != "owner@example.com" {
		t.Fatalf("owner by email = %v, %v", w, err)
	}

	// Coming back is a cross-site navigation, and a browser that is
	// already signed in sends its cookie along; that isn't refused.
	pat.Transport = crossSite{http.DefaultTransport}
	idp.as(map[string]any{"sub": "u-1", "email": "pat@acme.test", "email_verified": true})
	if at := pat.signInWith(t, srv, "acme"); at.Path != "/models" {
		t.Fatalf("signed-in browser landed on %v", at)
	}
	pat.Transport = nil

	// Two-step verification still asks for its code.
	start, err := own.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	totp := func() string {
		c, _ := auth.TOTPCode(start.Msg.GetSecret(), auth.TOTPStep(time.Now()))
		return c
	}
	if _, err := own.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: totp()})); err != nil {
		t.Fatal(err)
	}
	idp.as(map[string]any{"sub": "u-4", "email": "owner@example.com", "email_verified": true})
	ob = newBrowser(t, srv)
	if at := ob.signInWith(t, srv, "acme"); at.Path != "/sign-in" || at.Query().Get("step") != "code" || at.Query().Get("next") != "/models" {
		t.Fatalf("two-step sign-in landed on %v", at)
	}
	if _, err := ob.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("pending WhoAmI: %v", err)
	}
	// The code can't be the one EnableTwoFactor just used.
	time.Sleep(time.Until(time.Unix((auth.TOTPStep(time.Now())+1)*30, 0)))
	if _, err := ob.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: totp()})); err != nil {
		t.Fatal(err)
	}
	if w, err := ob.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil || w.Msg.GetUser().GetEmail() != "owner@example.com" {
		t.Fatalf("after code = %v, %v", w, err)
	}

	// A callback that didn't start here, or with someone else's state, is
	// turned away.
	stranger := newBrowser(t, srv)
	res, err := stranger.Get(srv.URL + "/auth/sso/acme/callback?code=x&state=y")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Request.URL.Path != "/sign-in" || res.Request.URL.Query().Get("error") == "" {
		t.Fatalf("forged callback landed on %v", res.Request.URL)
	}

	// Only owners see or change the providers.
	editor := mimirv1connect.NewSignInServiceClient(pat.Client, srv.URL)
	if _, err := editor.ListSignInProviders(ctx, connect.NewRequest(&mimirv1.ListSignInProvidersRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("editor lists providers: %v", err)
	}

	// Turned off, the button goes and so does the way in.
	provider.Enabled = false
	if _, err := signins.SaveSignInProvider(ctx, connect.NewRequest(&mimirv1.SaveSignInProviderRequest{Provider: provider})); err != nil {
		t.Fatal(err)
	}
	if opts, _ := anyone.ListSignInOptions(ctx, connect.NewRequest(&mimirv1.ListSignInOptionsRequest{})); len(opts.Msg.GetOptions()) != 0 {
		t.Fatalf("options after turning off = %v", opts.Msg.GetOptions())
	}
	idp.as(map[string]any{"sub": "u-1", "email": "pat@acme.test", "email_verified": true})
	if at := newBrowser(t, srv).signInWith(t, srv, "acme"); !strings.HasPrefix(at.Path, "/sign-in") {
		t.Fatalf("turned-off provider landed on %v", at)
	}
	if _, err := signins.DeleteSignInProvider(ctx, connect.NewRequest(&mimirv1.DeleteSignInProviderRequest{Id: "acme"})); err != nil {
		t.Fatal(err)
	}
	if list, _ := signins.ListSignInProviders(ctx, connect.NewRequest(&mimirv1.ListSignInProvidersRequest{})); len(list.Msg.GetProviders()) != 0 {
		t.Fatalf("providers after delete = %v", list.Msg.GetProviders())
	}
}
