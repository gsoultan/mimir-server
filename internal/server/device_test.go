package server_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
)

// TestOnDevice lets a model run in browsers and downloads it, and keeps
// the ones that can't, or weren't allowed to, on the server.
func TestOnDevice(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t)
	own := owner(t, srv)
	m := createModel(t, own, "Digits", "mnist-mlp")
	if m.GetOnDevice() || m.GetVersion() == "" {
		t.Fatalf("new model: on_device %v, version %q", m.GetOnDevice(), m.GetVersion())
	}
	download := func(c clients, id string) (*mimirv1.GetDeviceModelResponse, error) {
		res, err := c.models.GetDeviceModel(ctx, connect.NewRequest(&mimirv1.GetDeviceModelRequest{Id: id}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if _, err := download(own, m.GetId()); code(err) != connect.CodePermissionDenied {
		t.Fatalf("download before allowed: %v", err)
	}
	set := func(c clients, id string, on bool) error {
		_, err := c.models.UpdateModel(ctx, connect.NewRequest(&mimirv1.UpdateModelRequest{Id: id, OnDevice: proto.Bool(on)}))
		return err
	}
	if err := set(own, m.GetId(), true); err != nil {
		t.Fatal(err)
	}

	// A viewer who may use the model may run it on their device, but not
	// change whether it may.
	if _, err := own.users.CreateUser(ctx, connect.NewRequest(&mimirv1.CreateUserRequest{
		Email: "viewer@example.com", Name: "Vee", Password: password,
		Access: &mimirv1.Access{Role: mimirv1.Role_ROLE_VIEWER, ModelIds: []string{m.GetId()}},
	})); err != nil {
		t.Fatal(err)
	}
	v := signIn(t, srv, "viewer@example.com")
	got, err := download(v, m.GetId())
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetModel().GetOnDevice() || fmt.Sprintf("%x", sha256.Sum256(got.GetWeights())) != m.GetVersion() {
		t.Fatalf("downloaded %d bytes, on_device %v", len(got.GetWeights()), got.GetModel().GetOnDevice())
	}
	if err := set(v, m.GetId(), false); code(err) != connect.CodePermissionDenied {
		t.Fatalf("viewer turned it off: %v", err)
	}
	other := createModel(t, own, "Other", "mnist-mlp")
	if err := set(own, other.GetId(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := download(v, other.GetId()); code(err) != connect.CodeNotFound {
		t.Fatalf("viewer downloaded a model they can't reach: %v", err)
	}

	if err := set(own, m.GetId(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := download(v, m.GetId()); code(err) != connect.CodePermissionDenied {
		t.Fatalf("download after turned off: %v", err)
	}

	// Language models stay on the server.
	lm := languageModel(t, own, "Tiny Llama")
	if err := set(own, lm.GetId(), true); code(err) != connect.CodeFailedPrecondition {
		t.Fatalf("language model on devices: %v", err)
	}
}
