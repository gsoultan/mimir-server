package api

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/store"
)

var _ mimirv1connect.WorkerServiceHandler = (*Service)(nil)

// WorkerPrefix starts every worker token.
const WorkerPrefix = "mimirw_"

func (s *Service) ListWorkers(ctx context.Context, _ *connect.Request[mimirv1.ListWorkersRequest]) (*connect.Response[mimirv1.ListWorkersResponse], error) {
	ws, err := s.store.ListWorkers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	models, waiting, err := s.store.Placement(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	live := s.statuses()
	res := &mimirv1.ListWorkersResponse{SecureJoin: s.ConsolePin != ""}
	for _, w := range ws {
		res.Workers = append(res.Workers, s.workerView(w, live[w.ID], models, waiting))
	}
	return connect.NewResponse(res), nil
}

func (s *Service) CreateWorker(ctx context.Context, req *connect.Request[mimirv1.CreateWorkerRequest]) (*connect.Response[mimirv1.CreateWorkerResponse], error) {
	p, _ := auth.From(ctx)
	name, err := cleanName("worker", req.Msg.GetName())
	if err != nil {
		return nil, err
	}
	token, hash, err := auth.NewToken(WorkerPrefix)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	w, err := s.store.CreateWorker(ctx, store.Worker{Name: name, CreatedBy: p.Actor()}, hash)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "worker.create", w.Name, "")
	return connect.NewResponse(&mimirv1.CreateWorkerResponse{Worker: workerProto(w), Token: s.joinToken(token)}), nil
}

func (s *Service) DeleteWorker(ctx context.Context, req *connect.Request[mimirv1.DeleteWorkerRequest]) (*connect.Response[mimirv1.DeleteWorkerResponse], error) {
	p, _ := auth.From(ctx)
	id := req.Msg.GetId()
	if !store.ValidID(id) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such worker."))
	}
	ws, err := s.store.ListWorkers(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	i := slices.IndexFunc(ws, func(w store.Worker) bool { return w.ID == id })
	if i < 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("No such worker."))
	}
	if err := s.store.DeleteWorker(ctx, id); err != nil {
		return nil, s.internal(ctx, err)
	}
	if s.Pool != nil {
		s.Pool.Drop(id)
	}
	s.audit(ctx, p.Actor(), "worker.delete", ws[i].Name, "")
	return connect.NewResponse(&mimirv1.DeleteWorkerResponse{}), nil
}

// Link is the stream a worker holds open. It authenticates with the
// worker token rather than a person's credentials, so it does its own
// checking instead of going through the interceptor.
func (s *Service) Link(ctx context.Context, stream *connect.BidiStream[mimirv1.LinkRequest, mimirv1.LinkResponse]) error {
	secret, ok := strings.CutPrefix(stream.RequestHeader().Get("Authorization"), "Bearer ")
	// A pasted join token still carries the console's fingerprint; only
	// the part before it is the secret.
	secret, _, _ = strings.Cut(secret, ".")
	if !ok || !strings.HasPrefix(secret, WorkerPrefix) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("Connect with a worker token: Authorization: Bearer mimirw_..."))
	}
	hash := auth.HashToken(secret)
	w, err := s.store.WorkerByToken(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("That worker token isn't valid. It may have been deleted."))
	}
	if err != nil {
		return s.internal(ctx, err)
	}
	if s.Pool == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("This server doesn't take workers."))
	}
	addr := stream.Peer().Addr
	security, version := linkSecurity(ctx, addr)
	if security == "plain" && !s.AllowPlainWorkers {
		s.auditIn(ctx, "", "worker "+w.Name, "worker.refused", w.Name, "tried to connect without encryption from "+hostOf(addr))
		return connect.NewError(connect.CodePermissionDenied, errors.New("Workers must connect over https. Serve the console with TLS, or put it behind an https proxy."))
	}
	go func() { // keep last_seen fresh while linked
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.store.TouchWorker(context.WithoutCancel(ctx), w.ID)
				return
			case <-t.C:
				s.store.TouchWorker(ctx, w.ID)
			}
		}
	}()
	return s.Pool.Serve(ctx, stream, cluster.Join{Worker: w, Addr: addr, Security: security, TLSVersion: version, Verify: s.verifyKey(w, hash, addr)})
}

func workerProto(w store.Worker) *mimirv1.Worker {
	pw := &mimirv1.Worker{Id: w.ID, Name: w.Name, CreatedBy: w.CreatedBy, CreateTime: timestamppb.New(w.Created)}
	if !w.LastSeen.IsZero() {
		pw.LastSeen = timestamppb.New(w.LastSeen)
	}
	return pw
}
