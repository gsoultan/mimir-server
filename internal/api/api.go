// Package api implements the Connect services in proto/mimir/v1.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/alert"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// Limits on user-supplied text.
const (
	maxName        = 100
	maxDescription = 500
	maxTopK        = 100
)

// Service implements ModelService and PredictService.
type Service struct {
	store   *store.Store
	cache   *runtime.Cache
	log     *slog.Logger
	setupMu sync.Mutex // serialises Setup so only one first owner is made

	// Notifier sends alerts; nil means none.
	Notifier *alert.Notifier
	// Kick asks the webhook dispatcher to send now; nil means wait for its
	// next tick.
	Kick func()
	// AllowPrivateWebhooks lets webhooks point at local addresses.
	AllowPrivateWebhooks bool
	// Limiter holds back password and code guessing.
	Limiter *auth.Limiter
	// KickTraining asks the training runner to look at the queue now, and
	// CancelTraining stops a running job; either may be nil.
	KickTraining   func()
	CancelTraining func(id string) bool
}

var (
	_ mimirv1connect.ModelServiceHandler   = (*Service)(nil)
	_ mimirv1connect.PredictServiceHandler = (*Service)(nil)
)

// New returns a Service backed by s.
func New(s *store.Store, log *slog.Logger) *Service {
	return &Service{store: s, cache: runtime.NewCache(), log: log, Notifier: alert.New(s, log), Limiter: auth.NewLimiter(10, 15*time.Minute)}
}

func (s *Service) ListArchitectures(context.Context, *connect.Request[mimirv1.ListArchitecturesRequest]) (*connect.Response[mimirv1.ListArchitecturesResponse], error) {
	res := &mimirv1.ListArchitecturesResponse{}
	for _, a := range arch.All() {
		res.Architectures = append(res.Architectures, &mimirv1.Architecture{
			Name:        a.Name,
			Title:       a.Title,
			Description: a.Description,
			InputShape:  int64s(a.Spec.InputShape),
			Labels:      a.Spec.Labels,
			Trainable:   a.Trainable(),
			Input:       string(a.Input),
		})
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ListModels(ctx context.Context, _ *connect.Request[mimirv1.ListModelsRequest]) (*connect.Response[mimirv1.ListModelsResponse], error) {
	ms, err := s.store.List(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	res := &mimirv1.ListModelsResponse{}
	for _, m := range ms {
		if p.Access.Allows(m.ID) {
			res.Models = append(res.Models, toProto(m))
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetModel(ctx context.Context, req *connect.Request[mimirv1.GetModelRequest]) (*connect.Response[mimirv1.GetModelResponse], error) {
	m, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&mimirv1.GetModelResponse{Model: toProto(m)}), nil
}

func (s *Service) CreateModel(ctx context.Context, req *connect.Request[mimirv1.CreateModelRequest]) (*connect.Response[mimirv1.CreateModelResponse], error) {
	msg := req.Msg
	name, desc, err := cleanText(msg.GetName(), msg.GetDescription())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(msg.GetArchitecture())
	if !ok {
		return nil, invalid("Unknown model type %q. Pick one from the list of model types.", msg.GetArchitecture())
	}
	if !a.Fixed() {
		return nil, invalid("%s models are taught from a dataset here rather than uploaded. Choose Teach a new model.", a.Title)
	}
	if len(msg.GetWeights()) == 0 {
		return nil, invalid("The model file is empty.")
	}
	// Load the weights once before storing them, so a file that doesn't fit
	// is rejected now rather than on the first question.
	if _, err := runtime.Build(a, a.Spec, msg.GetWeights()); err != nil {
		s.log.InfoContext(ctx, "weights rejected", "architecture", a.Name, "error", err)
		return nil, invalid("This file doesn't fit the %q model type. Check that you picked the kind of model the file was saved from.", a.Title)
	}
	m, err := s.store.Create(ctx, store.Model{Name: name, Description: desc, Architecture: a.Name}, msg.GetWeights())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "model.create", m.Name, a.Title)
	s.emit(ctx, "model.created", modelEvent(m, p))
	s.log.InfoContext(ctx, "model created", "model_id", m.ID, "architecture", m.Architecture, "size_bytes", m.SizeBytes)
	return connect.NewResponse(&mimirv1.CreateModelResponse{Model: toProto(m)}), nil
}

func (s *Service) UpdateModel(ctx context.Context, req *connect.Request[mimirv1.UpdateModelRequest]) (*connect.Response[mimirv1.UpdateModelResponse], error) {
	id := req.Msg.GetId()
	if _, err := s.get(ctx, id); err != nil {
		return nil, err
	}
	name, desc, err := cleanText(req.Msg.GetName(), req.Msg.GetDescription())
	if err != nil {
		return nil, err
	}
	m, err := s.store.Update(ctx, id, name, desc)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "model.update", m.Name, "")
	s.emit(ctx, "model.updated", modelEvent(m, p))
	return connect.NewResponse(&mimirv1.UpdateModelResponse{Model: toProto(m)}), nil
}

func (s *Service) DeleteModel(ctx context.Context, req *connect.Request[mimirv1.DeleteModelRequest]) (*connect.Response[mimirv1.DeleteModelResponse], error) {
	id := req.Msg.GetId()
	m, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	err = s.store.Delete(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.cache.Forget(id)
	p, _ := auth.From(ctx)
	s.audit(ctx, p.Actor(), "model.delete", m.Name, "")
	s.emit(ctx, "model.deleted", modelEvent(m, p))
	s.log.InfoContext(ctx, "model deleted", "model_id", id)
	return connect.NewResponse(&mimirv1.DeleteModelResponse{}), nil
}

func (s *Service) Predict(ctx context.Context, req *connect.Request[mimirv1.PredictRequest]) (*connect.Response[mimirv1.PredictResponse], error) {
	msg := req.Msg
	m, err := s.get(ctx, msg.GetModelId())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(m.Architecture)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("This model's type (%s) isn't available on this server anymore.", m.Architecture))
	}
	topK := int(msg.GetTopK())
	switch {
	case topK == 0:
		topK = 3
	case topK < 0 || topK > maxTopK:
		return nil, invalid("top_k must be between 1 and %d.", maxTopK)
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	start := time.Now()
	r, err := s.cache.Predict(ctx, m.ID, a, spec, func() ([]byte, error) { return s.store.Weights(m.ID) }, msg.GetInput().GetValues())
	switch {
	case errors.Is(err, runtime.ErrBadInput):
		return nil, invalid("%v", err)
	case err != nil:
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.PredictResponse{
		Output:         &mimirv1.Tensor{Shape: []int64{int64(len(r.Output))}, Values: r.Output},
		Probabilities:  &mimirv1.Tensor{Shape: []int64{int64(len(r.Probability))}, Values: r.Probability},
		DurationMicros: time.Since(start).Microseconds(),
	}
	for _, g := range r.Top(spec.Labels, topK) {
		res.Guesses = append(res.Guesses, &mimirv1.Guess{Label: g.Label, Probability: g.Probability})
	}
	return connect.NewResponse(res), nil
}

func (s *Service) get(ctx context.Context, id string) (store.Model, error) {
	if !store.ValidID(id) {
		return store.Model{}, notFound(id)
	}
	m, err := s.store.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Model{}, notFound(id)
	}
	if err != nil {
		return store.Model{}, s.internal(ctx, err)
	}
	// A model outside the caller's access reads as missing, so the id
	// space doesn't leak.
	if p, _ := auth.From(ctx); !p.Access.Allows(m.ID) {
		return store.Model{}, notFound(id)
	}
	return m, nil
}

// internal logs err with its detail and returns a generic error, so storage
// paths and driver messages never reach the caller.
func (s *Service) internal(ctx context.Context, err error) error {
	s.log.ErrorContext(ctx, "request failed", "error", err)
	return connect.NewError(connect.CodeInternal, errors.New("Something went wrong on the server. Please try again."))
}

func invalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

func notFound(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("No model with id %q.", id))
}

func cleanText(name, desc string) (string, string, error) {
	name, desc = strings.TrimSpace(name), strings.TrimSpace(desc)
	switch {
	case name == "":
		return "", "", invalid("Give the model a name.")
	case !utf8.ValidString(name) || !utf8.ValidString(desc):
		return "", "", invalid("Names and descriptions must be valid text.")
	case utf8.RuneCountInString(name) > maxName:
		return "", "", invalid("Names can be at most %d characters.", maxName)
	case utf8.RuneCountInString(desc) > maxDescription:
		return "", "", invalid("Descriptions can be at most %d characters.", maxDescription)
	}
	return name, desc, nil
}

func toProto(m store.Model) *mimirv1.Model {
	out := &mimirv1.Model{
		Id:           m.ID,
		Name:         m.Name,
		Description:  m.Description,
		Architecture: m.Architecture,
		Status:       mimirv1.ModelStatus_MODEL_STATUS_READY,
		SizeBytes:    m.SizeBytes,
		CreateTime:   timestamppb.New(m.Created),
		UpdateTime:   timestamppb.New(m.Updated),
		Features:     m.Features,
		JobId:        m.JobID,
	}
	spec := arch.Spec{InputShape: m.InputShape, Labels: m.Labels}
	if a, ok := arch.Lookup(m.Architecture); ok {
		spec = arch.Resolve(a, spec)
	}
	out.InputShape, out.Labels = int64s(spec.InputShape), spec.Labels
	return out
}

func int64s(xs []int) []int64 {
	out := make([]int64, len(xs))
	for i, x := range xs {
		out[i] = int64(x)
	}
	return out
}

// modelEvent is the data part of a model.* webhook payload.
func modelEvent(m store.Model, p auth.Principal) map[string]any {
	return map[string]any{
		"model": map[string]any{"id": m.ID, "name": m.Name, "architecture": m.Architecture, "size_bytes": m.SizeBytes},
		"actor": p.Actor(),
	}
}
