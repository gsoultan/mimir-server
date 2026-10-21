package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/store"
)

var (
	_ mimirv1connect.AuthServiceHandler   = (*Service)(nil)
	_ mimirv1connect.UserServiceHandler   = (*Service)(nil)
	_ mimirv1connect.ApiKeyServiceHandler = (*Service)(nil)
	_ mimirv1connect.AuditServiceHandler  = (*Service)(nil)
)

// Setup is called at most once: by the first visitor, who becomes owner.
// Until then the server would belong to whoever found it first, so Setup
// needs a one-time token that only someone who can read the server's log
// (or set its environment) knows.

// MinSetupToken is the shortest setup token MIMIR_SETUP_TOKEN may set.
const MinSetupToken = 16

// PrepareSetup opens Setup when the server has no accounts yet, with
// preset as its token or, when preset is empty, a new random one, which
// it returns for the caller to log. It returns "" when the server is
// already set up.
func (s *Service) PrepareSetup(ctx context.Context, preset string) (string, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		s.setupToken = ""
		return "", nil
	}
	token := strings.TrimSpace(preset)
	switch {
	case token == "":
		token = rand.Text()
	case len(token) < MinSetupToken:
		return "", fmt.Errorf("the setup token needs at least %d characters", MinSetupToken)
	}
	s.setupToken = token
	return token, nil
}

func (s *Service) GetSetupState(ctx context.Context, _ *connect.Request[mimirv1.GetSetupStateRequest]) (*connect.Response[mimirv1.GetSetupStateResponse], error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	return connect.NewResponse(&mimirv1.GetSetupStateResponse{NeedsSetup: n == 0}), nil
}

func (s *Service) Setup(ctx context.Context, req *connect.Request[mimirv1.SetupRequest]) (*connect.Response[mimirv1.SetupResponse], error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if n > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This server is already set up. Sign in instead."))
	}
	if s.setupToken == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Setup isn't open. Restart the server and use the setup token it prints in its log."))
	}
	ipKey := "ip:" + s.Proxies.ClientIP(req.Header(), req.Peer().Addr)
	if wait := s.Limiter.Wait(ipKey); wait > 0 {
		return nil, tooMany(wait)
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Msg.GetSetupToken())), []byte(s.setupToken)) != 1 {
		s.Limiter.Fail(ipKey)
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("That setup token isn't right. The server prints it in its log when it starts with no accounts."))
	}
	email, name, err := cleanUser(req.Msg.GetEmail(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(req.Msg.GetPassword())
	if errors.Is(err, auth.ErrWeakPassword) {
		return nil, invalid("Passwords need at least %d characters.", auth.MinPassword)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	u, err := s.store.CreateUser(ctx, store.User{Email: email, Name: name, PasswordHash: hash, Access: store.Access{Role: store.Owner, AllModels: true}})
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.setupToken = "" // one owner, once
	res := connect.NewResponse(&mimirv1.SetupResponse{User: s.userProto(ctx, u)})
	if _, err := s.signIn(ctx, req, res.Header(), u, false); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.auditIn(ctx, u.TeamID, u.Email, "server.setup", u.Email, "")
	return res, nil
}

func (s *Service) SignIn(ctx context.Context, req *connect.Request[mimirv1.SignInRequest]) (*connect.Response[mimirv1.SignInResponse], error) {
	email := auth.NormalizeEmail(req.Msg.GetEmail())
	// Guessing is slowed per address (a hard budget, so one address can't
	// try many accounts quickly) and per account and address together (a
	// growing delay). Nothing is keyed on the account alone: anyone could
	// then lock its owner out by failing on purpose.
	ip := s.Proxies.ClientIP(req.Header(), req.Peer().Addr)
	ipKey, pairKey := "ip:"+ip, "signin:"+email+"|"+ip
	if wait := max(s.Limiter.Wait(ipKey), s.Backoff.Wait(pairKey)); wait > 0 {
		return nil, tooMany(wait)
	}
	fail := func() {
		s.Limiter.Fail(ipKey)
		s.Backoff.Fail(pairKey)
	}
	u, err := s.store.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Hash anyway so a missing user takes as long as a wrong password.
		auth.CheckPassword("$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Msg.GetPassword())
		fail()
		return nil, badSignIn()
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	if !auth.CheckPassword(u.PasswordHash, req.Msg.GetPassword()) {
		fail()
		s.auditIn(ctx, u.TeamID, email, "user.signin_failed", email, s.whence(req))
		return nil, badSignIn()
	}
	if u.Disabled {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("This account is disabled. Ask an admin to enable it."))
	}
	// Only this account's streak from here ends: the address's budget
	// stays, so signing in to an account of one's own can't reset it.
	s.Backoff.Clear(pairKey)
	// A stale cookie from before never carries over: the browser always
	// leaves with a token minted for this sign-in.
	if old := auth.SessionToken(req.Header()); old != "" {
		s.store.DeleteSession(ctx, auth.HashToken(old))
	}
	res := connect.NewResponse(&mimirv1.SignInResponse{})
	if u.TwoFactor() {
		// Half way there: a short-lived cookie that only the code step accepts.
		if _, err := s.signIn(ctx, req, res.Header(), u, true); err != nil {
			return nil, s.internal(ctx, err)
		}
		res.Msg.SecondFactorRequired = true
		return res, nil
	}
	if _, err := s.signIn(ctx, req, res.Header(), u, false); err != nil {
		return nil, s.internal(ctx, err)
	}
	res.Msg.User = s.userProto(ctx, u)
	s.auditIn(ctx, u.TeamID, u.Email, "user.signin", u.Email, s.whence(req))
	return res, nil
}

func (s *Service) SignOut(ctx context.Context, req *connect.Request[mimirv1.SignOutRequest]) (*connect.Response[mimirv1.SignOutResponse], error) {
	if token := auth.SessionToken(req.Header()); token != "" {
		if err := s.store.DeleteSession(ctx, auth.HashToken(token)); err != nil {
			return nil, s.internal(ctx, err)
		}
	}
	res := connect.NewResponse(&mimirv1.SignOutResponse{})
	auth.ClearSessionCookie(res.Header())
	return res, nil
}

func (s *Service) WhoAmI(ctx context.Context, _ *connect.Request[mimirv1.WhoAmIRequest]) (*connect.Response[mimirv1.WhoAmIResponse], error) {
	p, _ := auth.From(ctx)
	res := &mimirv1.WhoAmIResponse{User: s.userProto(ctx, p.User)}
	res.User.Access = accessProto(p.Access)
	for _, perm := range auth.Permissions(p.Access.Role) {
		res.Permissions = append(res.Permissions, string(perm))
	}
	if p.Session != nil {
		res.SessionId = p.Session.ID
		if !p.User.TwoFactor() {
			must, err := s.MustEnroll(ctx, p.User)
			if err != nil {
				return nil, s.internal(ctx, err)
			}
			res.MustSetUpTwoFactor = must
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ChangePassword(ctx context.Context, req *connect.Request[mimirv1.ChangePasswordRequest]) (*connect.Response[mimirv1.ChangePasswordResponse], error) {
	p, _ := auth.From(ctx)
	if err := s.proveIdentity(ctx, req.Header(), req.Peer().Addr, p, req.Msg.GetCurrentPassword()); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(req.Msg.GetNewPassword())
	if errors.Is(err, auth.ErrWeakPassword) {
		return nil, invalid("Passwords need at least %d characters.", auth.MinPassword)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if err := s.store.SetPassword(ctx, p.User.ID, hash); err != nil {
		return nil, s.internal(ctx, err)
	}
	// SetPassword ended every session, including this one; start a fresh one.
	res := connect.NewResponse(&mimirv1.ChangePasswordResponse{})
	if _, err := s.signIn(ctx, req, res.Header(), p.User, false); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.password_changed", p.User.Email, "")
	return res, nil
}

// Users

func (s *Service) ListUsers(ctx context.Context, _ *connect.Request[mimirv1.ListUsersRequest]) (*connect.Response[mimirv1.ListUsersResponse], error) {
	p, _ := auth.From(ctx)
	team := p.Team()
	if p.Can(auth.TeamsManage) {
		team = "" // owners see everyone
	}
	us, err := s.store.ListUsers(ctx, team)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	names, err := s.teamNames(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListUsersResponse{}
	for _, u := range us {
		up := userProto(u)
		up.TeamName = names[u.TeamID]
		res.Users = append(res.Users, up)
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateUser(ctx context.Context, req *connect.Request[mimirv1.CreateUserRequest]) (*connect.Response[mimirv1.CreateUserResponse], error) {
	p, _ := auth.From(ctx)
	email, name, err := cleanUser(req.Msg.GetEmail(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	team, err := s.pickTeam(ctx, p, req.Msg.GetTeamId(), p.Team())
	if err != nil {
		return nil, err
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess(), team)
	if err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(req.Msg.GetPassword())
	if errors.Is(err, auth.ErrWeakPassword) {
		return nil, invalid("Passwords need at least %d characters.", auth.MinPassword)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	u, err := s.store.CreateUser(ctx, store.User{Email: email, Name: name, PasswordHash: hash, Access: access, TeamID: team})
	if errors.Is(err, store.ErrEmailTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Someone already has that email address."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.create", u.Email, describeAccess(u.Access))
	s.emitIn(ctx, u.TeamID, "user.created", map[string]any{"user": map[string]any{"id": u.ID, "email": u.Email, "name": u.Name}, "actor": p.Actor()})
	return connect.NewResponse(&mimirv1.CreateUserResponse{User: s.userProto(ctx, u)}), nil
}

func (s *Service) UpdateUser(ctx context.Context, req *connect.Request[mimirv1.UpdateUserRequest]) (*connect.Response[mimirv1.UpdateUserResponse], error) {
	p, _ := auth.From(ctx)
	target, err := s.getUser(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	// Admins can't touch admins or owners; only owners can.
	if target.Access.Role >= store.Admin && !p.Can(auth.UsersAdmin) {
		return nil, auth.ErrForbidden
	}
	_, name, err := cleanUser("a@b.c", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	team, err := s.pickTeam(ctx, p, req.Msg.GetTeamId(), target.TeamID)
	if err != nil {
		return nil, err
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess(), team)
	if err != nil {
		return nil, err
	}
	disabled := req.Msg.GetDisabled()
	if target.ID == p.User.ID && (disabled || access.Role < p.Access.Role) {
		return nil, invalid("You can't disable or demote yourself. Ask another owner.")
	}
	if target.Access.Role == store.Owner && (access.Role != store.Owner || disabled) {
		if err := s.requireAnotherOwner(ctx, target.ID); err != nil {
			return nil, err
		}
	}
	if team != target.TeamID {
		// Their keys and sign-ins were for the old team; a person moving
		// themselves keeps this browser signed in.
		keep := ""
		if target.ID == p.User.ID && p.Session != nil {
			keep = p.Session.ID
		}
		if err := s.store.MoveUser(ctx, target.ID, team, keep); err != nil {
			return nil, s.internal(ctx, err)
		}
		names, _ := s.teamNames(ctx)
		s.audit(ctx, p.Actor(), "user.move", target.Email, names[target.TeamID]+" → "+names[team])
	}
	u, err := s.store.UpdateUser(ctx, target.ID, name, access, disabled)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	// A smaller role or a disabled account takes effect at once, in every
	// browser the person is signed in on.
	if disabled || access.Role < target.Access.Role {
		if err := s.store.EndSessions(ctx, u.ID); err != nil {
			return nil, s.internal(ctx, err)
		}
	}
	if req.Msg.GetResetTwoFactor() && target.TwoFactor() {
		if err := s.store.DisableTOTP(ctx, u.ID); err != nil {
			return nil, s.internal(ctx, err)
		}
		if err := s.store.EndSessions(ctx, u.ID); err != nil {
			return nil, s.internal(ctx, err)
		}
		u.TOTPSecret = ""
		s.audit(ctx, p.Actor(), "user.two_factor_reset", u.Email, "")
	}
	if pw := req.Msg.GetNewPassword(); pw != "" {
		hash, err := auth.HashPassword(pw)
		if errors.Is(err, auth.ErrWeakPassword) {
			return nil, invalid("Passwords need at least %d characters.", auth.MinPassword)
		}
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		if err := s.store.SetPassword(ctx, u.ID, hash); err != nil {
			return nil, s.internal(ctx, err)
		}
		s.audit(ctx, p.Actor(), "user.password_reset", u.Email, "")
	}
	s.audit(ctx, p.Actor(), "user.update", u.Email, describeAccess(u.Access)+boolNote(" disabled", u.Disabled))
	return connect.NewResponse(&mimirv1.UpdateUserResponse{User: s.userProto(ctx, u)}), nil
}

func (s *Service) DeleteUser(ctx context.Context, req *connect.Request[mimirv1.DeleteUserRequest]) (*connect.Response[mimirv1.DeleteUserResponse], error) {
	p, _ := auth.From(ctx)
	target, err := s.getUser(ctx, p, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if target.Access.Role >= store.Admin && !p.Can(auth.UsersAdmin) {
		return nil, auth.ErrForbidden
	}
	if target.ID == p.User.ID {
		return nil, invalid("You can't delete yourself. Ask another owner.")
	}
	if target.Access.Role == store.Owner {
		if err := s.requireAnotherOwner(ctx, target.ID); err != nil {
			return nil, err
		}
	}
	if err := s.store.DeleteUser(ctx, target.ID); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.delete", target.Email, "")
	s.emitIn(ctx, target.TeamID, "user.deleted", map[string]any{"user": map[string]any{"id": target.ID, "email": target.Email, "name": target.Name}, "actor": p.Actor()})
	return connect.NewResponse(&mimirv1.DeleteUserResponse{}), nil
}

// API keys

func (s *Service) ListApiKeys(ctx context.Context, _ *connect.Request[mimirv1.ListApiKeysRequest]) (*connect.Response[mimirv1.ListApiKeysResponse], error) {
	p, _ := auth.From(ctx)
	ks, err := s.store.ListAPIKeys(ctx, p.User.ID)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListApiKeysResponse{}
	for _, k := range ks {
		res.ApiKeys = append(res.ApiKeys, keyProto(k))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateApiKey(ctx context.Context, req *connect.Request[mimirv1.CreateApiKeyRequest]) (*connect.Response[mimirv1.CreateApiKeyResponse], error) {
	p, _ := auth.From(ctx)
	if p.Key != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("API keys can't make more keys. Sign in on the web."))
	}
	name, err := cleanName("key", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess(), p.Team())
	if err != nil {
		return nil, err
	}
	secret, hash, err := auth.NewToken(auth.KeyPrefix)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	k, err := s.store.CreateAPIKey(ctx, store.APIKey{UserID: p.User.ID, Name: name, Prefix: secret[:len(auth.KeyPrefix)+8], Access: access}, hash)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "apikey.create", k.Name, describeAccess(k.Access))
	return connect.NewResponse(&mimirv1.CreateApiKeyResponse{ApiKey: keyProto(k), Secret: secret}), nil
}

func (s *Service) RevokeApiKey(ctx context.Context, req *connect.Request[mimirv1.RevokeApiKeyRequest]) (*connect.Response[mimirv1.RevokeApiKeyResponse], error) {
	p, _ := auth.From(ctx)
	err := s.store.DeleteAPIKey(ctx, p.User.ID, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) || !store.ValidID(req.Msg.GetId()) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such key."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "apikey.revoke", req.Msg.GetId(), "")
	return connect.NewResponse(&mimirv1.RevokeApiKeyResponse{}), nil
}

// Audit log

func (s *Service) ListAuditEvents(ctx context.Context, req *connect.Request[mimirv1.ListAuditEventsRequest]) (*connect.Response[mimirv1.ListAuditEventsResponse], error) {
	size := int(req.Msg.GetPageSize())
	if size <= 0 || size > 200 {
		size = 50
	}
	before := req.Msg.GetPageToken()
	if before != "" && !store.ValidID(before) {
		return nil, invalid("That page token isn't valid.")
	}
	p, _ := auth.From(ctx)
	team := p.Team()
	if p.Can(auth.TeamsManage) {
		team = "" // owners see every team's events
	}
	es, err := s.store.ListAuditEvents(ctx, team, before, size+1)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListAuditEventsResponse{}
	if len(es) > size {
		es = es[:size]
		res.NextPageToken = es[size-1].ID
	}
	for _, e := range es {
		res.Events = append(res.Events, &mimirv1.AuditEvent{
			Id: e.ID, Time: timestamppb.New(e.Time), Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail, TeamName: e.TeamName,
		})
	}
	return connect.NewResponse(res), nil
}

// helpers

// signIn starts a session for u and sets its cookie. A pending session is
// the short-lived half of a sign-in that still needs its second factor.
func (s *Service) signIn(ctx context.Context, req connect.AnyRequest, resHeader http.Header, u store.User, pending bool) (store.Session, error) {
	token, hash, err := auth.NewToken("")
	if err != nil {
		return store.Session{}, err
	}
	ttl := auth.SessionTTL
	if pending {
		ttl = auth.PendingTTL
	}
	h := req.Header()
	se, err := s.store.CreateSession(ctx, hash, store.Session{
		UserID: u.ID, Device: auth.Device(h.Get("User-Agent")), IP: s.Proxies.ClientIP(h, req.Peer().Addr),
		Pending: pending, Expires: time.Now().Add(ttl),
	})
	if err != nil {
		return store.Session{}, err
	}
	// Mark the cookie Secure when the browser reached us over HTTPS: TLS
	// served here, or a trusted proxy that says so.
	auth.SetSessionCookie(resHeader, token, s.https(ctx, req), ttl)
	return se, nil
}

// audit records an event, logging rather than failing on error: the action
// already happened.
// The event belongs to the caller's team.
func (s *Service) audit(ctx context.Context, actor, action, target, detail string) {
	p, _ := auth.From(ctx)
	s.auditIn(ctx, p.Team(), actor, action, target, detail)
}

// auditIn records an event in a given team, for when there's no caller
// yet (signing in) or it's the server acting (training).
func (s *Service) auditIn(ctx context.Context, team, actor, action, target, detail string) {
	if err := s.store.Audit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Detail: detail, TeamID: team}); err != nil {
		s.log.ErrorContext(ctx, "audit failed", "action", action, "error", err)
	}
}

// getUser returns a user the caller may manage: anyone for owners, their
// own team's people for admins. Others read as missing.
func (s *Service) getUser(ctx context.Context, p auth.Principal, id string) (store.User, error) {
	missing := connect.NewError(connect.CodeNotFound, errors.New("No such user."))
	if !store.ValidID(id) {
		return store.User{}, missing
	}
	u, err := s.store.GetUser(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.User{}, missing
	}
	if err != nil {
		return store.User{}, s.internal(ctx, err)
	}
	if u.TeamID != p.Team() && !p.Can(auth.TeamsManage) {
		return store.User{}, missing
	}
	return u, nil
}

// pickTeam resolves the team a person should be in: current when none is
// asked for; any existing team for owners; otherwise only the current.
func (s *Service) pickTeam(ctx context.Context, p auth.Principal, asked, current string) (string, error) {
	if asked == "" || asked == current {
		return current, nil
	}
	if !p.Can(auth.TeamsManage) {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("Only owners can move people between teams."))
	}
	if !store.ValidID(asked) {
		return "", invalid("Pick a team from the list.")
	}
	if _, err := s.store.GetTeam(ctx, asked); errors.Is(err, store.ErrNotFound) {
		return "", invalid("That team no longer exists.")
	} else if err != nil {
		return "", s.internal(ctx, err)
	}
	return asked, nil
}

// teamNames maps team ids to names.
func (s *Service) teamNames(ctx context.Context) (map[string]string, error) {
	ts, err := s.store.ListTeams(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ts))
	for _, t := range ts {
		out[t.ID] = t.Name
	}
	return out, nil
}

// userProto is userProto with the team's name filled in.
func (s *Service) userProto(ctx context.Context, u store.User) *mimirv1.User {
	out := userProto(u)
	if t, err := s.store.GetTeam(ctx, u.TeamID); err == nil {
		out.TeamName = t.Name
	}
	return out
}

func (s *Service) requireAnotherOwner(ctx context.Context, exceptID string) error {
	n, err := s.store.CountOwners(ctx)
	if err != nil {
		return s.internal(ctx, err)
	}
	if n <= 1 {
		return invalid("This is the only owner. Make someone else an owner first.")
	}
	return nil
}

// cleanAccess validates requested access and caps it at the caller's own.
// Models must belong to team.
func (s *Service) cleanAccess(ctx context.Context, p auth.Principal, a *mimirv1.Access, team string) (store.Access, error) {
	role := store.Role(a.GetRole())
	if role < store.Viewer || role > store.Owner {
		return store.Access{}, invalid("Pick a role: viewer, editor, admin or owner.")
	}
	if role > p.Access.Role {
		return store.Access{}, invalid("You can't give a role higher than your own.")
	}
	if role >= store.Admin && !p.Can(auth.UsersAdmin) {
		return store.Access{}, invalid("Only owners can make admins and owners.")
	}
	out := store.Access{Role: role, AllModels: a.GetAllModels() || role >= store.Admin}
	if out.AllModels {
		if !p.Access.AllModels {
			return store.Access{}, invalid("You can only share models you can reach yourself.")
		}
		return out, nil
	}
	seen := map[string]bool{}
	for _, id := range a.GetModelIds() {
		if !store.ValidID(id) || seen[id] {
			continue
		}
		m, err := s.store.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && m.TeamID != team) {
			return store.Access{}, invalid("One of the chosen models no longer exists in this team.")
		} else if err != nil {
			return store.Access{}, s.internal(ctx, err)
		}
		if !p.Access.Allows(id) {
			return store.Access{}, invalid("You can only share models you can reach yourself.")
		}
		seen[id] = true
		out.ModelIDs = append(out.ModelIDs, id)
	}
	return out, nil
}

func cleanUser(email, name string) (string, string, error) {
	email = auth.NormalizeEmail(email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		return "", "", invalid("That doesn't look like an email address.")
	}
	name, err := cleanName("person", name)
	return email, name, err
}

func badSignIn() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("Wrong email or password."))
}

func describeAccess(a store.Access) string {
	role := [...]string{"", "viewer", "editor", "admin", "owner"}[a.Role]
	if a.AllModels {
		return role + ", all models"
	}
	if n := len(a.ModelIDs); n == 1 {
		return role + ", 1 model"
	} else {
		return fmt.Sprintf("%s, %d models", role, n)
	}
}

func boolNote(note string, b bool) string {
	if b {
		return "," + note
	}
	return ""
}

func userProto(u store.User) *mimirv1.User {
	out := &mimirv1.User{
		Id: u.ID, Email: u.Email, Name: u.Name, Access: accessProto(u.Access), Disabled: u.Disabled,
		CreateTime: timestamppb.New(u.Created), TwoFactor: u.TwoFactor(), TeamId: u.TeamID,
	}
	if !u.LastSignIn.IsZero() {
		out.LastSignInTime = timestamppb.New(u.LastSignIn)
	}
	return out
}

func keyProto(k store.APIKey) *mimirv1.ApiKey {
	out := &mimirv1.ApiKey{Id: k.ID, Name: k.Name, Prefix: k.Prefix, Access: accessProto(k.Access), CreateTime: timestamppb.New(k.Created)}
	if !k.LastUsed.IsZero() {
		out.LastUsedTime = timestamppb.New(k.LastUsed)
	}
	return out
}

func accessProto(a store.Access) *mimirv1.Access {
	return &mimirv1.Access{Role: mimirv1.Role(a.Role), AllModels: a.AllModels, ModelIds: a.ModelIDs}
}
