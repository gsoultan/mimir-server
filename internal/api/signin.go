package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/sso"
	"github.com/gsoultan/mimir-server/internal/store"
)

var _ mimirv1connect.SignInServiceHandler = (*Service)(nil)

// Sign-in with an outside account. Owners set up providers in Settings;
// the sign-in page shows a button for each one turned on. The browser
// leaves at /auth/sso/{id}/start and comes back to /auth/sso/{id}/callback,
// which ends like a password sign-in: a session cookie, or the half-way
// one when the account has two-step verification.
//
// Who the provider vouches for becomes an account in one of three ways:
// the identity was tied to an account before; an account has the email
// address the provider checked, and the identity is tied to it now; or a
// rule of the provider's matches their domain or group, and a new account
// is made in the rule's team. Anyone else is turned away.

// KeySignIn is the settings key for the providers. It is sealed, since
// it holds their client secrets.
const KeySignIn = "signin.providers"

// SSOPath is where the sign-in redirects live.
const SSOPath = "/auth/sso/"

// ssoCookie carries one sign-in's state from start to callback.
const ssoCookie = "mimir_sso"

// ssoTTL is how long someone has at the provider.
const ssoTTL = 10 * time.Minute

// maxProviders bounds how many providers one server keeps.
const maxProviders = 20

var providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// signInMu serialises changes to the providers, which are one setting.
var signInMu sync.Mutex

// LoadProviders reads the sign-in providers.
func LoadProviders(ctx context.Context, st *store.Store) ([]sso.Provider, error) {
	raw, err := st.GetSetting(ctx, KeySignIn)
	if err != nil || raw == "" {
		return nil, err
	}
	var ps []sso.Provider
	return ps, json.Unmarshal([]byte(raw), &ps)
}

func saveProviders(ctx context.Context, st *store.Store, ps []sso.Provider) error {
	b, err := json.Marshal(ps)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, KeySignIn, string(b))
}

func (s *Service) provider(ctx context.Context, id string) (sso.Provider, bool, error) {
	ps, err := LoadProviders(ctx, s.store)
	if err != nil {
		return sso.Provider{}, false, err
	}
	for _, p := range ps {
		if p.ID == id {
			return p, true, nil
		}
	}
	return sso.Provider{}, false, nil
}

func (s *Service) ListSignInOptions(ctx context.Context, _ *connect.Request[mimirv1.ListSignInOptionsRequest]) (*connect.Response[mimirv1.ListSignInOptionsResponse], error) {
	ps, err := LoadProviders(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListSignInOptionsResponse{}
	for _, p := range ps {
		if p.Enabled {
			res.Options = append(res.Options, &mimirv1.SignInOption{Id: p.ID, Name: p.Name, Kind: mimirv1.SignInKind(p.Kind)})
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ListSignInProviders(ctx context.Context, _ *connect.Request[mimirv1.ListSignInProvidersRequest]) (*connect.Response[mimirv1.ListSignInProvidersResponse], error) {
	ps, err := LoadProviders(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.teamNames(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListSignInProvidersResponse{}
	for _, p := range ps {
		res.Providers = append(res.Providers, providerProto(p, names))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) SaveSignInProvider(ctx context.Context, req *connect.Request[mimirv1.SaveSignInProviderRequest]) (*connect.Response[mimirv1.SaveSignInProviderResponse], error) {
	p, _ := auth.From(ctx)
	signInMu.Lock()
	defer signInMu.Unlock()
	ps, err := LoadProviders(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	in := req.Msg.GetProvider()
	at := -1
	for i, q := range ps {
		if q.ID == in.GetId() {
			at = i
		}
	}
	var prev *sso.Provider
	if at >= 0 {
		prev = &ps[at]
	} else if len(ps) >= maxProviders {
		return nil, invalid("A server can have up to %d ways to sign in.", maxProviders)
	}
	out, err := s.cleanProvider(ctx, in, prev)
	if err != nil {
		return nil, err
	}
	if out.Enabled {
		if err := s.Providers.Check(ctx, out); err != nil {
			s.log.WarnContext(ctx, "sign-in provider unreachable", "provider", out.ID, "error", err)
			return nil, invalid("Couldn't reach %s at %s to read its settings. Check the issuer address.", out.Name, out.IssuerURL())
		}
	}
	action := "settings.signin_add"
	if at >= 0 {
		ps[at] = out
		action = "settings.signin_change"
	} else {
		ps = append(ps, out)
	}
	if err := saveProviders(ctx, s.store, ps); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), action, out.Name, onOff(out.Enabled))
	names, err := s.teamNames(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.SaveSignInProviderResponse{Provider: providerProto(out, names)}), nil
}

func (s *Service) DeleteSignInProvider(ctx context.Context, req *connect.Request[mimirv1.DeleteSignInProviderRequest]) (*connect.Response[mimirv1.DeleteSignInProviderResponse], error) {
	p, _ := auth.From(ctx)
	signInMu.Lock()
	defer signInMu.Unlock()
	ps, err := LoadProviders(ctx, s.store)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	for i, q := range ps {
		if q.ID == req.Msg.GetId() {
			if err := saveProviders(ctx, s.store, append(ps[:i:i], ps[i+1:]...)); err != nil {
				return nil, s.internal(ctx, err)
			}
			// Identities stay tied to their accounts, so adding the provider
			// back signs the same people in to the same accounts.
			s.audit(ctx, p.Actor(), "settings.signin_remove", q.Name, "")
			return connect.NewResponse(&mimirv1.DeleteSignInProviderResponse{}), nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("No such way to sign in."))
}

// cleanProvider checks a provider from an owner. prev is the saved one it
// changes, if any, whose client secret stays unless a new one is given.
func (s *Service) cleanProvider(ctx context.Context, in *mimirv1.SignInProvider, prev *sso.Provider) (sso.Provider, error) {
	out := sso.Provider{ID: in.GetId(), Kind: sso.Kind(in.GetKind()), Enabled: in.GetEnabled(), ClientID: strings.TrimSpace(in.GetClientId())}
	if !providerID.MatchString(out.ID) {
		return out, invalid("Give it a short id of lowercase letters, digits and dashes, such as google.")
	}
	if prev != nil && prev.Kind != out.Kind {
		return out, invalid("A provider can't change kind. Add a new one instead.")
	}
	name, err := cleanName("way to sign in", in.GetName())
	if err != nil {
		return out, err
	}
	out.Name = name
	switch out.Kind {
	case sso.Google, sso.GitHub:
	case sso.OIDC:
		out.Issuer = strings.TrimRight(strings.TrimSpace(in.GetIssuer()), "/")
		if err := sso.CheckIssuer(out.Issuer); err != nil {
			return out, refused(err)
		}
		out.GroupsClaim = strings.TrimSpace(in.GetGroupsClaim())
		out.TrustEmails = in.GetTrustEmails()
		if len(out.GroupsClaim) > 100 {
			return out, invalid("That groups claim is too long.")
		}
	default:
		return out, invalid("Pick Google, GitHub or OpenID Connect.")
	}
	if out.ClientID == "" || len(out.ClientID) > 500 {
		return out, invalid("Give the client id from %s.", out.Name)
	}
	out.ClientSecret = strings.TrimSpace(in.GetClientSecret())
	if out.ClientSecret == "" && prev != nil {
		out.ClientSecret = prev.ClientSecret
	}
	if out.ClientSecret == "" || len(out.ClientSecret) > 1000 {
		return out, invalid("Give the client secret from %s.", out.Name)
	}
	if len(in.GetRules()) > 100 {
		return out, invalid("A provider can have up to 100 rules.")
	}
	names, err := s.teamNames(ctx)
	if err != nil {
		return out, s.internal(ctx, err)
	}
	for _, r := range in.GetRules() {
		rule := sso.Rule{
			Domain: strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.GetDomain()), "@")),
			Group:  strings.TrimSpace(r.GetGroup()),
			TeamID: r.GetTeamId(),
			Role:   int(r.GetRole()),
		}
		switch {
		case (rule.Domain == "") == (rule.Group == ""):
			return out, invalid("Each rule needs an email domain or a group, not both.")
		case rule.Domain != "" && (len(rule.Domain) > 253 || !strings.Contains(rule.Domain, ".") || strings.ContainsAny(rule.Domain, "@ /")):
			return out, invalid("%q doesn't look like an email domain, such as example.com.", rule.Domain)
		case len(rule.Group) > 200:
			return out, invalid("That group name is too long.")
		case names[rule.TeamID] == "":
			return out, invalid("Pick a team for each rule.")
		case store.Role(rule.Role) < store.Viewer || store.Role(rule.Role) > store.Admin:
			// New people never arrive as owners; an owner makes owners.
			return out, invalid("New people can be viewers, editors or admins.")
		}
		out.Rules = append(out.Rules, rule)
	}
	return out, nil
}

func providerProto(p sso.Provider, teams map[string]string) *mimirv1.SignInProvider {
	out := &mimirv1.SignInProvider{
		Id: p.ID, Kind: mimirv1.SignInKind(p.Kind), Name: p.Name, Enabled: p.Enabled, Issuer: p.Issuer,
		ClientId: p.ClientID, ClientSecretSet: p.ClientSecret != "", GroupsClaim: p.GroupsClaim, TrustEmails: p.TrustEmails,
	}
	for _, r := range p.Rules {
		out.Rules = append(out.Rules, &mimirv1.SignInRule{Domain: r.Domain, Group: r.Group, TeamId: r.TeamID, Role: mimirv1.Role(r.Role), TeamName: teams[r.TeamID]})
	}
	return out
}

// The redirects

// ssoState is what the cookie carries between start and callback.
type ssoState struct {
	sso.Start
	Provider string `json:"p"`
	Next     string `json:"x,omitempty"`
}

// SSOHandler serves the sign-in redirects under SSOPath.
func (s *Service) SSOHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+SSOPath+"{id}/start", s.startSSO)
	mux.HandleFunc("GET "+SSOPath+"{id}/callback", s.finishSSO)
	return mux
}

// httpsOf reports whether r reached us over HTTPS, as WithConn noted it.
func (s *Service) httpsOf(r *http.Request) bool {
	if c, ok := r.Context().Value(connKey{}).(conn); ok {
		return c.https()
	}
	return s.Proxies.ForwardedHTTPS(r.Header, r.RemoteAddr)
}

// callbackURL is where the provider sends the browser back. Providers
// only send people to addresses registered with them, so a made-up Host
// header leads nowhere.
func (s *Service) callbackURL(r *http.Request, id string) string {
	scheme := "http"
	if s.httpsOf(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + SSOPath + id + "/callback"
}

// safeNext keeps a path on this site, never an outside address.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") || len(next) > 2000 {
		return "/"
	}
	return next
}

// ssoFail sends the browser back to the sign-in page with a sentence to
// show.
func ssoFail(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/sign-in?"+url.Values{"error": {msg}}.Encode(), http.StatusSeeOther)
}

func (s *Service) startSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok, err := s.provider(ctx, r.PathValue("id"))
	if err != nil {
		s.log.ErrorContext(ctx, "sign-in providers", "error", err)
		ssoFail(w, r, "Something went wrong on the server. Try again.")
		return
	}
	if !ok || !p.Enabled {
		ssoFail(w, r, "That way to sign in isn't on any more.")
		return
	}
	st := ssoState{Start: sso.NewStart(), Provider: p.ID, Next: safeNext(r.URL.Query().Get("next"))}
	to, err := s.Providers.AuthURL(ctx, p, s.callbackURL(r, p.ID), st.Start)
	if err != nil {
		s.log.WarnContext(ctx, "sign-in provider unreachable", "provider", p.ID, "error", err)
		ssoFail(w, r, "Couldn't reach "+p.Name+". Try again, or sign in with your password.")
		return
	}
	b, _ := json.Marshal(st)
	// Lax, so the cookie comes back on the provider's redirect; scoped to
	// these addresses and gone once used.
	http.SetCookie(w, &http.Cookie{
		Name: ssoCookie, Value: base64.RawURLEncoding.EncodeToString(b), Path: SSOPath,
		HttpOnly: true, Secure: s.httpsOf(r), SameSite: http.SameSiteLaxMode, MaxAge: int(ssoTTL / time.Second),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Service) finishSSO(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Cache-Control", "no-store")
	http.SetCookie(w, &http.Cookie{Name: ssoCookie, Path: SSOPath, HttpOnly: true, Secure: s.httpsOf(r), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	var st ssoState
	if c, err := r.Cookie(ssoCookie); err == nil {
		if b, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			json.Unmarshal(b, &st)
		}
	}
	id, q := r.PathValue("id"), r.URL.Query()
	if st.State == "" || st.Provider != id || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		ssoFail(w, r, "That sign-in took too long or started somewhere else. Try again.")
		return
	}
	p, ok, err := s.provider(ctx, id)
	if err != nil {
		s.log.ErrorContext(ctx, "sign-in providers", "error", err)
		ssoFail(w, r, "Something went wrong on the server. Try again.")
		return
	}
	if !ok || !p.Enabled {
		ssoFail(w, r, "That way to sign in isn't on any more.")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		ssoFail(w, r, p.Name+" didn't sign you in. Try again, or sign in with your password.")
		return
	}
	ipKey := "ip:" + s.Proxies.ClientIP(r.Header, r.RemoteAddr)
	if wait := s.Limiter.Wait(ipKey); wait > 0 {
		ssoFail(w, r, "Too many attempts. Try again in a few minutes.")
		return
	}
	cl, err := s.Providers.Finish(ctx, p, s.callbackURL(r, p.ID), q.Get("code"), st.Start)
	if err != nil {
		s.Limiter.Fail(ipKey)
		s.log.WarnContext(ctx, "sign-in with provider failed", "provider", p.ID, "error", err)
		ssoFail(w, r, "Couldn't sign you in with "+p.Name+". Try again; if it keeps happening, an owner should check its settings.")
		return
	}
	u, err := s.ssoAccount(ctx, p, cl)
	if err != nil {
		var refuse ssoRefusal
		if errors.As(err, &refuse) {
			s.auditIn(ctx, "", cl.Email, "user.signin_failed", cl.Email, "with "+p.Name+": "+string(refuse))
			ssoFail(w, r, string(refuse))
			return
		}
		s.log.ErrorContext(ctx, "sign-in with provider", "provider", p.ID, "error", err)
		ssoFail(w, r, "Something went wrong on the server. Try again.")
		return
	}
	if u.Disabled {
		ssoFail(w, r, "This account is disabled. Ask an admin to enable it.")
		return
	}
	if old := auth.SessionToken(r.Header); old != "" {
		s.store.DeleteSession(ctx, auth.HashToken(old))
	}
	// Two-step verification still applies: the provider proves who you
	// are there, the code that this account is in your hands here.
	pending := u.TwoFactor()
	if _, err := s.startSession(ctx, r.Header, r.RemoteAddr, s.httpsOf(r), w.Header(), u, pending); err != nil {
		s.log.ErrorContext(ctx, "start session", "error", err)
		ssoFail(w, r, "Something went wrong on the server. Try again.")
		return
	}
	if pending {
		http.Redirect(w, r, "/sign-in?"+url.Values{"step": {"code"}, "next": {st.Next}}.Encode(), http.StatusSeeOther)
		return
	}
	where := s.Proxies.ClientIP(r.Header, r.RemoteAddr) + ", " + strings.ToLower(auth.Device(r.Header.Get("User-Agent")))
	s.auditIn(ctx, u.TeamID, u.Email, "user.signin", u.Email, "with "+p.Name+", "+where)
	http.Redirect(w, r, safeNext(st.Next), http.StatusSeeOther)
}

// ssoRefusal is a reason to turn someone away, written for them.
type ssoRefusal string

func (e ssoRefusal) Error() string { return string(e) }

// ssoAccount finds or makes the account for who the provider vouched for.
func (s *Service) ssoAccount(ctx context.Context, p sso.Provider, cl sso.Claims) (store.User, error) {
	ident := store.Identity{Issuer: cl.Issuer, Subject: cl.Subject, Email: auth.NormalizeEmail(cl.Email)}
	if ident.Issuer == "" || ident.Subject == "" {
		return store.User{}, errors.New("provider gave no subject")
	}
	u, err := s.store.UserByIdentity(ctx, ident)
	if !errors.Is(err, store.ErrNotFound) {
		return u, err
	}
	email := ident.Email
	if email == "" || !(cl.EmailVerified || (p.Kind == sso.OIDC && p.TrustEmails)) {
		return store.User{}, ssoRefusal(p.Name + " didn't share an email address it has checked, so there's no telling which account is yours.")
	}
	u, err = s.store.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		if err := s.store.LinkIdentity(ctx, u.ID, ident); err != nil && !errors.Is(err, store.ErrIdentityTaken) {
			return store.User{}, err
		}
		s.auditIn(ctx, u.TeamID, u.Email, "user.signin_linked", p.Name, "")
		return u, nil
	case !errors.Is(err, store.ErrNotFound):
		return store.User{}, err
	}
	rule, ok := p.Match(email, cl.Groups)
	if !ok {
		return store.User{}, ssoRefusal("There's no account for " + email + " on this server. Ask an admin to add you.")
	}
	if _, err := s.store.GetTeam(ctx, rule.TeamID); errors.Is(err, store.ErrNotFound) {
		return store.User{}, ssoRefusal("The team for new people from " + p.Name + " no longer exists. Ask an owner to fix the sign-in settings.")
	} else if err != nil {
		return store.User{}, err
	}
	u, err = s.createSSOUser(ctx, p, rule, email, cl.Name)
	if errors.Is(err, store.ErrEmailTaken) {
		// Signed up twice at once; the other one made it.
		u, err = s.store.GetUserByEmail(ctx, email)
	}
	if err != nil {
		return store.User{}, err
	}
	if err := s.store.LinkIdentity(ctx, u.ID, ident); err != nil && !errors.Is(err, store.ErrIdentityTaken) {
		return store.User{}, err
	}
	return u, nil
}

func (s *Service) createSSOUser(ctx context.Context, p sso.Provider, rule sso.Rule, email, name string) (store.User, error) {
	if _, _, err := cleanUser(email, "x"); err != nil {
		return store.User{}, ssoRefusal(p.Name + " gave an email address this server can't use.")
	}
	name = strings.TrimSpace(name)
	if n, err := cleanName("person", name); err == nil {
		name = n
	} else {
		name, _, _ = strings.Cut(email, "@")
		if len(name) > 100 {
			name = name[:100]
		}
	}
	// They sign in through the provider; a password nobody knows keeps
	// the password form shut until an admin sets one.
	hash, err := auth.HashPassword(rand.Text())
	if err != nil {
		return store.User{}, err
	}
	access := store.Access{Role: store.Role(rule.Role), AllModels: true}
	u, err := s.store.CreateUser(ctx, store.User{Email: email, Name: name, PasswordHash: hash, Access: access, TeamID: rule.TeamID})
	if err != nil {
		return store.User{}, err
	}
	actor := "sign-in with " + p.Name
	s.auditIn(ctx, u.TeamID, actor, "user.create", u.Email, describeAccess(u.Access))
	s.emitIn(ctx, u.TeamID, "user.created", map[string]any{"user": map[string]any{"id": u.ID, "email": u.Email, "name": u.Name}, "actor": actor})
	return u, nil
}
