package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

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
	res := connect.NewResponse(&mimirv1.SetupResponse{User: userProto(u)})
	if err := s.signIn(ctx, req.Header(), res.Header(), u); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, u.Email, "server.setup", u.Email, "")
	return res, nil
}

func (s *Service) SignIn(ctx context.Context, req *connect.Request[mimirv1.SignInRequest]) (*connect.Response[mimirv1.SignInResponse], error) {
	email := auth.NormalizeEmail(req.Msg.GetEmail())
	u, err := s.store.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Hash anyway so a missing user takes as long as a wrong password.
		auth.CheckPassword("$argon2id$v=19$m=19456,t=2,p=1$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", req.Msg.GetPassword())
		return nil, badSignIn()
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	if !auth.CheckPassword(u.PasswordHash, req.Msg.GetPassword()) {
		s.audit(ctx, email, "user.signin_failed", email, "")
		return nil, badSignIn()
	}
	if u.Disabled {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("This account is disabled. Ask an admin to enable it."))
	}
	res := connect.NewResponse(&mimirv1.SignInResponse{User: userProto(u)})
	if err := s.signIn(ctx, req.Header(), res.Header(), u); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, u.Email, "user.signin", u.Email, "")
	return res, nil
}

func (s *Service) SignOut(ctx context.Context, req *connect.Request[mimirv1.SignOutRequest]) (*connect.Response[mimirv1.SignOutResponse], error) {
	if token := cookieValue(req.Header().Values("Cookie"), auth.SessionCookie); token != "" {
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
	res := &mimirv1.WhoAmIResponse{User: userProto(p.User)}
	res.User.Access = accessProto(p.Access)
	for _, perm := range auth.Permissions(p.Access.Role) {
		res.Permissions = append(res.Permissions, string(perm))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ChangePassword(ctx context.Context, req *connect.Request[mimirv1.ChangePasswordRequest]) (*connect.Response[mimirv1.ChangePasswordResponse], error) {
	p, _ := auth.From(ctx)
	if p.Key != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("API keys can't change passwords. Sign in on the web."))
	}
	if !auth.CheckPassword(p.User.PasswordHash, req.Msg.GetCurrentPassword()) {
		return nil, invalid("Your current password doesn't match.")
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
	if err := s.signIn(ctx, req.Header(), res.Header(), p.User); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.password_changed", p.User.Email, "")
	return res, nil
}

// Users

func (s *Service) ListUsers(ctx context.Context, _ *connect.Request[mimirv1.ListUsersRequest]) (*connect.Response[mimirv1.ListUsersResponse], error) {
	us, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListUsersResponse{}
	for _, u := range us {
		res.Users = append(res.Users, userProto(u))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateUser(ctx context.Context, req *connect.Request[mimirv1.CreateUserRequest]) (*connect.Response[mimirv1.CreateUserResponse], error) {
	p, _ := auth.From(ctx)
	email, name, err := cleanUser(req.Msg.GetEmail(), req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess())
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
	u, err := s.store.CreateUser(ctx, store.User{Email: email, Name: name, PasswordHash: hash, Access: access})
	if errors.Is(err, store.ErrEmailTaken) {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("Someone already has that email address."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "user.create", u.Email, describeAccess(u.Access))
	s.emit(ctx, "user.created", map[string]any{"user": map[string]any{"id": u.ID, "email": u.Email, "name": u.Name}, "actor": p.Actor()})
	return connect.NewResponse(&mimirv1.CreateUserResponse{User: userProto(u)}), nil
}

func (s *Service) UpdateUser(ctx context.Context, req *connect.Request[mimirv1.UpdateUserRequest]) (*connect.Response[mimirv1.UpdateUserResponse], error) {
	p, _ := auth.From(ctx)
	target, err := s.store.GetUser(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) || !store.ValidID(req.Msg.GetId()) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such user."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	// Admins can't touch admins or owners; only owners can.
	if target.Access.Role >= store.Admin && !p.Can(auth.UsersAdmin) {
		return nil, auth.ErrForbidden
	}
	_, name, err := cleanUser("a@b.c", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess())
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
	u, err := s.store.UpdateUser(ctx, target.ID, name, access, disabled)
	if err != nil {
		return nil, s.internal(ctx, err)
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
	return connect.NewResponse(&mimirv1.UpdateUserResponse{User: userProto(u)}), nil
}

func (s *Service) DeleteUser(ctx context.Context, req *connect.Request[mimirv1.DeleteUserRequest]) (*connect.Response[mimirv1.DeleteUserResponse], error) {
	p, _ := auth.From(ctx)
	target, err := s.store.GetUser(ctx, req.Msg.GetId())
	if errors.Is(err, store.ErrNotFound) || !store.ValidID(req.Msg.GetId()) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such user."))
	}
	if err != nil {
		return nil, s.internal(ctx, err)
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
	s.emit(ctx, "user.deleted", map[string]any{"user": map[string]any{"id": target.ID, "email": target.Email, "name": target.Name}, "actor": p.Actor()})
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
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" || utf8.RuneCountInString(name) > maxName {
		return nil, invalid("Give the key a name of up to %d characters.", maxName)
	}
	access, err := s.cleanAccess(ctx, p, req.Msg.GetAccess())
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
	es, err := s.store.ListAuditEvents(ctx, before, size+1)
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
			Id: e.ID, Time: timestamppb.New(e.Time), Actor: e.Actor, Action: e.Action, Target: e.Target, Detail: e.Detail,
		})
	}
	return connect.NewResponse(res), nil
}

// helpers

func (s *Service) signIn(ctx context.Context, reqHeader, resHeader http.Header, u store.User) error {
	token, hash, err := auth.NewToken("")
	if err != nil {
		return err
	}
	if err := s.store.CreateSession(ctx, hash, u.ID, time.Now().Add(auth.SessionTTL)); err != nil {
		return err
	}
	// Mark the cookie Secure when the browser reached us over HTTPS, which
	// in production means through a TLS proxy.
	secure := strings.EqualFold(reqHeader.Get("X-Forwarded-Proto"), "https")
	auth.SetSessionCookie(resHeader, token, secure)
	return nil
}

// audit records an event, logging rather than failing on error: the action
// already happened.
func (s *Service) audit(ctx context.Context, actor, action, target, detail string) {
	if err := s.store.Audit(ctx, store.AuditEvent{Actor: actor, Action: action, Target: target, Detail: detail}); err != nil {
		s.log.ErrorContext(ctx, "audit failed", "action", action, "error", err)
	}
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
func (s *Service) cleanAccess(ctx context.Context, p auth.Principal, a *mimirv1.Access) (store.Access, error) {
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
		if _, err := s.store.Get(ctx, id); errors.Is(err, store.ErrNotFound) {
			return store.Access{}, invalid("One of the chosen models no longer exists.")
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
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxName {
		return "", "", invalid("Give the person a name of up to %d characters.", maxName)
	}
	return email, name, nil
}

func badSignIn() error {
	return connect.NewError(connect.CodeUnauthenticated, errors.New("Wrong email or password."))
}

func cookieValue(cookies []string, name string) string {
	for _, c := range cookies {
		for part := range strings.SplitSeq(c, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			if k == name {
				return v
			}
		}
	}
	return ""
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
		CreateTime: timestamppb.New(u.Created),
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
