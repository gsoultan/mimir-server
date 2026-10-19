package server_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/testdb"
	"github.com/gsoultan/mimir-server/internal/training"
	"github.com/gsoultan/mimir-server/internal/webhook"
	"github.com/gsoultan/mimir/io/mim"
)

const password = "correct horse battery"

// svc and dispatcher belong to the most recent newServer, so tests can
// flip server-side switches and run deliveries by hand.
var (
	svc        *api.Service
	dispatcher *webhook.Dispatcher
	dbURL      string // the test server's own database
	runner     *training.Runner
	setupToken string // the one-time token Setup needs on a fresh server
)

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	return startServer(t, false)
}

// startServer starts a test server, serving TLS with a certificate of
// httptest's own when tls is set.
func startServer(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	dbURL = testdb.URL(t)
	st, err := store.Open(context.Background(), dbURL, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>mimir</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	svc = api.New(st, log)
	svc.Notifier.TelegramAPI = "http://127.0.0.1:1" // nothing listens there
	dispatcher = webhook.New(st, log, true)
	svc.Pool = cluster.New(log)
	svc.Pool.OnSample = svc.KeepSample
	runner = training.New(st, log)
	runner.Remote = svc.Pool
	runner.OnDone = svc.TrainingDone
	svc.CancelTraining = runner.Cancel
	if setupToken, err = svc.PrepareSetup(context.Background(), ""); err != nil || setupToken == "" {
		t.Fatalf("PrepareSetup = %q, %v", setupToken, err)
	}
	srv := httptest.NewUnstartedServer(server.Handler(svc, st, ui, log))
	srv.Config.Protocols = server.Protocols()
	if tls {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// cookieJar is a client option that replays one Set-Cookie value, like a
// browser would.
type cookieJar struct{ cookie string }

func (j *cookieJar) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if j.cookie != "" {
			req.Header().Set("Cookie", j.cookie)
		}
		res, err := next(ctx, req)
		if res != nil {
			if c := res.Header().Get("Set-Cookie"); c != "" {
				j.cookie, _, _ = strings.Cut(c, ";")
			}
		}
		return res, err
	}
}
func (j *cookieJar) WrapStreamingClient(n connect.StreamingClientFunc) connect.StreamingClientFunc {
	return n
}
func (j *cookieJar) WrapStreamingHandler(n connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return n
}

type clients struct {
	jar      *cookieJar
	auth     mimirv1connect.AuthServiceClient
	models   mimirv1connect.ModelServiceClient
	predict  mimirv1connect.PredictServiceClient
	users    mimirv1connect.UserServiceClient
	keys     mimirv1connect.ApiKeyServiceClient
	audit    mimirv1connect.AuditServiceClient
	hooks    mimirv1connect.WebhookServiceClient
	settings mimirv1connect.SettingsServiceClient
	datasets mimirv1connect.DatasetServiceClient
	training mimirv1connect.TrainingServiceClient
	workers  mimirv1connect.WorkerServiceClient
	teams    mimirv1connect.TeamServiceClient
}

func newClients(srv *httptest.Server, opts ...connect.ClientOption) clients {
	jar := &cookieJar{}
	opts = append(opts, connect.WithInterceptors(jar))
	return clients{
		jar:      jar,
		auth:     mimirv1connect.NewAuthServiceClient(srv.Client(), srv.URL, opts...),
		models:   mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL, opts...),
		predict:  mimirv1connect.NewPredictServiceClient(srv.Client(), srv.URL, opts...),
		users:    mimirv1connect.NewUserServiceClient(srv.Client(), srv.URL, opts...),
		keys:     mimirv1connect.NewApiKeyServiceClient(srv.Client(), srv.URL, opts...),
		audit:    mimirv1connect.NewAuditServiceClient(srv.Client(), srv.URL, opts...),
		hooks:    mimirv1connect.NewWebhookServiceClient(srv.Client(), srv.URL, opts...),
		settings: mimirv1connect.NewSettingsServiceClient(srv.Client(), srv.URL, opts...),
		datasets: mimirv1connect.NewDatasetServiceClient(srv.Client(), srv.URL, opts...),
		training: mimirv1connect.NewTrainingServiceClient(srv.Client(), srv.URL, opts...),
		workers:  mimirv1connect.NewWorkerServiceClient(srv.Client(), srv.URL, opts...),
		teams:    mimirv1connect.NewTeamServiceClient(srv.Client(), srv.URL, opts...),
	}
}

// owner sets the server up and returns clients signed in as its owner.
func owner(t *testing.T, srv *httptest.Server) clients {
	t.Helper()
	c := newClients(srv)
	_, err := c.auth.Setup(context.Background(), connect.NewRequest(&mimirv1.SetupRequest{Email: "owner@example.com", Name: "Owner", Password: password, SetupToken: setupToken}))
	if err != nil {
		t.Fatal(err)
	}
	if c.jar.cookie == "" {
		t.Fatal("setup set no cookie")
	}
	return c
}

func signIn(t *testing.T, srv *httptest.Server, email string) clients {
	t.Helper()
	c := newClients(srv)
	if _, err := c.auth.SignIn(context.Background(), connect.NewRequest(&mimirv1.SignInRequest{Email: email, Password: password})); err != nil {
		t.Fatal(err)
	}
	return c
}

func weights(t *testing.T, name string) []byte {
	t.Helper()
	a, ok := arch.Lookup(name)
	if !ok {
		t.Fatalf("no architecture %q", name)
	}
	var buf bytes.Buffer
	if err := mim.Save(context.Background(), &buf, a.New(a.Spec), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func createModel(t *testing.T, c clients, name, archName string) *mimirv1.Model {
	t.Helper()
	res, err := c.models.CreateModel(context.Background(), connect.NewRequest(&mimirv1.CreateModelRequest{Name: name, Architecture: archName, Weights: weights(t, archName)}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.GetModel()
}

func code(err error) connect.Code {
	if err == nil {
		return 0
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func TestSetupAndSignIn(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	anon := newClients(srv)

	state, err := anon.auth.GetSetupState(ctx, connect.NewRequest(&mimirv1.GetSetupStateRequest{}))
	if err != nil || !state.Msg.GetNeedsSetup() {
		t.Fatalf("GetSetupState = %v, %v", state, err)
	}
	if _, err := anon.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous ListModels: %v", err)
	}
	// Setup needs the token from the server's log: without it, or with a
	// wrong one, nobody who merely found the server can claim it.
	for _, tok := range []string{"", "not-the-token-at-all", setupToken + "x"} {
		_, err = anon.auth.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{Email: "x@example.com", Name: "x", Password: password, SetupToken: tok}))
		if code(err) != connect.CodePermissionDenied {
			t.Fatalf("setup with token %q: %v", tok, err)
		}
	}
	if state, _ := anon.auth.GetSetupState(ctx, connect.NewRequest(&mimirv1.GetSetupStateRequest{})); !state.Msg.GetNeedsSetup() {
		t.Fatal("a refused setup made an account")
	}
	// $MIMIR_SETUP_TOKEN presets the token, if it's long enough.
	if _, err := svc.PrepareSetup(ctx, "too-short"); err == nil {
		t.Fatal("a short preset setup token was accepted")
	}
	const preset = "preset-setup-token-0123456789"
	if tok, err := svc.PrepareSetup(ctx, preset); err != nil || tok != preset {
		t.Fatalf("PrepareSetup(preset) = %q, %v", tok, err)
	}
	_, err = anon.auth.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{Email: "x@example.com", Name: "x", Password: password, SetupToken: setupToken}))
	if code(err) != connect.CodePermissionDenied {
		t.Fatalf("setup with the replaced token: %v", err)
	}
	setupToken = preset
	_, err = anon.auth.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{Email: "x@example.com", Name: "x", Password: "short", SetupToken: setupToken}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("weak password: %v", err)
	}

	own := owner(t, srv)
	_, err = anon.auth.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{Email: "again@example.com", Name: "x", Password: password, SetupToken: setupToken}))
	if code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second setup: %v", err)
	}
	if tok, err := svc.PrepareSetup(ctx, ""); err != nil || tok != "" {
		t.Fatalf("PrepareSetup once set up = %q, %v", tok, err)
	}
	me, err := own.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if err != nil || me.Msg.GetUser().GetAccess().GetRole() != mimirv1.Role_ROLE_OWNER || len(me.Msg.GetPermissions()) == 0 {
		t.Fatalf("WhoAmI = %v, %v", me, err)
	}

	bad := newClients(srv)
	_, err = bad.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "owner@example.com", Password: "nope nope nope"}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("wrong password: %v", err)
	}
	_, err = bad.auth.SignIn(ctx, connect.NewRequest(&mimirv1.SignInRequest{Email: "ghost@example.com", Password: password}))
	if code(err) != connect.CodeUnauthenticated {
		t.Fatalf("unknown user: %v", err)
	}

	again := signIn(t, srv, "OWNER@example.com")
	if _, err := again.auth.SignOut(ctx, connect.NewRequest(&mimirv1.SignOutRequest{})); err != nil {
		t.Fatal(err)
	}
	if _, err := again.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("after sign out: %v", err)
	}
}

func TestModelLifecycle(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)

	archs, err := c.models.ListArchitectures(ctx, connect.NewRequest(&mimirv1.ListArchitecturesRequest{}))
	if err != nil || len(archs.Msg.GetArchitectures()) == 0 {
		t.Fatalf("ListArchitectures = %v, %v", archs, err)
	}

	for _, name := range []string{"mnist-mlp", "mnist-cnn"} {
		t.Run(name, func(t *testing.T) {
			m := createModel(t, c, "  Digits  ", name)
			if m.GetName() != "Digits" || m.GetStatus() != mimirv1.ModelStatus_MODEL_STATUS_READY {
				t.Fatalf("CreateModel = %v", m)
			}
			res, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{
				ModelId: m.GetId(),
				Input:   &mimirv1.Tensor{Shape: []int64{1, 28, 28}, Values: make([]float32, 784)},
			}))
			if err != nil {
				t.Fatal(err)
			}
			var sum float64
			for _, p := range res.Msg.GetProbabilities().GetValues() {
				sum += float64(p)
			}
			if n := len(res.Msg.GetProbabilities().GetValues()); n != 10 || math.Abs(sum-1) > 1e-4 {
				t.Fatalf("output has %d values summing to %v", n, sum)
			}
			if g := res.Msg.GetGuesses(); len(g) != 3 || g[0].GetProbability() < g[1].GetProbability() {
				t.Fatalf("guesses = %v", g)
			}
			if _, err := c.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: m.GetId()})); err != nil {
				t.Fatal(err)
			}
			_, err = c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: m.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}}))
			if code(err) != connect.CodeNotFound {
				t.Fatalf("Predict after delete: %v", err)
			}
		})
	}

	events, err := c.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{PageSize: 2}))
	if err != nil || len(events.Msg.GetEvents()) != 2 || events.Msg.GetNextPageToken() == "" {
		t.Fatalf("ListAuditEvents = %v, %v", events, err)
	}
	if a := events.Msg.GetEvents()[0]; a.GetAction() != "model.delete" || a.GetActor() != "owner@example.com" {
		t.Fatalf("latest audit event = %v", a)
	}
}

func TestErrors(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	c := owner(t, srv)
	id := createModel(t, c, "Digits", "mnist-mlp").GetId()

	tests := []struct {
		name string
		call func() error
		want connect.Code
	}{
		{"wrong input size", func() error {
			_, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, Input: &mimirv1.Tensor{Values: make([]float32, 5)}}))
			return err
		}, connect.CodeInvalidArgument},
		{"unknown model", func() error {
			_, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: "0190a5c6-0000-7000-8000-000000000000"}))
			return err
		}, connect.CodeNotFound},
		{"malformed id", func() error {
			_, err := c.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: "../etc/passwd"}))
			return err
		}, connect.CodeNotFound},
		{"weights for another type", func() error {
			_, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "mnist-cnn", Weights: weights(t, "mnist-mlp")}))
			return err
		}, connect.CodeInvalidArgument},
		{"garbage weights", func() error {
			_, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "mnist-mlp", Weights: []byte("nope")}))
			return err
		}, connect.CodeInvalidArgument},
		{"unknown architecture", func() error {
			_, err := c.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "x", Architecture: "gpt", Weights: []byte("x")}))
			return err
		}, connect.CodeInvalidArgument},
		{"empty name", func() error {
			_, err := c.models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: id, Name: "   "}))
			return err
		}, connect.CodeInvalidArgument},
		{"bad top_k", func() error {
			_, err := c.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: id, TopK: -1, Input: &mimirv1.Tensor{Values: make([]float32, 784)}}))
			return err
		}, connect.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := code(tt.call()); got != tt.want {
				t.Fatalf("code = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRolesAndModelAccess(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	a := createModel(t, own, "A", "mnist-mlp")
	b := createModel(t, own, "B", "mnist-mlp")

	// A viewer who may only reach model A.
	_, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{
		Email: "viewer@example.com", Name: "Vee", Password: password,
		Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{a.GetId()}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	v := signIn(t, srv, "viewer@example.com")

	list, err := v.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil || len(list.Msg.GetModels()) != 1 || list.Msg.GetModels()[0].GetId() != a.GetId() {
		t.Fatalf("viewer ListModels = %v, %v", list, err)
	}
	if _, err := v.models.GetModel(ctx, connect.NewRequest(&mimirv1.GetModelRequest{Id: b.GetId()})); code(err) != connect.CodeNotFound {
		t.Fatalf("viewer GetModel(B): %v", err)
	}
	if _, err := v.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: a.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}})); err != nil {
		t.Fatalf("viewer Predict(A): %v", err)
	}
	if _, err := v.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: a.GetId()})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer DeleteModel: %v", err)
	}
	if _, err := v.users.ListUsers(ctx, connect.NewRequest(&mimirv1.ListUsersRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer ListUsers: %v", err)
	}
	if _, err := v.audit.ListAuditEvents(ctx, connect.NewRequest(&mimirv1.ListAuditEventsRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer ListAuditEvents: %v", err)
	}

	// An admin can manage viewers but not owners, and can't make owners.
	_, err = own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{
		Email: "admin@example.com", Name: "Ada", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN},
	}))
	if err != nil {
		t.Fatal(err)
	}
	ad := signIn(t, srv, "admin@example.com")
	users, err := ad.users.ListUsers(ctx, connect.NewRequest(&mimirv1.ListUsersRequest{}))
	if err != nil || len(users.Msg.GetUsers()) != 3 {
		t.Fatalf("admin ListUsers = %v, %v", users, err)
	}
	var ownerID, viewerID string
	for _, u := range users.Msg.GetUsers() {
		switch u.GetEmail() {
		case "owner@example.com":
			ownerID = u.GetId()
		case "viewer@example.com":
			viewerID = u.GetId()
		}
	}
	_, err = ad.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: ownerID, Name: "Pwned", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER}}))
	if code(err) != connect.CodePermissionDenied {
		t.Fatalf("admin editing owner: %v", err)
	}
	_, err = ad.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "o2@example.com", Name: "x", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_OWNER}}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("admin making owner: %v", err)
	}
	// Admin widens the viewer to all models, then disables them.
	_, err = ad.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: viewerID, Name: "Vee", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	list, err = v.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{}))
	if err != nil || len(list.Msg.GetModels()) != 2 {
		t.Fatalf("widened ListModels = %v, %v", list, err)
	}
	_, err = ad.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: viewerID, Name: "Vee", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}, Disabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("disabled user: %v", err)
	}

	// The last owner can't be demoted or deleted.
	_, err = own.users.UpdateUser(ctx, connect.NewRequest(&mimirv1.UpdateUserRequest{Id: ownerID, Name: "Owner", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_ADMIN}}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("demote self: %v", err)
	}
	if _, err := own.users.DeleteUser(ctx, connect.NewRequest(&mimirv1.DeleteUserRequest{Id: ownerID})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("delete self: %v", err)
	}
}

func TestAPIKeys(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	a := createModel(t, own, "A", "mnist-mlp")
	b := createModel(t, own, "B", "mnist-mlp")

	created, err := own.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{
		Name: "my app", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{a.GetId()}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret := created.Msg.GetSecret()
	if !strings.HasPrefix(secret, "mimir_") || !strings.HasPrefix(secret, created.Msg.GetApiKey().GetPrefix()) {
		t.Fatalf("secret = %q, prefix = %q", secret, created.Msg.GetApiKey().GetPrefix())
	}

	bearer := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+secret)
			return next(ctx, req)
		}
	}))
	app := newClients(srv, bearer)
	if _, err := app.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: a.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}})); err != nil {
		t.Fatalf("key Predict(A): %v", err)
	}
	if _, err := app.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: b.GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}})); code(err) != connect.CodeNotFound {
		t.Fatalf("key Predict(B): %v", err)
	}
	if _, err := app.models.DeleteModel(ctx, connect.NewRequest(&mimirv1.DeleteModelRequest{Id: a.GetId()})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer key DeleteModel: %v", err)
	}
	if _, err := app.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: "more"})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("key making key: %v", err)
	}
	me, err := app.auth.WhoAmI(ctx, connect.NewRequest(&mimirv1.WhoAmIRequest{}))
	if err != nil || me.Msg.GetUser().GetAccess().GetRole() != mimirv1.Role_ROLE_VIEWER {
		t.Fatalf("key WhoAmI = %v, %v", me, err)
	}

	keys, err := own.keys.ListApiKeys(ctx, connect.NewRequest(&mimirv1.ListApiKeysRequest{}))
	if err != nil || len(keys.Msg.GetApiKeys()) != 1 || keys.Msg.GetApiKeys()[0].GetLastUsedTime() == nil {
		t.Fatalf("ListApiKeys = %v, %v", keys, err)
	}
	if _, err := own.keys.RevokeApiKey(ctx, connect.NewRequest(&mimirv1.RevokeApiKeyRequest{Id: created.Msg.GetApiKey().GetId()})); err != nil {
		t.Fatal(err)
	}
	if _, err := app.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("revoked key: %v", err)
	}

	wrong := newClients(srv, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer mimir_nope")
			return next(ctx, req)
		}
	})))
	if _, err := wrong.models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("bad key: %v", err)
	}
}

func TestGRPCAndHealth(t *testing.T) {
	srv := newServer(t)
	web := mimirv1connect.NewAuthServiceClient(srv.Client(), srv.URL, connect.WithGRPCWeb())
	if _, err := web.GetSetupState(context.Background(), connect.NewRequest(&mimirv1.GetSetupStateRequest{})); err != nil {
		t.Fatalf("gRPC-Web: %v", err)
	}

	// Plain gRPC needs HTTP/2; the server accepts it without TLS.
	h2c := new(http.Protocols)
	h2c.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{Protocols: h2c}}
	grpc := mimirv1connect.NewAuthServiceClient(client, srv.URL, connect.WithGRPC())
	if _, err := grpc.GetSetupState(context.Background(), connect.NewRequest(&mimirv1.GetSetupStateRequest{})); err != nil {
		t.Fatalf("gRPC over h2c: %v", err)
	}
	res, err := http.Post(srv.URL+"/grpc.health.v1.Health/Check", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", res.StatusCode)
	}
}

func TestWebUI(t *testing.T) {
	srv := newServer(t)
	tests := []struct {
		path   string
		status int
		cache  string
	}{
		{"/", 200, "no-cache"},
		{"/models/123", 200, "no-cache"},
		{"/assets/app.js", 200, "public, max-age=31536000, immutable"},
		{"/assets/missing.js", 404, ""},
	}
	for _, tt := range tests {
		res, err := http.Get(srv.URL + tt.path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != tt.status || (tt.cache != "" && res.Header.Get("Cache-Control") != tt.cache) {
			t.Errorf("GET %s = %d %q", tt.path, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
}
