package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/webhook"
)

func TestWebhooksAndSettings(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)

	// A receiver that checks the signature with the secret it was given.
	var secret string
	got := make(chan string, 10)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !webhook.Verify(secret, r.Header.Get("Mimir-Signature"), body, time.Minute) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got <- r.Header.Get("Mimir-Event")
	}))
	defer receiver.Close()

	// Private addresses are refused unless the server allows them.
	_, err := own.hooks.CreateWebhook(ctx, connect.NewRequest(&mimirv1.CreateWebhookRequest{Name: "h", Url: receiver.URL}))
	if code(err) != connect.CodeInvalidArgument {
		t.Fatalf("private url: %v", err)
	}
	svc.AllowPrivateWebhooks = true
	created, err := own.hooks.CreateWebhook(ctx, connect.NewRequest(&mimirv1.CreateWebhookRequest{Name: "h", Url: receiver.URL, Events: []string{"model.created", "test"}}))
	if err != nil {
		t.Fatal(err)
	}
	secret = created.Msg.GetSecret()
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret = %q", secret)
	}
	hookID := created.Msg.GetWebhook().GetId()

	// A model.created event reaches the receiver once the dispatcher runs.
	createModel(t, own, "Digits", "mnist-mlp")
	dispatcher.Tick(ctx)
	select {
	case ev := <-got:
		if ev != "model.created" {
			t.Fatalf("event = %q", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delivery")
	}
	// Events it didn't subscribe to are not sent.
	list, _ := own.hooks.ListDeliveries(ctx, connect.NewRequest(&mimirv1.ListDeliveriesRequest{WebhookId: hookID}))
	if n := len(list.Msg.GetDeliveries()); n != 1 || list.Msg.GetDeliveries()[0].GetStatus() != mimirv1.DeliveryStatus_DELIVERY_STATUS_DELIVERED {
		t.Fatalf("deliveries = %v", list.Msg.GetDeliveries())
	}
	var payload struct {
		Type string `json:"type"`
		Data struct {
			Model struct{ Name string } `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(list.Msg.GetDeliveries()[0].GetPayload()), &payload); err != nil || payload.Data.Model.Name != "Digits" {
		t.Fatalf("payload = %s, %v", list.Msg.GetDeliveries()[0].GetPayload(), err)
	}

	// Test event and redeliver.
	if _, err := own.hooks.SendTestEvent(ctx, connect.NewRequest(&mimirv1.SendTestEventRequest{Id: hookID})); err != nil {
		t.Fatal(err)
	}
	dispatcher.Tick(ctx)
	if ev := <-got; ev != "test" {
		t.Fatalf("event = %q", ev)
	}
	if _, err := own.hooks.Redeliver(ctx, connect.NewRequest(&mimirv1.RedeliverRequest{Id: list.Msg.GetDeliveries()[0].GetId()})); err != nil {
		t.Fatal(err)
	}
	dispatcher.Tick(ctx)
	if ev := <-got; ev != "model.created" {
		t.Fatalf("redelivered event = %q", ev)
	}

	// Disabled webhooks get nothing.
	if _, err := own.hooks.UpdateWebhook(ctx, connect.NewRequest(&mimirv1.UpdateWebhookRequest{Id: hookID, Name: "h", Url: receiver.URL, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	createModel(t, own, "Other", "mnist-mlp")
	dispatcher.Tick(ctx)
	select {
	case ev := <-got:
		t.Fatalf("disabled webhook got %q", ev)
	case <-time.After(200 * time.Millisecond):
	}

	// Viewers can't manage webhooks or settings.
	_, _ = own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "v@example.com", Name: "V", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}}))
	v := signIn(t, srv, "v@example.com")
	if _, err := v.hooks.ListWebhooks(ctx, connect.NewRequest(&mimirv1.ListWebhooksRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer ListWebhooks: %v", err)
	}
	if _, err := v.settings.GetSettings(ctx, connect.NewRequest(&mimirv1.GetSettingsRequest{})); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer GetSettings: %v", err)
	}

	// Settings round trip; secrets are stored but never returned.
	_, err = own.settings.UpdateAlerts(ctx, connect.NewRequest(&mimirv1.UpdateAlertsRequest{
		Email:    &mimirv1.EmailAlerts{Enabled: true, Host: "smtp.example.com", Port: 587, Username: "u", Password: "p", From: "mimir@example.com", To: []string{"ops@example.com"}},
		Telegram: &mimirv1.TelegramAlerts{Enabled: false, BotToken: "123:abc", ChatId: "42"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := own.settings.GetSettings(ctx, connect.NewRequest(&mimirv1.GetSettingsRequest{}))
	if err != nil || st.Msg.GetEmail().GetPassword() != "" || !st.Msg.GetEmail().GetPasswordSet() || st.Msg.GetTelegram().GetBotToken() != "" || !st.Msg.GetTelegram().GetTokenSet() {
		t.Fatalf("GetSettings = %v, %v", st.Msg, err)
	}
	if st.Msg.GetRetention().GetAuditDays() != 365 || st.Msg.GetRetention().GetDeliveryDays() != 30 {
		t.Fatalf("default retention = %v", st.Msg.GetRetention())
	}
	if _, err := own.settings.UpdateRetention(ctx, connect.NewRequest(&mimirv1.UpdateRetentionRequest{Retention: &mimirv1.Retention{AuditDays: 0, DeliveryDays: 0}})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad retention: %v", err)
	}
	if _, err := own.settings.UpdateRetention(ctx, connect.NewRequest(&mimirv1.UpdateRetentionRequest{Retention: &mimirv1.Retention{AuditDays: 90, DeliveryDays: 7}})); err != nil {
		t.Fatal(err)
	}
	if _, err := own.settings.SendTestAlert(ctx, connect.NewRequest(&mimirv1.SendTestAlertRequest{Channel: "telegram"})); code(err) != connect.CodeUnavailable {
		t.Fatalf("test alert with unreachable bot: %v", err)
	}
	info, err := own.settings.GetDatabaseInfo(ctx, connect.NewRequest(&mimirv1.GetDatabaseInfoRequest{}))
	if err != nil || info.Msg.GetKind() != "postgres" || !strings.HasPrefix(info.Msg.GetPath(), "postgres://") || info.Msg.GetLatestSchemaVersion() < 1 || info.Msg.GetSchemaVersion() != info.Msg.GetLatestSchemaVersion() || info.Msg.GetSizeBytes() <= 0 || info.Msg.GetModels() != 2 {
		t.Fatalf("GetDatabaseInfo = %v, %v", info.Msg, err)
	}

	// Backup download needs an admin cookie.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/backup", nil)
	res, err := srv.Client().Do(req)
	if err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous backup: %v %v", res.Status, err)
	}
	req.Header.Set("Cookie", own.jar.cookie)
	res, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if _, err := exec.LookPath("pg_dump"); err != nil {
		// Without pg_dump the server says how to back up instead.
		if res.StatusCode != http.StatusNotImplemented || !strings.Contains(string(b), "pg_dump") {
			t.Fatalf("backup without pg_dump: %s, %q", res.Status, b)
		}
		return
	}
	// pg_dump's custom format starts with PGDMP.
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(string(b), "PGDMP") {
		t.Fatalf("backup: %s, %d bytes: %.200s", res.Status, len(b), b)
	}
}
