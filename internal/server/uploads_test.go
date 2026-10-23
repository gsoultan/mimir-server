package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
)

// slowModels is a ModelService whose CreateModel waits to be released.
type slowModels struct {
	mimirv1connect.UnimplementedModelServiceHandler
	entered chan struct{}
	release chan struct{}
}

func (s *slowModels) CreateModel(ctx context.Context, _ *connect.Request[mimirv1.CreateModelRequest]) (*connect.Response[mimirv1.CreateModelResponse], error) {
	s.entered <- struct{}{}
	<-s.release
	return connect.NewResponse(&mimirv1.CreateModelResponse{}), nil
}

func (s *slowModels) ListModels(context.Context, *connect.Request[mimirv1.ListModelsRequest]) (*connect.Response[mimirv1.ListModelsResponse], error) {
	return connect.NewResponse(&mimirv1.ListModelsResponse{}), nil
}

// With the cap reached, one more upload is refused with ResourceExhausted
// while other calls go through; once an upload finishes, the next fits.
func TestUploadCap(t *testing.T) {
	svc := &slowModels{entered: make(chan struct{}, 4), release: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(mimirv1connect.NewModelServiceHandler(svc))
	srv := httptest.NewServer(newUploadGate(2, 0, 1<<30).wrap(mux))
	defer srv.Close()

	for _, opts := range [][]connect.ClientOption{nil, {connect.WithGRPC()}} {
		srv := srv
		if len(opts) > 0 { // gRPC needs HTTP/2
			srv = httptest.NewUnstartedServer(newUploadGate(2, 0, 1<<30).wrap(mux))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			defer srv.Close()
		}
		c := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL, opts...)
		upload := func() error {
			_, err := c.CreateModel(context.Background(), connect.NewRequest(&mimirv1.CreateModelRequest{Name: "m", Weights: make([]byte, 1024)}))
			return err
		}
		errs := make(chan error, 2)
		for range 2 {
			go func() { errs <- upload() }()
		}
		for range 2 {
			select {
			case <-svc.entered:
			case <-time.After(10 * time.Second):
				t.Fatal("the first uploads never started")
			}
		}
		if err := upload(); connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("third upload with two running = %v, want ResourceExhausted", err)
		}
		if _, err := c.ListModels(context.Background(), connect.NewRequest(&mimirv1.ListModelsRequest{})); err != nil {
			t.Fatalf("ListModels while uploads are full = %v", err)
		}
		svc.release <- struct{}{}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		// One slot is free again.
		go func() { errs <- upload() }()
		<-svc.entered
		svc.release <- struct{}{}
		svc.release <- struct{}{}
		for range 2 {
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestUploadSlowSenderLosesItsSlot opens connections that announce a large
// upload and never send it, which hold slots before anyone checks who
// they are, and checks that they are cut off in time for an honest
// upload to go through.
func TestUploadSlowSenderLosesItsSlot(t *testing.T) {
	svc := &slowModels{entered: make(chan struct{}, 4), release: make(chan struct{}, 4)}
	mux := http.NewServeMux()
	mux.Handle(mimirv1connect.NewModelServiceHandler(svc))
	gate := newUploadGate(2, 0, 1<<30)
	gate.patience = func(int64) time.Duration { return 300 * time.Millisecond }
	srv := httptest.NewServer(gate.wrap(mux))
	defer srv.Close()

	for range 2 {
		conn, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "POST /mimir.v1.ModelService/CreateModel HTTP/1.1\r\nHost: mimir\r\nContent-Type: application/proto\r\nContent-Length: 50000000\r\n\r\n")
	}
	c := mimirv1connect.NewModelServiceClient(srv.Client(), srv.URL)
	upload := func() error {
		_, err := c.CreateModel(context.Background(), connect.NewRequest(&mimirv1.CreateModelRequest{Name: "m", Weights: make([]byte, 1024)}))
		return err
	}
	// The slots fill as the server reads the headers.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := upload()
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("upload with two stalled senders = %v, want ResourceExhausted", err)
		}
		svc.release <- struct{}{}
		<-svc.entered
		time.Sleep(10 * time.Millisecond)
	}
	// Once their time is up, the stalled senders are gone.
	deadline = time.Now().Add(5 * time.Second)
	for {
		svc.release <- struct{}{}
		err := upload()
		if err == nil {
			<-svc.entered
			break
		}
		<-svc.release
		if time.Now().After(deadline) {
			t.Fatalf("upload after the stalled senders' time = %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
