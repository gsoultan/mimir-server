package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/gsoultan/mimir-server/internal/store"
)

// SessionCookie carries the browser's session token.
const SessionCookie = "mimir_session"

// SessionTTL is how long a sign-in lasts.
const SessionTTL = 30 * 24 * time.Hour

// Public lists procedures anyone may call without signing in.
var Public = map[string]bool{
	"/mimir.v1.AuthService/GetSetupState": true,
	"/mimir.v1.AuthService/Setup":         true,
	"/mimir.v1.AuthService/SignIn":        true,
	"/mimir.v1.AuthService/SignOut":       true,
}

// Required maps each procedure to the permission it needs. Model-level
// checks happen in the handlers, which know which model a call touches.
var Required = map[string]Permission{
	"/mimir.v1.ModelService/ListArchitectures": ModelsRead,
	"/mimir.v1.ModelService/ListModels":        ModelsRead,
	"/mimir.v1.ModelService/GetModel":          ModelsRead,
	"/mimir.v1.ModelService/CreateModel":       ModelsWrite,
	"/mimir.v1.ModelService/UpdateModel":       ModelsWrite,
	"/mimir.v1.ModelService/DeleteModel":       ModelsWrite,
	"/mimir.v1.PredictService/Predict":         ModelsPredict,
	"/mimir.v1.AuthService/WhoAmI":             "",
	"/mimir.v1.AuthService/ChangePassword":     "",
	"/mimir.v1.UserService/ListUsers":          UsersRead,
	"/mimir.v1.UserService/CreateUser":         UsersWrite,
	"/mimir.v1.UserService/UpdateUser":         UsersWrite,
	"/mimir.v1.UserService/DeleteUser":         UsersWrite,
	"/mimir.v1.ApiKeyService/ListApiKeys":      KeysManage,
	"/mimir.v1.ApiKeyService/CreateApiKey":     KeysManage,
	"/mimir.v1.ApiKeyService/RevokeApiKey":     KeysManage,
	"/mimir.v1.AuditService/ListAuditEvents":   AuditRead,
}

// ErrSignedOut is returned to callers with no valid credentials.
var ErrSignedOut = connect.NewError(connect.CodeUnauthenticated, errors.New("Sign in to continue."))

// ErrForbidden is returned to callers who lack a permission.
var ErrForbidden = connect.NewError(connect.CodePermissionDenied, errors.New("You don't have permission to do that."))

// Interceptor authenticates every request from its cookie or bearer key
// and checks the procedure's permission. It never reaches the handler
// for a procedure it doesn't know.
func Interceptor(st *store.Store) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			proc := req.Spec().Procedure
			p, err := authenticate(ctx, st, req.Header())
			if err != nil {
				return nil, err
			}
			if p != nil {
				ctx = WithPrincipal(ctx, *p)
			}
			if Public[proc] {
				return next(ctx, req)
			}
			perm, known := Required[proc]
			if !known {
				return nil, ErrForbidden
			}
			if p == nil {
				return nil, ErrSignedOut
			}
			if perm != "" && !p.Can(perm) {
				return nil, ErrForbidden
			}
			return next(ctx, req)
		}
	}
}

// authenticate returns the caller, nil when anonymous, or an error when
// credentials were offered but are wrong.
func authenticate(ctx context.Context, st *store.Store, h http.Header) (*Principal, error) {
	if a := h.Get("Authorization"); a != "" {
		secret, ok := strings.CutPrefix(a, "Bearer ")
		if !ok || !strings.HasPrefix(secret, KeyPrefix) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("Use an API key: Authorization: Bearer mimir_..."))
		}
		key, user, err := st.KeyUser(ctx, HashToken(secret))
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("That API key isn't valid. It may have been revoked."))
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		if user.Disabled {
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("This account is disabled."))
		}
		return &Principal{User: user, Key: &key, Access: Effective(user.Access, key.Access)}, nil
	}
	token := cookie(h, SessionCookie)
	if token == "" {
		return nil, nil
	}
	user, err := st.SessionUser(ctx, HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // stale cookie: treat as signed out
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if user.Disabled {
		return nil, nil
	}
	return &Principal{User: user, Access: user.Access}, nil
}

func cookie(h http.Header, name string) string {
	for _, c := range h.Values("Cookie") {
		for part := range strings.SplitSeq(c, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			if k == name {
				return v
			}
		}
	}
	return ""
}

// SetSessionCookie writes a sign-in cookie on the response.
func SetSessionCookie(h http.Header, token string, secure bool) {
	c := &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(SessionTTL / time.Second),
	}
	h.Add("Set-Cookie", c.String())
}

// ClearSessionCookie removes the sign-in cookie.
func ClearSessionCookie(h http.Header) {
	c := &http.Cookie{Name: SessionCookie, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	h.Add("Set-Cookie", c.String())
}
