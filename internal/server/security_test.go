package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
)

// withHeader is a client option that sends one fixed header.
func withHeader(name, value string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set(name, value)
			return next(ctx, req)
		}
	}))
}

func TestTwoFactorSignIn(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	// Turning it on needs the password, then a code from the new secret.
	_, err := own.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: "wrong wrong wrong"}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("start with wrong password: %v", err)
	}
	start, err := own.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: password}))
	if err != nil || !strings.HasPrefix(start.Msg.GetOtpauthUrl(), "otpauth://totp/mimir:owner@example.com?") {
		t.Fatalf("StartTwoFactor = %v, %v", start, err)
	}
	_, err = own.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: "000000"}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("enable with wrong code: %v", err)
	}
	// Until a code confirms it, sign-in is still password only.
	if _, err := signInRes(srv, "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	totp := func() string {
		c, _ := auth.TOTPCode(start.Msg.GetSecret(), auth.TOTPStep(time.Now()))
		return c
	}
	en, err := own.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: totp()}))
	if err != nil || len(en.Msg.GetRecoveryCodes()) != auth.RecoveryCodes {
		t.Fatalf("EnableTwoFactor = %v, %v", en, err)
	}
	me, _ := own.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if !me.Msg.GetUser().GetTwoFactor() {
		t.Fatal("WhoAmI: two_factor false")
	}

	// Sign in: password gives a pending cookie that can't do anything else.
	c := newClients(srv)
	res, err := c.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password}))
	if err != nil || !res.Msg.GetSecondFactorRequired() || res.Msg.GetUser() != nil || c.jar.cookie == "" {
		t.Fatalf("SignIn = %v, %v", res, err)
	}
	pending := c.jar.cookie
	if _, err := c.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("pending WhoAmI: %v", err)
	}
	if _, err := c.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("pending ListModels: %v", err)
	}
	_, err = c.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: "123456"}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong code: %v", err)
	}
	// The code that enabled 2FA was spent; the next step's code works. Use
	// the next step directly so the test doesn't have to wait.
	next, _ := auth.TOTPCode(start.Msg.GetSecret(), auth.TOTPStep(time.Now())+1)
	v, err := c.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: next}))
	if err != nil || v.Msg.GetUser().GetEmail() != "owner@example.com" || v.Msg.GetUsedRecoveryCode() {
		t.Fatalf("VerifySecondFactor = %v, %v", v, err)
	}
	if c.jar.cookie == pending {
		t.Fatal("session token was not rotated after the second factor")
	}
	if _, err := c.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil {
		t.Fatalf("after code: %v", err)
	}
	// The same code can't be replayed by another sign-in.
	c2 := newClients(srv)
	c2.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password}))
	if _, err := c2.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: next})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("replayed code: %v", err)
	}
	// A recovery code works once.
	rc := en.Msg.GetRecoveryCodes()[0]
	v, err = c2.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: strings.ToUpper(rc)}))
	if err != nil || !v.Msg.GetUsedRecoveryCode() || v.Msg.GetRecoveryCodesLeft() != auth.RecoveryCodes-1 {
		t.Fatalf("recovery code: %v, %v", v, err)
	}
	c3 := newClients(srv)
	c3.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password}))
	if _, err := c3.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: rc})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("reused recovery code: %v", err)
	}
	// Guessing codes locks the account for a while.
	for range 12 {
		c3.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: "111111"}))
	}
	if _, err := c3.auth.VerifySecondFactor(ctx, connect.NewRequest(&mimirv1.VerifySecondFactorRequest{Code: "111111"})); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("after many wrong codes: %v", err)
	}
	svc.Limiter.Clear("2fa:"+me.Msg.GetUser().GetId(), "ip:127.0.0.1")

	// An admin can reset it for someone who lost their phone, which also
	// signs them out everywhere.
	_, err = c.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "pat@example.com", Name: "Pat", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	pat := signIn(t, srv, "pat@example.com")
	ps, _ := pat.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: password}))
	pc, _ := auth.TOTPCode(ps.Msg.GetSecret(), auth.TOTPStep(time.Now()))
	if _, err := pat.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: pc})); err != nil {
		t.Fatal(err)
	}
	patID := func() string {
		us, _ := c.users.ListUsers(ctx, connect.NewRequest(&mimirv1.ListUsersRequest{}))
		for _, u := range us.Msg.GetUsers() {
			if u.GetEmail() == "pat@example.com" {
				if !u.GetTwoFactor() {
					t.Fatal("ListUsers: pat two_factor false")
				}
				return u.GetId()
			}
		}
		t.Fatal("pat missing")
		return ""
	}()
	_, err = c.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: patID, Name: "Pat", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}, ResetTwoFactor: true}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pat.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("pat after reset: %v", err)
	}
	if r, err := signInRes(srv, "pat@example.com"); err != nil || r.Msg.GetSecondFactorRequired() {
		t.Fatalf("pat sign-in after reset: %v, %v", r, err)
	}

	// Turning it off needs the password.
	if _, err := c.auth.DisableTwoFactor(ctx, connect.NewRequest(&mimirv1.DisableTwoFactorRequest{Password: password})); err != nil {
		t.Fatal(err)
	}
	if r, err := signInRes(srv, "owner@example.com"); err != nil || r.Msg.GetSecondFactorRequired() {
		t.Fatalf("after disable: %v, %v", r, err)
	}
}

func signInRes(srv *httptest.Server, email string) (*connect.Response[mimirv1.SignInResponse], error) {
	c := newClients(srv)
	return c.auth.SignIn(context.Background(), connect.NewRequest(&mimirv1.SignInRequest{Email: email, Password: password}))
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	chrome := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0.0.0 Safari/537.36"
	own := owner(t, srv)

	// A second browser signs in; both show up, and only one is current.
	other := newClients(srv, withHeader("User-Agent", chrome))
	if _, err := other.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password})); err != nil {
		t.Fatal(err)
	}
	ls, err := own.auth.ListSessions(ctx, connect.NewRequest(&mimirv1.ListSessionsRequest{}))
	if err != nil || len(ls.Msg.GetSessions()) != 2 {
		t.Fatalf("ListSessions = %v, %v", ls, err)
	}
	me, _ := own.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	var current, chromeID string
	for _, se := range ls.Msg.GetSessions() {
		if se.GetCurrent() {
			current = se.GetId()
		} else {
			chromeID = se.GetId()
			if se.GetDevice() != "Chrome on Windows" || se.GetIp() == "" {
				t.Fatalf("session %v", se)
			}
		}
	}
	if current != me.Msg.GetSessionId() || chromeID == "" {
		t.Fatalf("current %q vs %q", current, me.Msg.GetSessionId())
	}

	// A stolen cookie replayed from a different browser ends the session.
	thief := newClients(srv, withHeader("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/131.0"))
	thief.jar.cookie = other.jar.cookie
	if _, err := thief.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("thief: %v", err)
	}
	if _, err := other.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("victim's session should have ended: %v", err)
	}

	// Revoking: one by id, then everything but this one.
	other = newClients(srv, withHeader("User-Agent", chrome))
	other.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password}))
	third := signIn(t, srv, "owner@example.com")
	ls, _ = own.auth.ListSessions(ctx, connect.NewRequest(&mimirv1.ListSessionsRequest{}))
	for _, se := range ls.Msg.GetSessions() {
		if se.GetDevice() == "Chrome on Windows" {
			chromeID = se.GetId()
		}
	}
	if _, err := own.auth.RevokeSessions(ctx, connect.NewRequest(&mimirv1.RevokeSessionsRequest{Id: current})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("revoke self: %v", err)
	}
	r, err := own.auth.RevokeSessions(ctx, connect.NewRequest(&mimirv1.RevokeSessionsRequest{Id: chromeID}))
	if err != nil || r.Msg.GetRevoked() != 1 {
		t.Fatalf("revoke one: %v, %v", r, err)
	}
	if _, err := other.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked chrome: %v", err)
	}
	r, err = own.auth.RevokeSessions(ctx, connect.NewRequest(&mimirv1.RevokeSessionsRequest{}))
	if err != nil || r.Msg.GetRevoked() != 1 {
		t.Fatalf("revoke others: %v, %v", r, err)
	}
	if _, err := third.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("third: %v", err)
	}
	if _, err := own.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil {
		t.Fatalf("own survives: %v", err)
	}

	// Demoting or disabling someone signs them out everywhere.
	cu, _ := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "ed@example.com", Name: "Ed", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	ed := signIn(t, srv, "ed@example.com")
	own.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: cu.Msg.GetUser().GetId(), Name: "Ed", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if _, err := ed.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("demoted editor still signed in: %v", err)
	}

	// Too many wrong passwords lock sign-in for that account.
	for range 10 {
		newClients(srv).auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "ed@example.com", Password: "nope nope nope"}))
	}
	if _, err := newClients(srv).auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "ed@example.com", Password: password})); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("locked out: %v", err)
	}
}

func TestCrossSiteAndHeaders(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	// A request from another site that carries the cookie is refused
	// before any handler runs, even for a read.
	evil := newClients(srv, withHeader("Origin", "https://evil.example"))
	evil.jar.cookie = own.jar.cookie
	if _, err := evil.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("cross-site: %v", err)
	}
	same := newClients(srv, withHeader("Origin", "http://"+strings.TrimPrefix(srv.URL, "http://")))
	same.jar.cookie = own.jar.cookie
	if _, err := same.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil {
		t.Fatalf("same origin: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	for k, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff"} {
		if got := res.Header.Get(k); got != want {
			t.Fatalf("%s = %q", k, got)
		}
	}
	if !strings.Contains(res.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("no CSP")
	}

	// Over HTTPS (behind a proxy) the cookie gets the __Host- prefix.
	tls := newClients(srv, withHeader("X-Forwarded-Proto", "https"))
	if _, err := tls.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: password})); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tls.jar.cookie, "__Host-mimir_session=") {
		t.Fatalf("cookie %q", tls.jar.cookie)
	}
	if _, err := tls.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); err != nil {
		t.Fatalf("secure cookie: %v", err)
	}
}

func TestTwoFactorPolicy(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	require := func(p mimirv1.TwoFactorPolicy) error {
		_, err := own.settings.UpdateSecurity(ctx, connect.NewRequest(&mimirv1.UpdateSecurityRequest{Security: &mimirv1.Security{TwoFactor: p}}))
		return err
	}
	// The owner can't require what they don't have.
	if err := require(mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_ADMINS); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("require without own 2fa: %v", err)
	}
	st, _ := own.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: password}))
	c, _ := auth.TOTPCode(st.Msg.GetSecret(), auth.TOTPStep(time.Now()))
	if _, err := own.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: c})); err != nil {
		t.Fatal(err)
	}
	if err := require(mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_ADMINS); err != nil {
		t.Fatal(err)
	}
	// A viewer is unaffected; an admin without 2FA can only set it up.
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "a@example.com", Name: "A", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN, AllModels: true}}))
	v := signIn(t, srv, "v@example.com")
	if _, err := v.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); err != nil {
		t.Fatalf("viewer: %v", err)
	}
	a := signIn(t, srv, "a@example.com")
	me, err := a.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if err != nil || !me.Msg.GetMustSetUpTwoFactor() {
		t.Fatalf("admin WhoAmI = %v, %v", me, err)
	}
	if _, err := a.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("admin without 2fa: %v", err)
	}
	ast, err := a.auth.StartTwoFactor(ctx, connect.NewRequest(&mimirv1.StartTwoFactorRequest{Password: password}))
	if err != nil {
		t.Fatal(err)
	}
	ac, _ := auth.TOTPCode(ast.Msg.GetSecret(), auth.TOTPStep(time.Now()))
	if _, err := a.auth.EnableTwoFactor(ctx, connect.NewRequest(&mimirv1.EnableTwoFactorRequest{Code: ac})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); err != nil {
		t.Fatalf("admin with 2fa: %v", err)
	}
	// And they can't turn it off while it's required.
	if _, err := a.auth.DisableTwoFactor(ctx, connect.NewRequest(&mimirv1.DisableTwoFactorRequest{Password: password})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("disable while required: %v", err)
	}
	// API keys keep working for apps.
	k, err := a.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: "app", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	app := newClients(srv, withHeader("Authorization", "Bearer "+k.Msg.GetSecret()))
	if _, err := app.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); err != nil {
		t.Fatalf("api key: %v", err)
	}
}
