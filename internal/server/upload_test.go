package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/api"
)

// sendUpload posts body to /api/uploads with the given credentials, a
// cookie or an Authorization header, and returns the status and answer.
func sendUpload(t *testing.T, srv *httptest.Server, cookie, key string, body []byte) (int, struct{ Upload, Code, Message string }) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/uploads", bytes.NewReader(body))
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct{ Upload, Code, Message string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("answer to an upload: %v", err)
	}
	return res.StatusCode, out
}

// TestUploads sends models to POST /api/uploads and adds them with
// CreateModel, the way the console sends large files.
func TestUploads(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	gguf := fixture(t, "tiny-llama.gguf")

	if status, out := sendUpload(t, srv, "", "", gguf); status != http.StatusUnauthorized || out.Code != "unauthenticated" {
		t.Fatalf("upload signed out: %d %+v", status, out)
	}
	status, out := sendUpload(t, srv, own.jar.cookie, "", gguf)
	if status != http.StatusOK || out.Upload == "" {
		t.Fatalf("upload: %d %+v", status, out)
	}
	res, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Llama", Architecture: "mim", Upload: out.Upload}))
	if err != nil {
		t.Fatal(err)
	}
	lm := res.Msg.GetModel()
	if lm.GetArchitecture() != "language-model" {
		t.Fatalf("a GGUF upload became %s", lm.GetArchitecture())
	}
	g, err := own.predict.Generate(ctx, connect.NewRequest(&mimirv1.GenerateRequest{ModelId: lm.GetId(), Prompt: "Once upon a time", MaxTokens: 3}))
	if err != nil || g.Msg.GetGeneratedTokens() == 0 {
		t.Fatalf("generate from an uploaded model: %v, %v", g, err)
	}
	if _, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Again", Architecture: "mim", Upload: out.Upload})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("an upload used twice: %v", err)
	}

	// A file that already is a Mimir Model file, of a type the server
	// builds itself, goes the same way.
	_, out = sendUpload(t, srv, own.jar.cookie, "", weights(t, "mnist-mlp"))
	res, err = own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Digits", Architecture: "mnist-mlp", Upload: out.Upload}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := own.predict.Predict(ctx, connect.NewRequest(&mimirv1.PredictRequest{ModelId: res.Msg.GetModel().GetId(), Input: &mimirv1.Tensor{Values: make([]float32, 784)}})); err != nil {
		t.Fatalf("predict with an uploaded model: %v", err)
	}

	// Only whoever sent an upload may use it: not an API key of theirs.
	created, err := own.keys.CreateApiKey(ctx, connect.NewRequest(&mimirv1.CreateApiKeyRequest{Name: "uploader", Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_EDITOR, AllModels: true}}))
	if err != nil {
		t.Fatal(err)
	}
	key := created.Msg.GetSecret()
	app := newClients(srv, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+key)
			return next(ctx, req)
		}
	})))
	_, out = sendUpload(t, srv, own.jar.cookie, "", gguf)
	if _, err := app.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Not mine", Architecture: "mim", Upload: out.Upload})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("someone else's upload: %v", err)
	}
	if _, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Both", Architecture: "mim", Upload: out.Upload, Weights: gguf})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("an upload and weights both: %v", err)
	}
	if _, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Mine", Architecture: "mim", Upload: out.Upload})); err != nil {
		t.Fatalf("the sender's own upload, after another caller tried it: %v", err)
	}
	status, out = sendUpload(t, srv, "", key, gguf)
	if status != http.StatusOK {
		t.Fatalf("upload with an API key: %d %+v", status, out)
	}
	if _, err := app.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Key's", Architecture: "mim", Upload: out.Upload})); err != nil {
		t.Fatalf("an API key's own upload: %v", err)
	}

	// A file that isn't a model is refused when it is added, and leaves
	// nothing behind.
	_, out = sendUpload(t, srv, own.jar.cookie, "", []byte("not a model"))
	if _, err := own.models.CreateModel(ctx, connect.NewRequest(&mimirv1.CreateModelRequest{Name: "Junk", Architecture: "mim", Upload: out.Upload})); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("a junk upload: %v", err)
	}

	// Viewers can't add models, so they can't upload them either.
	if _, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{Email: "viewer@example.com", Name: "Vee", Password: password, Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, AllModels: true}})); err != nil {
		t.Fatal(err)
	}
	v := signIn(t, srv, "viewer@example.com")
	if status, out := sendUpload(t, srv, v.jar.cookie, "", gguf); status != http.StatusForbidden || out.Code != "permission_denied" {
		t.Fatalf("viewer upload: %d %+v", status, out)
	}
	if status, out := sendUpload(t, srv, own.jar.cookie, "", nil); status != http.StatusBadRequest {
		t.Fatalf("empty upload: %d %+v", status, out)
	}

	files, _ := os.ReadDir(filepath.Join(svc.DataDir(), "models"))
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".mim") {
			t.Errorf("left beside the models: %s", f.Name())
		}
	}
	if len(files) != 4 {
		t.Errorf("%d model files, want 4", len(files))
	}
}

// TestUploadLimit refuses an upload larger than the server takes, whether
// it says its length or not.
func TestUploadLimit(t *testing.T) {
	srv := startServerWith(t, false, func(s *api.Service) { s.MaxModelBytes = 1 << 10 })
	own := owner(t, srv)
	big := make([]byte, 2<<10)
	if status, out := sendUpload(t, srv, own.jar.cookie, "", big); status != http.StatusRequestEntityTooLarge || out.Code != "resource_exhausted" || !strings.Contains(out.Message, "larger than") {
		t.Fatalf("too big: %d %+v", status, out)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/uploads", struct{ *bytes.Reader }{bytes.NewReader(big)}) // no length
	req.Header.Set("Cookie", own.jar.cookie)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("too big, length unsaid: %d", res.StatusCode)
	}
}
