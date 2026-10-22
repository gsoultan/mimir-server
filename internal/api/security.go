package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Issuer is the name authenticator apps show next to the account.
const Issuer = "mimir"

// Security is who must use two-step verification. The zero value means
// nobody has to.
type Security struct {
	TwoFactor mimirv1.TwoFactorPolicy `json:"two_factor"`
}

// KeySecurity is the settings key for Security.
const KeySecurity = "security"

// LoadSecurity reads the security setting.
func LoadSecurity(ctx context.Context, st *store.Store) (Security, error) {
	sec := Security{TwoFactor: mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_OPTIONAL}
	raw, err := st.GetSetting(ctx, KeySecurity)
	if err != nil || raw == "" {
		return sec, err
	}
	return sec, json.Unmarshal([]byte(raw), &sec)
}

// MustEnroll reports whether policy requires u to set up two-step
// verification. It's the interceptor's gate for people who haven't.
func (s *Service) MustEnroll(ctx context.Context, u store.User) (bool, error) {
	sec, err := LoadSecurity(ctx, s.store)
	if err != nil {
		return false, err
	}
	switch sec.TwoFactor {
	case mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_EVERYONE:
		return true, nil
	case mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_ADMINS:
		return u.Access.Role >= store.Admin, nil
	}
	return false, nil
}

func (s *Service) UpdateSecurity(ctx context.Context, req *connect.Request[mimirv1.UpdateSecurityRequest]) (*connect.Response[mimirv1.UpdateSecurityResponse], error) {
	p, _ := auth.From(ctx)
	sec := Security{TwoFactor: req.Msg.GetSecurity().GetTwoFactor()}
	if sec.TwoFactor < mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_OPTIONAL || sec.TwoFactor > mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_EVERYONE {
		return nil, invalid("Choose who must use two-step verification.")
	}
	// Whoever turns the requirement on must already meet it, or they'd lock
	// themselves out of Settings on the next click.
	if sec.TwoFactor != mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_OPTIONAL && !p.User.TwoFactor() {
		return nil, invalid("Set up two-step verification on your own account first.")
	}
	b, _ := json.Marshal(sec)
	if err := s.store.SetSetting(ctx, KeySecurity, string(b)); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "settings.security", "", "two-step verification: "+policyWord(sec.TwoFactor))
	return connect.NewResponse(&mimirv1.UpdateSecurityResponse{Security: securityProto(sec)}), nil
}

func securityProto(sec Security) *mimirv1.Security {
	return &mimirv1.Security{TwoFactor: sec.TwoFactor}
}

func policyWord(p mimirv1.TwoFactorPolicy) string {
	switch p {
	case mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_ADMINS:
		return "required for admins"
	case mimirv1.TwoFactorPolicy_TWO_FACTOR_POLICY_EVERYONE:
		return "required for everyone"
	}
	return "optional"
}

// Sign-in with a second step

// ErrTooMany is returned while a limiter holds the caller back.
func tooMany(wait time.Duration) error {
	if wait < time.Minute {
		secs := int(wait.Seconds()) + 1
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("Too many attempts. Try again in %d seconds.", secs))
	}
	mins := int(wait.Minutes()) + 1
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("Too many attempts. Try again in %d minutes.", mins))
}

func (s *Service) VerifySecondFactor(ctx context.Context, req *connect.Request[mimirv1.VerifySecondFactorRequest]) (*connect.Response[mimirv1.VerifySecondFactorResponse], error) {
	p, ok := auth.From(ctx)
	if !ok || p.Session == nil || !p.Session.Pending {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Sign in with your password first."))
	}
	u := p.User
	recovery, left, err := s.spendSecondFactor(ctx, req.Header(), req.Peer().Addr, u, req.Msg.GetCode())
	if err != nil {
		return nil, err
	}
	res := &mimirv1.VerifySecondFactorResponse{UsedRecoveryCode: recovery, RecoveryCodesLeft: int32(left)}
	// The pending session is spent; the real one gets a brand-new token.
	s.store.DeleteSession(ctx, auth.HashToken(auth.SessionToken(req.Header())))
	out := connect.NewResponse(res)
	if _, err := s.signIn(ctx, req, out.Header(), u, false); err != nil {
		return nil, s.internal(ctx, err)
	}
	res.User = s.userProto(ctx, u)
	s.auditIn(ctx, u.TeamID, u.Email, "user.signin", u.Email, s.whence(req)+boolNote(" recovery code", res.UsedRecoveryCode))
	return out, nil
}

// spendSecondFactor checks code, a current code from u's authenticator app
// or one of their recovery codes, and spends it so it can't be used again.
// Whoever gets here knows the password, so the account's own budget counts
// as well as the address's, and each failure past a few makes the next
// guess wait longer: a fixed budget alone would still allow about a
// thousand guesses a day, from any number of addresses. A wrong or reused
// code is CodeUnauthenticated. It returns whether a recovery code was used
// and how many are left.
func (s *Service) spendSecondFactor(ctx context.Context, h http.Header, remote string, u store.User, code string) (recovery bool, left int, err error) {
	keys := []string{"2fa:" + u.ID, "ip:" + s.Proxies.ClientIP(h, remote)}
	streak := "2fa:" + u.ID
	if wait := max(s.Limiter.Wait(keys...), s.Backoff.Wait(streak)); wait > 0 {
		return false, 0, tooMany(wait)
	}
	fail := func() {
		s.Limiter.Fail(keys...)
		s.Backoff.Fail(streak)
	}
	switch {
	case auth.LooksLikeRecoveryCode(code):
		used, n, err := s.store.UseRecoveryCode(ctx, u.ID, auth.HashToken(auth.NormalizeRecoveryCode(code)))
		if err != nil {
			return false, 0, s.internal(ctx, err)
		}
		if !used {
			fail()
			s.auditIn(ctx, u.TeamID, u.Email, "user.code_failed", u.Email, "recovery code")
			return false, 0, connect.NewError(connect.CodeUnauthenticated, errors.New("That recovery code isn't right, or was already used."))
		}
		recovery, left = true, n
	default:
		step, ok := auth.CheckTOTP(u.TOTPSecret, code, time.Now())
		if ok {
			ok, err := s.store.UseTOTPStep(ctx, u.ID, step)
			if err != nil {
				return false, 0, s.internal(ctx, err)
			}
			if !ok {
				return false, 0, connect.NewError(connect.CodeUnauthenticated, errors.New("That code was already used. Wait for the next one."))
			}
		}
		if !ok {
			fail()
			s.auditIn(ctx, u.TeamID, u.Email, "user.code_failed", u.Email, "")
			return false, 0, connect.NewError(connect.CodeUnauthenticated, errors.New("That code isn't right. Check the time on your phone and try again."))
		}
	}
	s.Limiter.Clear(keys...)
	s.Backoff.Clear(streak)
	return recovery, left, nil
}

// proveSecondFactor asks for a current code before a change to two-step
// verification itself, when it is on, so a stolen cookie and password
// can't turn it off or swap in another authenticator. Call it after
// proveIdentity.
func (s *Service) proveSecondFactor(ctx context.Context, h http.Header, remote string, u store.User, code string) error {
	if !u.TwoFactor() {
		return nil
	}
	if strings.TrimSpace(code) == "" {
		return invalid("Enter a code from your authenticator app, or a recovery code.")
	}
	_, _, err := s.spendSecondFactor(ctx, h, remote, u, code)
	if ce := new(connect.Error); errors.As(err, &ce) && ce.Code() == connect.CodeUnauthenticated {
		// The person is signed in; a wrong code is a bad field, not a
		// lost session.
		return invalid("%s", ce.Message())
	}
	return err
}

func (s *Service) StartTwoFactor(ctx context.Context, req *connect.Request[mimirv1.StartTwoFactorRequest]) (*connect.Response[mimirv1.StartTwoFactorResponse], error) {
	p, _ := auth.From(ctx)
	if err := s.proveIdentity(ctx, req.Header(), req.Peer().Addr, p, req.Msg.GetPassword()); err != nil {
		return nil, err
	}
	if err := s.proveSecondFactor(ctx, req.Header(), req.Peer().Addr, p.User, req.Msg.GetCode()); err != nil {
		return nil, err
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.store.SetPendingTOTP(ctx, p.User.ID, secret); err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.StartTwoFactorResponse{Secret: secret, OtpauthUrl: auth.OTPAuthURL(secret, Issuer, p.User.Email)}), nil
}

func (s *Service) EnableTwoFactor(ctx context.Context, req *connect.Request[mimirv1.EnableTwoFactorRequest]) (*connect.Response[mimirv1.EnableTwoFactorResponse], error) {
	p, _ := auth.From(ctx)
	if p.Session == nil {
		return nil, apiKeyNo()
	}
	if p.User.TOTPPending == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Scan the code first, then enter what the app shows."))
	}
	step, ok := auth.CheckTOTP(p.User.TOTPPending, req.Msg.GetCode(), time.Now())
	if !ok {
		return nil, invalid("That code isn't right. Make sure you scanned the latest code and try the next number the app shows.")
	}
	codes, hashes, err := auth.NewRecoveryCodes()
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.store.EnableTOTP(ctx, p.User.ID, p.Session.ID, hashes); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.store.UseTOTPStep(ctx, p.User.ID, step)
	s.audit(ctx, p.Actor(), "user.two_factor_on", p.User.Email, "")
	return connect.NewResponse(&mimirv1.EnableTwoFactorResponse{RecoveryCodes: codes}), nil
}

func (s *Service) DisableTwoFactor(ctx context.Context, req *connect.Request[mimirv1.DisableTwoFactorRequest]) (*connect.Response[mimirv1.DisableTwoFactorResponse], error) {
	p, _ := auth.From(ctx)
	if err := s.proveIdentity(ctx, req.Header(), req.Peer().Addr, p, req.Msg.GetPassword()); err != nil {
		return nil, err
	}
	if must, err := s.MustEnroll(ctx, p.User); err != nil {
		return nil, s.internal(ctx, err)
	} else if must {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Two-step verification is required for your account on this server."))
	}
	if err := s.proveSecondFactor(ctx, req.Header(), req.Peer().Addr, p.User, req.Msg.GetCode()); err != nil {
		return nil, err
	}
	if err := s.store.DisableTOTP(ctx, p.User.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.two_factor_off", p.User.Email, "")
	return connect.NewResponse(&mimirv1.DisableTwoFactorResponse{}), nil
}

// proveIdentity checks the password for a sensitive change, so a stolen
// cookie alone can't make it. Failures count against the sign-in limiter.
func (s *Service) proveIdentity(ctx context.Context, h http.Header, remote string, p auth.Principal, password string) error {
	if p.Session == nil {
		return apiKeyNo()
	}
	keys := []string{"user:" + p.User.ID, "ip:" + s.Proxies.ClientIP(h, remote)}
	if wait := s.Limiter.Wait(keys...); wait > 0 {
		return tooMany(wait)
	}
	if !auth.CheckPassword(p.User.PasswordHash, password) {
		s.Limiter.Fail(keys...)
		return invalid("Your password doesn't match.")
	}
	s.Limiter.Clear(keys...)
	return nil
}

func apiKeyNo() error {
	return connect.NewError(connect.CodePermissionDenied, errors.New("API keys can't change account security. Sign in on the web."))
}

// Sessions

func (s *Service) ListSessions(ctx context.Context, _ *connect.Request[mimirv1.ListSessionsRequest]) (*connect.Response[mimirv1.ListSessionsResponse], error) {
	p, _ := auth.From(ctx)
	ses, err := s.store.ListSessions(ctx, p.User.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListSessionsResponse{}
	for _, se := range ses {
		res.Sessions = append(res.Sessions, &mimirv1.Session{
			Id: se.ID, Device: se.Device, Ip: se.IP, CreateTime: timestamppb.New(se.Created), LastSeenTime: timestamppb.New(se.LastSeen),
			Current: p.Session != nil && se.ID == p.Session.ID,
		})
	}
	return connect.NewResponse(res), nil
}

func (s *Service) RevokeSessions(ctx context.Context, req *connect.Request[mimirv1.RevokeSessionsRequest]) (*connect.Response[mimirv1.RevokeSessionsResponse], error) {
	p, _ := auth.From(ctx)
	if p.Session == nil {
		return nil, apiKeyNo()
	}
	var n int64
	if id := req.Msg.GetId(); id != "" {
		if id == p.Session.ID {
			return nil, invalid("Use Sign out for this device.")
		}
		ok, err := s.store.RevokeSession(ctx, p.User.ID, id)
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		if ok {
			n = 1
		}
	} else {
		var err error
		if n, err = s.store.RevokeOtherSessions(ctx, p.User.ID, p.Session.ID); err != nil {
			return nil, s.internal(ctx, err)
		}
	}
	s.audit(ctx, p.Actor(), "user.sessions_revoked", p.User.Email, fmt.Sprintf("%d sessions", n))
	return connect.NewResponse(&mimirv1.RevokeSessionsResponse{Revoked: int32(n)}), nil
}

// whence describes where a request came from, for the audit log.
func (s *Service) whence(req connect.AnyRequest) string {
	return s.Proxies.ClientIP(req.Header(), req.Peer().Addr) + ", " + strings.ToLower(auth.Device(req.Header().Get("User-Agent")))
}
