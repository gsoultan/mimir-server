package setup

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Server answers the AuthService calls the setup screen makes while the
// console has no database. Every other call fails until setup is done.
type Server struct {
	mimirv1connect.UnimplementedAuthServiceHandler

	DataDir    string
	ConfigPath string
	Key        Key
	Proxies    auth.Proxies
	Log        *slog.Logger
	// Done gets the configuration, once, when setup succeeds.
	Done chan config.File

	token   string
	limiter *auth.Limiter

	mu       sync.Mutex
	finished bool
}

// NewServer opens setup with token, which every change needs.
func NewServer(token string) *Server {
	return &Server{token: token, limiter: auth.NewLimiter(10, 15*time.Minute), Done: make(chan config.File, 1)}
}

var _ mimirv1connect.AuthServiceHandler = (*Server)(nil)

// Finished reports whether setup is done; the full server starts next.
func (s *Server) Finished() bool { return s.isFinished() }

func (s *Server) isFinished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

func (s *Server) GetSetupState(context.Context, *connect.Request[mimirv1.GetSetupStateRequest]) (*connect.Response[mimirv1.GetSetupStateResponse], error) {
	if s.isFinished() {
		return connect.NewResponse(&mimirv1.GetSetupStateResponse{}), nil
	}
	src := map[KeySource]mimirv1.SecretKeySource{
		KeyNone:        mimirv1.SecretKeySource_SECRET_KEY_SOURCE_NONE,
		KeyEnvironment: mimirv1.SecretKeySource_SECRET_KEY_SOURCE_ENVIRONMENT,
		KeyFile:        mimirv1.SecretKeySource_SECRET_KEY_SOURCE_FILE,
	}[s.Key.Source]
	r := api.DefaultRetention
	return connect.NewResponse(&mimirv1.GetSetupStateResponse{
		NeedsSetup: true, NeedsConfig: true, SecretKey: src,
		Retention: &mimirv1.Retention{AuditDays: int32(r.AuditDays), DeliveryDays: int32(r.DeliveryDays)},
	}), nil
}

// WhoAmI says nobody is signed in, which sends the UI to the setup screen;
// once setup is done it says the full server is on its way.
func (s *Server) WhoAmI(context.Context, *connect.Request[mimirv1.WhoAmIRequest]) (*connect.Response[mimirv1.WhoAmIResponse], error) {
	if s.isFinished() {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("The server is starting. Try again in a moment."))
	}
	return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("This server isn't set up yet."))
}

// guard checks the setup token, counting wrong ones against the caller's
// address.
func (s *Server) guard(header http.Header, peer, token string) error {
	if s.isFinished() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("This server is already set up. Sign in instead."))
	}
	ipKey := "ip:" + s.Proxies.ClientIP(header, peer)
	if wait := s.limiter.Wait(ipKey); wait > 0 {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("Too many attempts. Try again in %d minutes.", int(wait.Minutes())+1))
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(s.token)) != 1 {
		s.limiter.Fail(ipKey)
		return connect.NewError(connect.CodePermissionDenied, errors.New("That setup token isn't right. The server prints it in its log when it starts without a configuration."))
	}
	return nil
}

func (s *Server) CheckDatabase(ctx context.Context, req *connect.Request[mimirv1.CheckDatabaseRequest]) (*connect.Response[mimirv1.CheckDatabaseResponse], error) {
	if err := s.guard(req.Header(), req.Peer().Addr, req.Msg.GetSetupToken()); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(req.Msg.GetDatabaseUrl())
	if err := CheckURL(raw, "database URL"); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	version, has, err := store.CheckDatabase(ctx, raw)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Couldn't connect: %s", Scrub(err.Error(), raw)))
	}
	return connect.NewResponse(&mimirv1.CheckDatabaseResponse{Version: version, HasAccounts: has}), nil
}

func (s *Server) Setup(ctx context.Context, req *connect.Request[mimirv1.SetupRequest]) (*connect.Response[mimirv1.SetupResponse], error) {
	m := req.Msg
	if err := s.guard(req.Header(), req.Peer().Addr, m.GetSetupToken()); err != nil {
		return nil, err
	}
	// One setup at a time, and only one ever.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("This server is already set up. Sign in instead."))
	}

	p := Plan{
		DataDir: s.DataDir, ConfigPath: s.ConfigPath, Key: s.Key,
		DatabaseURL:    strings.TrimSpace(m.GetDatabaseUrl()),
		LogDatabaseURL: strings.TrimSpace(m.GetLogDatabaseUrl()),
	}
	if p.LogDatabaseURL == p.DatabaseURL {
		p.LogDatabaseURL = ""
	}
	invalid := func(err error) error { return connect.NewError(connect.CodeInvalidArgument, err) }
	if err := CheckURL(p.DatabaseURL, "database URL"); err != nil {
		return nil, invalid(err)
	}
	if p.LogDatabaseURL != "" {
		if err := CheckURL(p.LogDatabaseURL, "log database URL"); err != nil {
			return nil, invalid(err)
		}
	}
	if k := strings.TrimSpace(m.GetSecretKey()); k != "" && s.Key.Source == KeyNone {
		key, err := secret.ParseKey(k)
		if err != nil {
			return nil, invalid(errors.New("The secret key must be 32 bytes written as base64 or hex, such as the output of `openssl rand -base64 32`."))
		}
		p.NewKey = key
	}
	if r := m.GetRetention(); r != nil {
		rr, err := api.CheckRetention(int(r.GetAuditDays()), int(r.GetDeliveryDays()))
		if err != nil {
			return nil, err
		}
		p.Retention = &rr
	}

	// The owner is needed only for a database without accounts; check
	// their details before anything is written.
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	_, has, err := store.CheckDatabase(cctx, p.DatabaseURL)
	cancel()
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Couldn't connect to the database: %s", Scrub(err.Error(), p.DatabaseURL)))
	}
	if !has {
		hash, email, name, err := api.NewOwner(m.GetEmail(), m.GetName(), m.GetPassword())
		if err != nil {
			return nil, err
		}
		p.Owner = &Owner{Email: email, Name: name, PasswordHash: hash}
	}

	res, err := Apply(ctx, p)
	if err != nil {
		msg := Scrub(Scrub(err.Error(), p.DatabaseURL), p.LogDatabaseURL)
		s.Log.ErrorContext(ctx, "setup failed", "error", msg)
		switch {
		case errors.Is(err, secret.ErrWrongKey):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("That database has secrets sealed with another key. Give the key it was set up with."))
		case errors.Is(err, config.ErrExists):
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("A configuration file appeared while setting up. Restart the server."))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Setup failed: %s", msg))
	}
	s.finished = true
	s.Log.InfoContext(ctx, "setup: done", "config", s.ConfigPath,
		"database", store.Redact(p.DatabaseURL), "log_database", redactOrNone(p.LogDatabaseURL),
		"key_generated", res.KeyGenerated, "key_saved", res.KeySaved, "owner_created", res.OwnerCreated, "migration_steps", res.MigrationSteps)
	if res.KeySaved {
		s.Log.WarnContext(ctx, "secret key: saved; back it up with the database, which cannot be read without it", "path", s.Key.File)
	}
	s.Done <- res.Config
	return connect.NewResponse(&mimirv1.SetupResponse{SignInNext: true}), nil
}

func redactOrNone(u string) string {
	if u == "" {
		return ""
	}
	return store.Redact(u)
}
