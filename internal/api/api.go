// Package api implements the Connect services in proto/mimir/v1.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/convert"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
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
	meter   *meter
	log     *slog.Logger
	setupMu sync.Mutex // serialises Setup so only one first owner is made
	// setupToken must come with Setup; empty means Setup is closed. Set by
	// PrepareSetup, cleared once the first owner exists. Guarded by setupMu.
	setupToken string

	// Notifier sends alerts; nil means none.
	Notifier *alert.Notifier
	// Kick asks the webhook dispatcher to send now; nil means wait for its
	// next tick.
	Kick func()
	// AllowPrivateWebhooks lets webhooks point at local addresses.
	AllowPrivateWebhooks bool
	// Limiter holds back password and code guessing: per address at
	// sign-in, per account where a password or code was already needed.
	Limiter *auth.Limiter
	// Backoff slows password guessing at sign-in per account and address
	// together, so guesses from one place can't lock the owner out.
	Backoff *auth.Backoff
	// Proxies are the reverse proxies whose X-Forwarded-* headers count.
	// The zero value trusts none, so a client can't choose its own address
	// or claim it used HTTPS.
	Proxies auth.Proxies
	// KickTraining asks the training runner to look at the queue now, and
	// CancelTraining stops a running job; either may be nil.
	KickTraining   func()
	CancelTraining func(id string) bool
	// Pool holds connected workers; nil means this server takes none.
	Pool *cluster.Pool
	// ConsolePin is the fingerprint of the console's own TLS key, put in
	// join tokens so workers check they reached this console. Empty when
	// a proxy or a publicly trusted certificate does that job.
	ConsolePin string
	// AllowPlainWorkers lets workers connect without encryption from
	// other machines, for a private network you trust.
	AllowPlainWorkers bool
	// Local says predictions run in this process when no worker is
	// preferred. Off for a server that only coordinates workers.
	Local bool
	// MaxModelBytes is the largest model file an upload may be; 0 means
	// the server's default.
	MaxModelBytes int
	// PredictTimeout bounds one prediction, here or on a worker, on top
	// of the caller's own deadline; 0 means DefaultPredictTimeout.
	PredictTimeout time.Duration
}

// DefaultModelCacheBytes is how many bytes of loaded models the console
// keeps in memory unless SetModelCacheBytes says otherwise.
const DefaultModelCacheBytes = 4 << 30

// SetModelCacheBytes bounds the models this process keeps loaded, by the
// size of their files; the least recently used go first. 0 means no
// bound.
func (s *Service) SetModelCacheBytes(n int64) { s.cache.SetLimit(n) }

// DefaultPredictTimeout is how long a prediction may take, loading the
// model included, when PredictTimeout isn't set.
const DefaultPredictTimeout = time.Minute

var (
	_ mimirv1connect.ModelServiceHandler   = (*Service)(nil)
	_ mimirv1connect.PredictServiceHandler = (*Service)(nil)
)

// New returns a Service backed by s.
func New(s *store.Store, log *slog.Logger) *Service {
	return &Service{store: s, cache: runtime.NewCache(DefaultModelCacheBytes), meter: newMeter(), log: log, Notifier: alert.New(s, log), Limiter: auth.NewLimiter(10, 15*time.Minute), Backoff: auth.NewBackoff(5, time.Second, 15*time.Minute), Local: true}
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
			Output:      string(a.Answers()),
		})
	}
	return connect.NewResponse(res), nil
}

func (s *Service) ListModels(ctx context.Context, _ *connect.Request[mimirv1.ListModelsRequest]) (*connect.Response[mimirv1.ListModelsResponse], error) {
	p, _ := auth.From(ctx)
	ms, err := s.store.List(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
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
	if !a.Fixed() && !a.File {
		return nil, invalid("%s models are taught from a dataset here rather than uploaded. Choose Teach a new model.", a.Title)
	}
	weights := msg.GetWeights()
	if len(weights) == 0 {
		return nil, invalid("The model file is empty.")
	}
	p, _ := auth.From(ctx)
	m := store.Model{Name: name, Description: desc, Architecture: a.Name, TeamID: p.Team()}
	detail := a.Title
	// Load the file once before storing it, so one that doesn't fit is
	// rejected now rather than on the first question.
	if a.File {
		var from convert.Format
		if weights, from, err = convert.ToMim(weights); err != nil {
			s.log.InfoContext(ctx, "model file not converted", "format", from, "error", err)
			if !errors.Is(err, convert.ErrUnsupported) {
				return nil, s.internal(ctx, err)
			}
			return nil, invalid("This %s can't be used here: %s.", fileName(from), strings.TrimPrefix(err.Error(), convert.ErrUnsupported.Error()+": "))
		}
		// The file says itself whether it holds a language model; one
		// from GGUF always does.
		kind := "mim"
		if from == convert.GGUF || runtime.IsLanguageModel(weights) {
			kind = arch.LanguageModel
		}
		a, _ = arch.Lookup(kind)
		m.Architecture, detail = a.Name, a.Title
		if from != convert.Mim {
			detail += ", converted from " + strings.ToUpper(string(from))
		}
		if err := checkFile(&m, a, weights); err != nil {
			s.log.InfoContext(ctx, "model file rejected", "architecture", a.Name, "format", from, "error", err)
			return nil, invalid("This %s can't run here: %s.", fileName(from), strings.TrimPrefix(err.Error(), runtime.ErrBadWeights.Error()+": "))
		}
	} else if _, err := runtime.Build(a, a.Spec, weights); err != nil {
		s.log.InfoContext(ctx, "weights rejected", "architecture", a.Name, "error", err)
		return nil, invalid("This file doesn't fit the %q model type. Check that you picked the kind of model the file was saved from.", a.Title)
	}
	m, err = s.store.Create(ctx, m, weights)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "model.create", m.Name, detail)
	s.emit(ctx, "model.created", modelEvent(m, p))
	s.log.InfoContext(ctx, "model created", "model_id", m.ID, "architecture", m.Architecture, "size_bytes", m.SizeBytes)
	return connect.NewResponse(&mimirv1.CreateModelResponse{Model: toProto(m)}), nil
}

// checkFile loads a whole-model file of architecture a, a Mimir Model file
// of a language model or of a model that reads numbers, and fills in what
// m reads and answers from it.
func checkFile(m *store.Model, a arch.Architecture, weights []byte) error {
	if a.Answers() == arch.Written {
		lm, err := runtime.OpenLanguageModel(weights)
		if err != nil {
			return err
		}
		return lm.Close()
	}
	f, err := runtime.OpenFile(weights)
	if err != nil {
		return err
	}
	m.InputShape, m.Labels = f.Spec.InputShape, f.Spec.Labels
	return f.Close()
}

// fileName names an uploaded model file of format f in an error message.
func fileName(f convert.Format) string {
	switch f {
	case convert.ONNX, convert.GGUF:
		return strings.ToUpper(string(f)) + " model"
	case convert.Safetensors:
		return "safetensors file"
	case convert.Mim:
		return ".mim file"
	}
	return "file"
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
	if a.Answers() == arch.Written {
		return nil, invalid("%s is a language model: send it a prompt to continue with Generate.", m.Name)
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
	input := msg.GetInput().GetValues()
	if len(input) == 0 && (len(msg.GetFields()) > 0 || len(msg.GetPhoto()) > 0) {
		if input, err = encode(m, a, msg.GetFields(), msg.GetPhoto()); err != nil {
			return nil, err
		}
	}
	if want := size(spec.InputShape); len(input) != want {
		return nil, invalid("Send %d numbers (shape %v); got %d.", want, spec.InputShape, len(input))
	}
	if p, ok := auth.From(ctx); ok {
		if err := s.admit(ctx, p); err != nil {
			return nil, err
		}
	}
	load := func() ([]byte, error) { return s.store.Weights(m.ID) }
	timeout := s.PredictTimeout
	if timeout <= 0 {
		timeout = DefaultPredictTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r runtime.Result
	var err2 error
	ran := false
	// A worker takes the prediction when this server doesn't run models
	// itself; otherwise local is faster for the small models here.
	// A model placed on one worker only runs there.
	if s.Pool != nil && (!s.Local || m.WorkerID != "") {
		var worker string
		r, worker, ran, err2 = s.Pool.Predict(pctx, m.ID, m.SHA256, m.WorkerID, a, spec, load, input)
		if ran && err2 != nil {
			s.log.WarnContext(ctx, "predict: worker failed", "worker", worker, "model_id", m.ID, "error", err2)
		}
	}
	if !ran && m.WorkerID != "" {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("This model runs on %s, which is offline or full right now. Wait for it, or change where the model runs on its page.", s.workerName(ctx, m.WorkerID)))
	}
	if !ran {
		if !s.Local {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("No worker is connected that can run this model. Check Settings → Workers."))
		}
		r, err2 = s.cache.Predict(pctx, m.ID, m.SHA256, a, spec, load, input)
	}
	switch {
	case err2 != nil && ctx.Err() != nil:
		return nil, ctx.Err() // the caller gave up; connect maps it to its code
	case err2 != nil && pctx.Err() != nil:
		return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("The prediction took longer than %v and was stopped.", timeout))
	case errors.Is(err2, runtime.ErrBadInput):
		return nil, invalid("%v", err2)
	case errors.Is(err2, cluster.ErrWorkerFailed), errors.Is(err2, cluster.ErrLinkLost):
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("The worker couldn't answer: %v", err2))
	case err2 != nil:
		return nil, s.internal(ctx, err2)
	}
	res := &mimirv1.PredictResponse{
		Output:         &mimirv1.Tensor{Shape: []int64{int64(len(r.Output))}, Values: r.Output},
		Probabilities:  &mimirv1.Tensor{Shape: []int64{int64(len(r.Probability))}, Values: r.Probability},
		DurationMicros: time.Since(start).Microseconds(),
	}
	if a.Answers() == arch.Number {
		res.Number = true
		if len(r.Output) > 0 {
			res.Value = float64(r.Output[0])
		}
		return connect.NewResponse(res), nil
	}
	for _, g := range r.Top(spec.Labels, topK) {
		res.Guesses = append(res.Guesses, &mimirv1.Guess{Label: g.Label, Probability: g.Probability})
	}
	return connect.NewResponse(res), nil
}

// encode turns a question asked by column name, and a photo, into the
// numbers model m reads, the same way its examples were.
func encode(m store.Model, a arch.Architecture, fields map[string]string, photo []byte) ([]float32, error) {
	var out []float32
	if a.Input == arch.Image && a.Fixed() || a.Input == arch.Tensor {
		return nil, invalid("This model reads numbers; send them as input.")
	}
	if a.Input == arch.Image || a.Input == arch.PhotoTable {
		if len(photo) == 0 {
			return nil, invalid("This model looks at a photo. Send one.")
		}
		px, err := schema.DecodePhoto(photo)
		if err != nil {
			return nil, invalid("That photo can't be read. Send a JPG, PNG or GIF.")
		}
		out = schema.Floats(out, px)
	}
	if a.Input == arch.Image {
		return out, nil
	}
	cols := m.Columns
	if len(cols) == 0 {
		// Taught before columns had kinds: every column is a number.
		for _, f := range m.Features {
			cols = append(cols, schema.Column{Name: f, Kind: schema.Number})
		}
	}
	for name := range fields {
		if !slices.ContainsFunc(cols, func(c schema.Column) bool { return c.Name == name }) {
			return nil, invalid("This model has no column called %q.", name)
		}
	}
	for _, c := range cols {
		if v := strings.TrimSpace(fields[c.Name]); c.Kind == schema.Number && v != "" {
			if _, ok := schema.ParseNumber(v); !ok {
				return nil, invalid("%s needs a number; %q isn't one.", c.Name, v)
			}
		}
	}
	return schema.Encode(out, cols, func(i int) string { return fields[cols[i].Name] }), nil
}

// DataDir is the server's data folder.
func (s *Service) DataDir() string { return s.store.Dir() }

// size is how many numbers a shape holds.
func size(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
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
	// A model outside the caller's team or access reads as missing, so
	// the id space doesn't leak.
	if p, _ := auth.From(ctx); m.TeamID != p.Team() || !p.Access.Allows(m.ID) {
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
		Accuracy:     m.Accuracy,
		WorkerId:     m.WorkerID,
		Columns:      columnsProto(m.Columns),
	}
	spec := arch.Spec{InputShape: m.InputShape, Labels: m.Labels}
	if a, ok := arch.Lookup(m.Architecture); ok {
		spec = arch.Resolve(a, spec)
	}
	out.InputShape, out.Labels = int64s(spec.InputShape), spec.Labels
	return out
}

func columnsProto(cols []schema.Column) []*mimirv1.Column {
	out := make([]*mimirv1.Column, len(cols))
	for i, c := range cols {
		out[i] = &mimirv1.Column{Name: c.Name, Kind: string(c.Kind), Values: c.Values, Fill: c.Fill}
	}
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
