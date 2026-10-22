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

// SessionCookie carries the browser's session token. Over HTTPS the
// cookie gets the __Host- prefix, which browsers only accept from a secure
// origin for the whole site, so a subdomain or a plain-HTTP page can't
// plant one.
const (
	SessionCookie       = "mimir_session"
	SecureSessionCookie = "__Host-mimir_session"
)

// SessionTTL is how long a sign-in lasts at most; idle sessions end
// sooner (store.SessionIdleTTL). PendingTTL is how long a person has to
// enter their second factor.
const (
	SessionTTL = 30 * 24 * time.Hour
	PendingTTL = 10 * time.Minute
)

// PendingAllowed lists what a sign-in that still needs its code may call.
var PendingAllowed = map[string]bool{
	"/mimir.v1.AuthService/VerifySecondFactor": true,
	"/mimir.v1.AuthService/SignOut":            true,
}

// EnrollAllowed lists what a person who must still set up two-step
// verification may call: enough to set it up, and to leave.
var EnrollAllowed = map[string]bool{
	"/mimir.v1.AuthService/WhoAmI":          true,
	"/mimir.v1.AuthService/StartTwoFactor":  true,
	"/mimir.v1.AuthService/EnableTwoFactor": true,
	"/mimir.v1.AuthService/SignOut":         true,
}

// Public lists procedures anyone may call without signing in.
var Public = map[string]bool{
	"/mimir.v1.AuthService/GetSetupState": true,
	// CheckDatabase only answers before the server has a database.
	"/mimir.v1.AuthService/CheckDatabase": true,
	"/mimir.v1.AuthService/Setup":         true,
	"/mimir.v1.AuthService/SignIn":        true,
	"/mimir.v1.AuthService/SignOut":       true,
}

// Required maps each procedure to the permission it needs. Model-level
// checks happen in the handlers, which know which model a call touches.
var Required = map[string]Permission{
	"/mimir.v1.ModelService/ListArchitectures":     ModelsRead,
	"/mimir.v1.ModelService/SaveArchitecture":      ModelsWrite,
	"/mimir.v1.ModelService/DeleteArchitecture":    ModelsWrite,
	"/mimir.v1.ModelService/GetDeviceModel":        ModelsPredict,
	"/mimir.v1.ModelService/ListModels":            ModelsRead,
	"/mimir.v1.ModelService/GetModel":              ModelsRead,
	"/mimir.v1.ModelService/CreateModel":           ModelsWrite,
	"/mimir.v1.ModelService/UpdateModel":           ModelsWrite,
	"/mimir.v1.ModelService/DeleteModel":           ModelsWrite,
	"/mimir.v1.PredictService/Predict":             ModelsPredict,
	"/mimir.v1.PredictService/Generate":            ModelsPredict,
	"/mimir.v1.PredictService/GenerateStream":      ModelsPredict,
	"/mimir.v1.PredictService/Chat":                ModelsPredict,
	"/mimir.v1.DatasetService/ListDatasets":        ModelsWrite,
	"/mimir.v1.DatasetService/GetDataset":          ModelsWrite,
	"/mimir.v1.DatasetService/CreateDataset":       ModelsWrite,
	"/mimir.v1.DatasetService/DeleteDataset":       ModelsWrite,
	"/mimir.v1.DatasetService/CheckDataset":        ModelsWrite,
	"/mimir.v1.TemplateService/ListTemplates":      ModelsRead,
	"/mimir.v1.TemplateService/GetTemplateExample": ModelsRead,
	"/mimir.v1.TrainingService/ListJobs":           ModelsWrite,
	"/mimir.v1.TrainingService/GetJob":             ModelsWrite,
	"/mimir.v1.TrainingService/CreateJob":          ModelsWrite,
	"/mimir.v1.TrainingService/CancelJob":          ModelsWrite,
	"/mimir.v1.TrainingService/ResumeJob":          ModelsWrite,
	"/mimir.v1.TrainingService/DeleteJob":          ModelsWrite,
	"/mimir.v1.AuthService/WhoAmI":                 "",
	"/mimir.v1.AuthService/ChangePassword":         "",
	"/mimir.v1.AuthService/VerifySecondFactor":     "",
	"/mimir.v1.AuthService/StartTwoFactor":         "",
	"/mimir.v1.AuthService/EnableTwoFactor":        "",
	"/mimir.v1.AuthService/DisableTwoFactor":       "",
	"/mimir.v1.AuthService/ListSessions":           "",
	"/mimir.v1.AuthService/RevokeSessions":         "",
	"/mimir.v1.UserService/ListUsers":              UsersRead,
	"/mimir.v1.UserService/CreateUser":             UsersWrite,
	"/mimir.v1.UserService/UpdateUser":             UsersWrite,
	"/mimir.v1.UserService/DeleteUser":             UsersWrite,
	"/mimir.v1.ApiKeyService/ListApiKeys":          KeysManage,
	"/mimir.v1.ApiKeyService/CreateApiKey":         KeysManage,
	"/mimir.v1.ApiKeyService/RevokeApiKey":         KeysManage,
	"/mimir.v1.AuditService/ListAuditEvents":       AuditRead,
	"/mimir.v1.WebhookService/ListWebhooks":        WebhooksManage,
	"/mimir.v1.WebhookService/CreateWebhook":       WebhooksManage,
	"/mimir.v1.WebhookService/UpdateWebhook":       WebhooksManage,
	"/mimir.v1.WebhookService/DeleteWebhook":       WebhooksManage,
	"/mimir.v1.WebhookService/SendTestEvent":       WebhooksManage,
	"/mimir.v1.WebhookService/ListDeliveries":      WebhooksManage,
	"/mimir.v1.WebhookService/Redeliver":           WebhooksManage,
	"/mimir.v1.SettingsService/GetSettings":        SettingsManage,
	"/mimir.v1.SettingsService/UpdateAlerts":       SettingsManage,
	"/mimir.v1.SettingsService/SendTestAlert":      SettingsManage,
	"/mimir.v1.SettingsService/UpdateRetention":    SettingsManage,
	"/mimir.v1.SettingsService/UpdateSecurity":     SettingsManage,
	"/mimir.v1.SettingsService/GetDatabaseInfo":    SettingsManage,
	"/mimir.v1.SettingsService/UpdateLimits":       SettingsManage,
	"/mimir.v1.SettingsService/GetUsage":           ModelsRead,
	"/mimir.v1.DatasetService/TestQuery":           DatabaseQuery,
	"/mimir.v1.DatasetService/TestBucket":          DatabaseQuery,
	"/mimir.v1.DatasetService/RefreshDataset":      ModelsWrite,
	"/mimir.v1.TrainingService/SetRepeat":          ModelsWrite,
	"/mimir.v1.WorkerService/ListWorkers":          WorkersManage,
	"/mimir.v1.WorkerService/GetWorker":            WorkersManage,
	"/mimir.v1.WorkerService/ReplaceWorkerToken":   WorkersManage,
	"/mimir.v1.WorkerService/ListWorkerChoices":    ModelsWrite,
	"/mimir.v1.ModelService/SetModelWorker":        ModelsWrite,
	"/mimir.v1.WorkerService/CreateWorker":         WorkersManage,
	"/mimir.v1.WorkerService/DeleteWorker":         WorkersManage,
	"/mimir.v1.TeamService/ListTeams":              TeamsManage,
	"/mimir.v1.TeamService/CreateTeam":             TeamsManage,
	"/mimir.v1.TeamService/UpdateTeam":             TeamsManage,
	"/mimir.v1.TeamService/DeleteTeam":             TeamsManage,
}

// ErrSignedOut is returned to callers with no valid credentials.
var ErrSignedOut = connect.NewError(connect.CodeUnauthenticated, errors.New("Sign in to continue."))

// ErrForbidden is returned to callers who lack a permission.
var ErrForbidden = connect.NewError(connect.CodePermissionDenied, errors.New("You don't have permission to do that."))

// ErrMustEnroll is returned when an admin requires two-step verification
// and the caller hasn't set it up yet.
var ErrMustEnroll = connect.NewError(connect.CodeFailedPrecondition, errors.New("Set up two-step verification to continue."))

// MustEnroll decides whether a signed-in person has to set up two-step
// verification before doing anything else.
type MustEnroll func(ctx context.Context, u store.User) (bool, error)

// SelfAuthenticated lists the streams that check their caller themselves:
// a worker's link proves itself with its token inside the stream.
var SelfAuthenticated = map[string]bool{
	"/mimir.v1.WorkerService/Link": true,
}

// Interceptor authenticates every request from its cookie or bearer key
// and checks the procedure's permission, for calls and streams alike. It
// never reaches the handler for a procedure it doesn't know.
func Interceptor(st *store.Store, mustEnroll MustEnroll) connect.Interceptor {
	return guard{st, mustEnroll}
}

type guard struct {
	st         *store.Store
	mustEnroll MustEnroll
}

func (g guard) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().IsClient {
			return next(ctx, req)
		}
		ctx, err := g.check(ctx, req.Spec().Procedure, req.Header(), req.Peer().Addr)
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (g guard) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (g guard) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		proc := conn.Spec().Procedure
		if SelfAuthenticated[proc] {
			return next(ctx, conn)
		}
		ctx, err := g.check(ctx, proc, conn.RequestHeader(), conn.Peer().Addr)
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

// check signs the caller in and checks they may call proc, returning ctx
// carrying them.
func (g guard) check(ctx context.Context, proc string, h http.Header, addr string) (context.Context, error) {
	p, err := Authenticate(ctx, g.st, h, addr)
	if err != nil {
		return ctx, err
	}
	if p != nil && p.Session != nil && p.Session.Pending && !PendingAllowed[proc] {
		p = nil // half signed in: only the second factor counts
	}
	if p != nil {
		ctx = WithPrincipal(ctx, *p)
	}
	if Public[proc] {
		return ctx, nil
	}
	perm, known := Required[proc]
	if !known {
		return ctx, ErrForbidden
	}
	if p == nil {
		return ctx, ErrSignedOut
	}
	if perm != "" && !p.Can(perm) {
		return ctx, ErrForbidden
	}
	if p.Session != nil && !p.User.TwoFactor() && !EnrollAllowed[proc] && g.mustEnroll != nil {
		must, err := g.mustEnroll(ctx, p.User)
		if err != nil {
			return ctx, connect.NewError(connect.CodeInternal, err)
		}
		if must {
			return ctx, ErrMustEnroll
		}
	}
	return ctx, nil
}

// Authenticate returns the caller, nil when anonymous, or an error when
// credentials were offered but are wrong. A session only counts from the
// browser that started it: a token replayed elsewhere ends the session.
func Authenticate(ctx context.Context, st *store.Store, h http.Header, remoteAddr string) (*Principal, error) {
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
	token := SessionToken(h)
	if token == "" {
		return nil, nil
	}
	se, user, err := st.LookupSession(ctx, HashToken(token), Device(h.Get("User-Agent")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, nil // stale cookie: treat as signed out
	case errors.Is(err, store.ErrWrongDevice):
		st.DeleteSession(ctx, HashToken(token))
		return nil, nil
	case err != nil:
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if user.Disabled {
		return nil, nil
	}
	return &Principal{User: user, Access: user.Access, Session: &se}, nil
}

// SessionToken reads the session cookie, preferring the secure one.
func SessionToken(h http.Header) string {
	if t := cookie(h, SecureSessionCookie); t != "" {
		return t
	}
	return cookie(h, SessionCookie)
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
func SetSessionCookie(h http.Header, token string, secure bool, ttl time.Duration) {
	c := &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl / time.Second),
	}
	if secure {
		c.Name = SecureSessionCookie
	}
	h.Add("Set-Cookie", c.String())
}

// ClearSessionCookie removes the sign-in cookie, whichever name it had.
func ClearSessionCookie(h http.Header) {
	for _, name := range []string{SessionCookie, SecureSessionCookie} {
		c := &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: name == SecureSessionCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1}
		h.Add("Set-Cookie", c.String())
	}
}
