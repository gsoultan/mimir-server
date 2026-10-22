package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/api"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/config"
	"github.com/gsoultan/mimir-server/internal/secret"
	"github.com/gsoultan/mimir-server/internal/server"
	"github.com/gsoultan/mimir-server/internal/setup"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/internal/testdb"
)

const firstRunToken = "first-run-setup-token-123"

func startSetup(t *testing.T) (*setup.Server, *httptest.Server, mimirv1connect.AuthServiceClient) {
	t.Helper()
	dir := t.TempDir()
	key, err := setup.FindKey("", "", dir)
	if err != nil {
		t.Fatal(err)
	}
	s := setup.NewServer(firstRunToken)
	s.DataDir, s.ConfigPath, s.Key = dir, config.Path("", dir), key
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	ui := fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>mimir</title>")}}
	srv := httptest.NewServer(server.SetupHandler(s, auth.Proxies{}, ui, s.Log))
	t.Cleanup(srv.Close)
	return s, srv, mimirv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
}

func TestFirstRunSetup(t *testing.T) {
	ctx := context.Background()
	s, srv, c := startSetup(t)
	mainURL, logURL := testdb.URL(t), testdb.URL(t)

	st, err := c.GetSetupState(ctx, connect.NewRequest(&mimirv1.GetSetupStateRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Msg.NeedsSetup || !st.Msg.NeedsConfig || st.Msg.SecretKey != mimirv1.SecretKeySource_SECRET_KEY_SOURCE_NONE || st.Msg.Retention.GetAuditDays() != int32(api.DefaultRetention.AuditDays) {
		t.Fatalf("state before setup: %+v", st.Msg)
	}

	// Nothing but the setup calls answers yet, and the UI loads.
	models := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL)
	if _, err := models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnavailable {
		t.Fatalf("ListModels before setup: %v", err)
	}
	if res, err := srv.Client().Get(srv.URL + "/setup"); err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("UI: %v %v", res, err)
	}

	if _, err := c.CheckDatabase(ctx, connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: "wrong", DatabaseUrl: mainURL})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("CheckDatabase with a wrong token: %v", err)
	}
	chk, err := c.CheckDatabase(ctx, connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: firstRunToken, DatabaseUrl: mainURL}))
	if err != nil || !strings.HasPrefix(chk.Msg.Version, "PostgreSQL ") || chk.Msg.HasAccounts {
		t.Fatalf("CheckDatabase: %v, %v", chk, err)
	}
	// A database that isn't there says so without its password.
	_, err = c.CheckDatabase(ctx, connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: firstRunToken, DatabaseUrl: "postgres://mimir:s3cr3t-pw@127.0.0.1:1/x?connect_timeout=2"}))
	if code(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), "s3cr3t-pw") {
		t.Fatalf("CheckDatabase of a missing database: %v", err)
	}

	req := func(m *mimirv1.SetupRequest) *connect.Request[mimirv1.SetupRequest] {
		m.SetupToken = firstRunToken
		if m.Email == "" {
			m.Email, m.Name, m.Password = "owner@example.com", "Owner", "a long enough password"
		}
		return connect.NewRequest(m)
	}
	// References to the server's environment and files are for the
	// configuration file, not the setup screen.
	if _, err := c.Setup(ctx, req(&mimirv1.SetupRequest{DatabaseUrl: "file:/etc/passwd"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("Setup with a file reference: %v", err)
	}
	if _, err := c.Setup(ctx, req(&mimirv1.SetupRequest{DatabaseUrl: mainURL, Email: "owner@example.com", Name: "Owner", Password: "short"})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("Setup with a short password: %v", err)
	}
	if _, err := c.Setup(ctx, req(&mimirv1.SetupRequest{DatabaseUrl: mainURL, Retention: &mimirv1.Retention{AuditDays: -1, DeliveryDays: 1}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("Setup with a bad retention: %v", err)
	}
	if _, err := os.Stat(s.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed setup wrote the configuration")
	}

	res, err := c.Setup(ctx, req(&mimirv1.SetupRequest{DatabaseUrl: mainURL, LogDatabaseUrl: logURL, Retention: &mimirv1.Retention{AuditDays: 90, DeliveryDays: 7}}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.SignInNext {
		t.Fatal("setup didn't say to sign in next")
	}
	var done config.File
	select {
	case done = <-s.Done:
	default:
		t.Fatal("setup sent no configuration")
	}
	if done.DatabaseURL != mainURL || done.LogDatabaseURL != logURL {
		t.Fatalf("configuration: %+v", done)
	}

	// Setup is closed now, and other calls say the server is starting.
	if _, err := c.Setup(ctx, req(&mimirv1.SetupRequest{DatabaseUrl: mainURL})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("second Setup: %v", err)
	}
	if _, err := c.CheckDatabase(ctx, connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: firstRunToken, DatabaseUrl: mainURL})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CheckDatabase after setup: %v", err)
	}
	if st, _ := c.GetSetupState(ctx, connect.NewRequest(&mimirv1.GetSetupStateRequest{})); st.Msg.NeedsSetup || st.Msg.NeedsConfig {
		t.Fatalf("state after setup: %+v", st.Msg)
	}
	if _, err := models.ListModels(ctx, connect.NewRequest(&mimirv1.ListModelsRequest{})); code(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "starting") {
		t.Fatalf("ListModels after setup: %v", err)
	}

	// The file and the key are private, and the file holds no URL in the
	// clear.
	for _, p := range []string{s.ConfigPath, s.Key.File} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %04o, want 0600", filepath.Base(p), info.Mode().Perm())
		}
	}
	raw, _ := os.ReadFile(s.ConfigPath)
	if strings.Contains(string(raw), mainURL) || strings.Contains(string(raw), logURL) {
		t.Fatalf("the configuration holds a database URL in the clear: %s", raw)
	}
	var onDisk config.File
	json.Unmarshal(raw, &onDisk)
	if !secret.Sealed(onDisk.DatabaseURL) || !secret.Sealed(onDisk.LogDatabaseURL) {
		t.Fatalf("URLs not sealed: %s", raw)
	}

	// The server starts from what setup left: the owner, the retention and
	// the first audit event, in the log database.
	f, err := config.Read(s.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	key, _, err := secret.Load("", f.SecretKeyFile, s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := secret.New(key)
	if f, err = f.Open(b); err != nil {
		t.Fatal(err)
	}
	full, err := store.Open(ctx, f.DatabaseURL, s.DataDir, store.WithSecretKey(key), store.WithLogDatabase(f.LogDatabaseURL))
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	if n, _ := full.CountUsers(ctx); n != 1 {
		t.Fatalf("%d users, want the owner", n)
	}
	if r, _ := api.LoadRetention(ctx, full); r.AuditDays != 90 || r.DeliveryDays != 7 {
		t.Fatalf("retention %+v", r)
	}
	if ev, _ := full.ListAuditEvents(ctx, "", "", 10); len(ev) != 1 || ev[0].Action != "server.setup" {
		t.Fatalf("audit: %+v", ev)
	}

	// Then the owner step is shut as well: the full server's setup refuses.
	svc := api.New(full, s.Log)
	if tok, err := svc.PrepareSetup(ctx, ""); err != nil || tok != "" {
		t.Fatalf("PrepareSetup after first-run setup: %q, %v", tok, err)
	}
}

// Pointed at a database that already has accounts, setup keeps them and
// needs no owner.
func TestFirstRunSetupKeepsAccounts(t *testing.T) {
	ctx := context.Background()
	url := testdb.URL(t)
	dir := t.TempDir()
	key, _ := secret.Generate()
	st, err := store.Open(ctx, url, dir, store.WithSecretKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, store.User{Email: "old@example.com", Name: "Old", PasswordHash: "x", Access: store.Access{Role: store.Owner, AllModels: true}}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	_, _, c := startSetup(t)
	chk, err := c.CheckDatabase(ctx, connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: firstRunToken, DatabaseUrl: url}))
	if err != nil || !chk.Msg.HasAccounts {
		t.Fatalf("CheckDatabase: %v, %v", chk, err)
	}
	if _, err := c.Setup(ctx, connect.NewRequest(&mimirv1.SetupRequest{SetupToken: firstRunToken, DatabaseUrl: url})); err != nil {
		t.Fatal(err)
	}
}

// The full server's Setup takes no configuration.
func TestSetupRefusesConfigOnceConfigured(t *testing.T) {
	srv := newServer(t)
	c := newClients(srv)
	_, err := c.auth.Setup(context.Background(), connect.NewRequest(&mimirv1.SetupRequest{Email: "owner@example.com", Name: "Owner", Password: password, SetupToken: setupToken, DatabaseUrl: "postgres://a@b/c"}))
	if code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("Setup with a database URL: %v", err)
	}
	if _, err := c.auth.CheckDatabase(context.Background(), connect.NewRequest(&mimirv1.CheckDatabaseRequest{SetupToken: setupToken, DatabaseUrl: "postgres://a@b/c"})); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("CheckDatabase: %v", err)
	}
	// Retention given with the first owner is kept.
	res, err := c.auth.Setup(context.Background(), connect.NewRequest(&mimirv1.SetupRequest{Email: "owner@example.com", Name: "Owner", Password: password, SetupToken: setupToken, Retention: &mimirv1.Retention{AuditDays: 30, DeliveryDays: 3}}))
	if err != nil || res.Msg.SignInNext {
		t.Fatalf("Setup: %v, %v", res, err)
	}
	got, err := c.settings.GetSettings(context.Background(), connect.NewRequest(&mimirv1.GetSettingsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if r := got.Msg.Retention; r.AuditDays != 30 || r.DeliveryDays != 3 {
		t.Fatalf("retention %+v", r)
	}
}
