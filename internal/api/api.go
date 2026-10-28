// Package api implements the Connect services in proto/mimir/v1.
package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/gen/mimir/v1/mimirv1connect"
	"github.com/gsoultan/mimir-server/internal/alert"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/cluster"
	"github.com/gsoultan/mimir-server/internal/convert"
	"github.com/gsoultan/mimir-server/internal/gpucloud"
	"github.com/gsoultan/mimir-server/internal/mapfile"
	"github.com/gsoultan/mimir-server/internal/memguard"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/sso"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir/tensor"
)

// maxTopK is the most guesses a prediction returns.
const maxTopK = 100

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
	// uploads holds files sent to POST /api/uploads until CreateModel
	// uses them.
	uploads uploads

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
	// SignIn talks to outside sign-in providers (Google, GitHub, OpenID
	// Connect).
	Providers *sso.Client
	// KickTraining asks the training runner to look at the queue now, and
	// CancelTraining stops a running job; either may be nil.
	KickTraining   func()
	CancelTraining func(id string) bool
	// Pool holds connected workers; nil means this server takes none.
	Pool *cluster.Pool
	// GPU rents a GPU for training runs that wait; nil means
	// this server doesn't.
	GPU   *gpucloud.Launcher
	gpuMu sync.Mutex // serialises changes to the rented GPU's setting
	// ConsolePin is the fingerprint of the console's own TLS key, put in
	// join tokens so workers check they reached this console. Empty when
	// a proxy or a publicly trusted certificate does that job.
	ConsolePin string
	// AllowPlainWorkers lets workers connect without encryption from
	// other machines, for a private network you trust.
	AllowPlainWorkers bool
	// TrainHereDefault is whether a standalone console trains runs itself
	// until Settings says otherwise; LoadTrainingSetting reads the stored
	// choice over it.
	TrainHereDefault bool
	trainHere        atomic.Bool
	groupWorkers     atomic.Int32
	// Local says predictions run in this process when no worker is
	// preferred. Off for a server that only coordinates workers.
	Local bool
	// MaxModelBytes is the largest model file an upload may be; 0 means
	// the server's default.
	MaxModelBytes int
	// MaxUploads is how many model and dataset uploads the server reads
	// and handles at once; more are refused with ResourceExhausted. 0
	// means the server's default.
	MaxUploads int
	// PredictTimeout bounds one prediction, here or on a worker, on top
	// of the caller's own deadline; 0 means DefaultPredictTimeout.
	PredictTimeout time.Duration

	memory      *memguard.Guard
	memoryAlert atomic.Int64 // unix seconds of the last low-memory alert
	// answers holds predictions until FlushAnswers keeps them for feedback.
	answers answerLog
}

// memoryAlertEvery is the least time between two low-memory alerts.
const memoryAlertEvery = time.Hour

// WatchMemory keeps the console within g's budget: as memory runs short
// the least recently used models are let go (half of them when it is
// high, all but the last used when critical), and while it is critical no
// model is loaded, so the ones already loaded keep answering. Owners are
// alerted when it turns critical, at most once an hour.
func (s *Service) WatchMemory(g *memguard.Guard) {
	s.memory = g
	if g == nil {
		return
	}
	s.cache.SetGate(func() error { return g.Check(memguard.Critical) })
	g.Watch(func(l memguard.Level) {
		used, _ := s.cache.Used()
		switch l {
		case memguard.High:
			if freed := s.cache.Shed(used / 2); freed > 0 {
				s.log.Warn("memory: let go of models", "bytes", freed, "level", l.String())
			}
		case memguard.Critical:
			if freed := s.cache.Shed(0); freed > 0 {
				s.log.Warn("memory: let go of models", "bytes", freed, "level", l.String())
			}
			s.alertLowMemory(g)
		}
	})
}

// Memory returns the guard WatchMemory was given; nil means none.
func (s *Service) Memory() *memguard.Guard { return s.memory }

// alertLowMemory tells the owners memory ran short, unless it did so
// within memoryAlertEvery. It doesn't wait for the alert to go out.
func (s *Service) alertLowMemory(g *memguard.Guard) {
	now := time.Now().Unix()
	last := s.memoryAlert.Load()
	if s.Notifier == nil || now-last < int64(memoryAlertEvery/time.Second) || !s.memoryAlert.CompareAndSwap(last, now) {
		return
	}
	body := fmt.Sprintf("The console is using %s of the %s it may use. To stay up it has let go of loaded models, paused training here and is turning away new model loads and uploads until memory frees up.\n\nIf this keeps happening, give the console more memory (-memory-limit), lower -model-cache-mb, or move training to a worker.", memguard.Format(g.Used()), memguard.Format(g.Limit()))
	go s.Notifier.Notify(context.Background(), "mimir is running low on memory", body)
}

// lowMemory is the answer to a request turned away to keep the server
// within its memory budget.
func lowMemory() error {
	return connect.NewError(connect.CodeUnavailable, errors.New("The server is low on memory right now and isn't loading more models. Models already loaded keep answering; try this one again in a minute."))
}

// DefaultModelCacheBytes is how many bytes of loaded models the console
// keeps in memory unless SetModelCacheBytes says otherwise.
const DefaultModelCacheBytes = 4 << 30

// SetModelCacheBytes bounds the models this process keeps loaded, by the
// size of their files; the least recently used go first. 0 means no
// bound.
func (s *Service) SetModelCacheBytes(n int64) { s.cache.SetLimit(n) }

// ObserveModels has o told about the predictions the console runs itself
// and the models it loads, for metrics.
func (s *Service) ObserveModels(o runtime.Observer) { s.cache.SetObserver(o) }

// ModelCacheUsage returns the bytes of models the console holds loaded,
// by file size, and how many.
func (s *Service) ModelCacheUsage() (bytes int64, models int) { return s.cache.Used() }

// DefaultPredictTimeout is how long a prediction may take, loading the
// model included, when PredictTimeout isn't set.
const DefaultPredictTimeout = time.Minute

var (
	_ mimirv1connect.ModelServiceHandler   = (*Service)(nil)
	_ mimirv1connect.PredictServiceHandler = (*Service)(nil)
)

// New returns a Service backed by s.
func New(s *store.Store, log *slog.Logger) *Service {
	svc := &Service{store: s, cache: runtime.NewCache(DefaultModelCacheBytes), meter: newMeter(), log: log, Notifier: alert.New(s, log), Limiter: auth.NewLimiter(10, 15*time.Minute), Backoff: auth.NewBackoff(5, time.Second, 15*time.Minute), Local: true, TrainHereDefault: true, Providers: sso.New()}
	svc.trainHere.Store(true)
	return svc
}

func (s *Service) ListArchitectures(context.Context, *connect.Request[mimirv1.ListArchitecturesRequest]) (*connect.Response[mimirv1.ListArchitecturesResponse], error) {
	res := &mimirv1.ListArchitecturesResponse{}
	for _, a := range arch.Every() {
		res.Architectures = append(res.Architectures, archProto(a))
	}
	return connect.NewResponse(res), nil
}

func archProto(a arch.Architecture) *mimirv1.Architecture {
	return &mimirv1.Architecture{
		Name:        a.Name,
		Title:       a.Title,
		Description: a.Description,
		InputShape:  int64s(a.Spec.InputShape),
		Labels:      a.Spec.Labels,
		Trainable:   a.Trainable(),
		Input:       string(a.Input),
		Output:      string(a.Answers()),
		Custom:      a.Custom,
		Items:       int32(a.Items),
		Designable:  a.Designable,
		Learned:     a.Learner != nil,
		Target:      string(a.Target),
	}
}

func (s *Service) ListModels(ctx context.Context, _ *connect.Request[mimirv1.ListModelsRequest]) (*connect.Response[mimirv1.ListModelsResponse], error) {
	p, _ := auth.From(ctx)
	ms, err := s.store.List(ctx, p.Team())
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	res := &mimirv1.ListModelsResponse{}
	names := map[string]string{}
	for _, m := range ms {
		names[m.ID] = m.Name
	}
	for _, m := range ms {
		if p.Access.Allows(m.ID) {
			out := toProto(m)
			out.BaseModelName = names[m.BaseModelID]
			res.Models = append(res.Models, out)
		}
	}
	return connect.NewResponse(res), nil
}

func (s *Service) GetModel(ctx context.Context, req *connect.Request[mimirv1.GetModelRequest]) (*connect.Response[mimirv1.GetModelResponse], error) {
	m, err := s.get(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	out := toProto(m)
	if m.FineTune() {
		if base, err := s.store.Get(ctx, m.BaseModelID); err == nil {
			out.BaseModelName = base.Name
		}
	}
	return connect.NewResponse(&mimirv1.GetModelResponse{Model: out}), nil
}

func (s *Service) CreateModel(ctx context.Context, req *connect.Request[mimirv1.CreateModelRequest]) (*connect.Response[mimirv1.CreateModelResponse], error) {
	msg := req.Msg
	name, desc, err := cleanText("model", msg.GetName(), msg.GetDescription())
	if err != nil {
		return nil, err
	}
	a, ok := arch.Lookup(msg.GetArchitecture())
	if !ok {
		return nil, invalid("Unknown model type %q. Pick one from the list of model types.", msg.GetArchitecture())
	}
	if !a.Fixed() && !a.File && !a.Custom {
		return nil, invalid("%s models are taught from a dataset here rather than uploaded. Choose Teach a new model.", a.Title)
	}
	p, _ := auth.From(ctx)
	// The model is a file on disk when it came through POST /api/uploads,
	// and bytes in the request otherwise.
	weights, path := msg.GetWeights(), ""
	var sum []byte // the SHA-256 of the file at path, when known
	switch {
	case msg.GetUpload() != "" && len(weights) > 0:
		return nil, invalid("Send the model file or an upload, not both.")
	case msg.GetUpload() != "":
		var ok bool
		if path, ok = s.uploads.take(msg.GetUpload(), uploader(p)); !ok {
			return nil, invalid("That upload isn't here any more: an upload can be used once, within an hour of sending it. Send the file again.")
		}
		defer os.Remove(path) // no-op once stored
	case len(weights) == 0:
		return nil, invalid("The model file is empty.")
	}
	m := store.Model{Name: name, Description: desc, Architecture: a.Name, TeamID: p.Team()}
	detail := a.Title
	// Load the file once before storing it, so one that doesn't fit is
	// rejected now rather than on the first question.
	if a.File {
		// Whole models go through disk however they came, so converting
		// one takes no more memory than reading a small part of it.
		if path == "" {
			if path, err = s.spill(weights); err != nil {
				return nil, s.internal(ctx, err)
			}
			defer os.Remove(path)
			weights = nil
		}
		var from convert.Format
		if path, from, sum, err = s.toMim(path); path != "" && sum != nil {
			defer os.Remove(path)
		}
		if err != nil {
			s.log.InfoContext(ctx, "model file not converted", "format", from, "error", err)
			if !errors.Is(err, convert.ErrUnsupported) {
				return nil, s.internal(ctx, err)
			}
			return nil, invalid("This %s can't be used here: %s.", fileName(from), strings.TrimPrefix(err.Error(), convert.ErrUnsupported.Error()+": "))
		}
		mf, err := mapfile.Open(path)
		if err != nil {
			return nil, s.internal(ctx, err)
		}
		// The file says itself whether it holds a language model; one
		// from GGUF always does.
		kind := "mim"
		if from == convert.GGUF || runtime.IsLanguageModel(mf.Bytes()) {
			kind = arch.LanguageModel
		}
		a, _ = arch.Lookup(kind)
		m.Architecture, detail = a.Name, a.Title
		if from != convert.Mim {
			detail += ", converted from " + strings.ToUpper(string(from))
		}
		err = checkFile(&m, a, mf.Bytes())
		mf.Close()
		if err != nil {
			s.log.InfoContext(ctx, "model file rejected", "architecture", a.Name, "format", from, "error", err)
			return nil, invalid("This %s can't run here: %s.", fileName(from), strings.TrimPrefix(err.Error(), runtime.ErrBadWeights.Error()+": "))
		}
	} else if a.Custom {
		// Only its own code can read the weights; keep them as they are
		// with what the uploader says the model reads and answers.
		if err := customSpec(&m, a, msg.GetInputShape(), msg.GetLabels()); err != nil {
			return nil, err
		}
	} else if err := buildFrom(a, weights, path); err != nil {
		s.log.InfoContext(ctx, "weights rejected", "architecture", a.Name, "error", err)
		return nil, invalid("This file doesn't fit the %q model type. Check that you picked the kind of model the file was saved from.", a.Title)
	}
	if path != "" {
		m, err = s.store.CreateFromFile(ctx, m, path, sum)
	} else {
		m, err = s.store.Create(ctx, m, weights)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.audit(ctx, p.Actor(), "model.create", m.Name, detail)
	s.emit(ctx, "model.created", modelEvent(m, p))
	s.WarmModel(m.ID)
	s.log.InfoContext(ctx, "model created", "model_id", m.ID, "architecture", m.Architecture, "size_bytes", m.SizeBytes)
	return connect.NewResponse(&mimirv1.CreateModelResponse{Model: toProto(m)}), nil
}

// spill writes weights sent inside a request to a file beside the stored
// models and returns its path.
func (s *Service) spill(weights []byte) (string, error) {
	f, err := s.store.TempFile()
	if err != nil {
		return "", err
	}
	_, err = f.Write(weights)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// toMim returns the path of the model file at path as a Mimir Model file:
// path itself when it is one, or a new file it converted it into, which
// the caller removes, with that file's SHA-256, worked out as it was
// written. It also returns the format path was in.
func (s *Service) toMim(path string) (string, convert.Format, []byte, error) {
	out, err := s.store.TempFile()
	if err != nil {
		return "", "", nil, err
	}
	h := sha256.New()
	w := bufio.NewWriterSize(io.MultiWriter(out, h), uploadBuffer)
	from, err := convert.File(path, w)
	if err == nil {
		err = w.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil || from == convert.Mim {
		os.Remove(out.Name())
		if err != nil {
			return "", from, nil, err
		}
		return path, from, nil, nil
	}
	return out.Name(), from, h.Sum(nil), nil
}

// buildFrom checks that weights, or the file at path when weights is
// empty, fit architecture a.
func buildFrom(a arch.Architecture, weights []byte, path string) error {
	if path != "" {
		mf, err := mapfile.Open(path)
		if err != nil {
			return err
		}
		defer mf.Close()
		weights = mf.Bytes()
	}
	_, err := runtime.Build(a, a.Spec, weights)
	return err
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
	old, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	// A request that only says where the model runs keeps its name.
	onlyWhere := req.Msg.OnDevice != nil && req.Msg.GetName() == "" && req.Msg.GetDescription() == "" && req.Msg.GetLanguage() == nil
	name, desc := old.Name, old.Description
	if !onlyWhere {
		if name, desc, err = cleanText("model", req.Msg.GetName(), req.Msg.GetDescription()); err != nil {
			return nil, err
		}
	}
	var lang *store.Language
	if l := req.Msg.GetLanguage(); l != nil {
		if !isLanguage(old) {
			return nil, invalid("Only a language model has answer settings.")
		}
		v, err := checkLanguage(l)
		if err != nil {
			return nil, err
		}
		lang = &v
	}
	if req.Msg.OnDevice != nil && req.Msg.GetOnDevice() && !old.OnDevice {
		if why := deviceRefusal(old); why != "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
		}
	}
	if req.Msg.OnDevice != nil && req.Msg.GetOnDevice() != old.OnDevice {
		if err := s.store.SetOnDevice(ctx, id, req.Msg.GetOnDevice()); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, notFound(id)
			}
			return nil, s.internal(ctx, err)
		}
		p, _ := auth.From(ctx)
		s.audit(ctx, p.Actor(), "model.on_device", old.Name, map[bool]string{true: "may run on devices", false: "runs on the server only"}[req.Msg.GetOnDevice()])
	}
	if lang != nil {
		if err := s.store.SetLanguage(ctx, id, *lang); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, notFound(id)
			}
			return nil, s.internal(ctx, err)
		}
	}
	if onlyWhere {
		m, err := s.get(ctx, id)
		if err != nil {
			return nil, err
		}
		return connect.NewResponse(&mimirv1.UpdateModelResponse{Model: toProto(m)}), nil
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
	// A fine-tune runs on its base model, so the base stays while it has
	// any.
	tunes, err := s.store.FineTunes(ctx, id)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	if len(tunes) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s has fine-tunes that run on it, such as %s. Delete them first.", m.Name, tunes[0].Name))
	}
	err = s.store.Delete(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound(id)
	}
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	s.cache.Forget(id)
	if s.Pool != nil {
		s.Pool.Forget(id, "")
	}
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
	case topK == 0 && a.Answers() == arch.Ranked && a.Items > 0:
		topK = min(a.Items, maxTopK)
	case topK == 0:
		topK = 3
	case topK < 0 || topK > maxTopK:
		return nil, invalid("top_k must be between 1 and %d.", maxTopK)
	}
	spec := arch.Resolve(a, arch.Spec{InputShape: m.InputShape, Labels: m.Labels})
	start := time.Now()
	input := msg.GetInput().GetValues()
	// A model with columns reads a question with none filled in as every
	// column empty, as a forecast of a single series is asked.
	if len(input) == 0 && (len(msg.GetFields()) > 0 || len(m.Columns) > 0 || len(msg.GetPhoto()) > 0 || len(msg.GetSound()) > 0) {
		if input, err = encode(m, a, msg.GetFields(), msg.GetPhoto(), msg.GetSound()); err != nil {
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
	pin, pinned := m.PinnedWorker()
	// A custom model's own code runs only on workers.
	if s.Pool != nil && (!s.Local || pinned || a.Custom) {
		var worker string
		r, worker, ran, err2 = s.Pool.Predict(pctx, m.ID, m.WeightsVersion(), pin, a, spec, load, input)
		if ran && err2 != nil {
			s.log.WarnContext(ctx, "predict: worker failed", "worker", worker, "model_id", m.ID, "error", err2)
		}
	}
	if !ran && pinned {
		if s.trainingOnly(pin) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("This model is set to run on %s, which only teaches models. Change where the model runs on its page.", s.workerName(ctx, pin)))
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("This model runs on %s, which is offline or full right now. Wait for it, or change where the model runs on its page.", s.workerName(ctx, pin)))
	}
	if !ran {
		if a.Custom {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("No worker that runs %s is connected right now. Start one with its code, or check Settings → Workers.", a.Name))
		}
		if !s.Local {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("No worker is connected that can run this model. Check Settings → Workers."))
		}
		r, err2 = s.cache.Predict(pctx, m.ID, m.WeightsVersion(), a, spec, load, input)
	}
	switch {
	case err2 != nil && ctx.Err() != nil:
		return nil, ctx.Err() // the caller gave up; connect maps it to its code
	case err2 != nil && pctx.Err() != nil:
		return nil, connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("The prediction took longer than %v and was stopped.", timeout))
	case errors.Is(err2, runtime.ErrBadInput):
		return nil, invalid("%v", err2)
	case tooBig(err2):
		return nil, tooBigError(err2)
	case errors.Is(err2, memguard.ErrLowMemory):
		return nil, lowMemory()
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
	kept := store.Answer{ModelID: m.ID, VersionID: m.VersionID, Time: time.Now(), Fields: keptFields(msg.GetFields())}
	if a.Answers() == arch.Number {
		res.Number = true
		if len(r.Output) > 0 {
			res.Value = float64(r.Output[0])
		}
		kept.Number, kept.Value = true, res.Value
		res.PredictionId = s.keepAnswer(kept)
		return connect.NewResponse(res), nil
	}
	if a.Answers() == arch.Boxes {
		res.Probabilities = nil
		for _, f := range arch.FoundIn(r.Output, spec.Labels) {
			res.Boxes = append(res.Boxes, &mimirv1.Box{Label: f.Label, Score: f.Score, Left: f.Left, Top: f.Top, Width: f.Width, Height: f.Height})
		}
		return connect.NewResponse(res), nil
	}
	for _, g := range r.Top(spec.Labels, topK) {
		res.Guesses = append(res.Guesses, &mimirv1.Guess{Label: g.Label, Probability: g.Probability})
	}
	if len(res.Guesses) > 0 {
		kept.Label, kept.Confidence = res.Guesses[0].Label, float64(res.Guesses[0].Probability)
		res.PredictionId = s.keepAnswer(kept)
	}
	return connect.NewResponse(res), nil
}

// encode turns a question asked by column name, and a photo or a sound,
// into the numbers model m reads, the same way its examples were.
func encode(m store.Model, a arch.Architecture, fields map[string]string, photo, sound []byte) ([]float32, error) {
	in, err := ask.Encode(a, m.Columns, m.Features, fields, photo, sound)
	if err != nil {
		return nil, invalid("%s", ask.Reason(err))
	}
	return in, nil
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
// tooBig reports whether a run failed because the model needs more memory
// than mimir may take, such as for a file that declares a value larger
// than the bound on one.
func tooBig(err error) bool {
	return errors.Is(err, tensor.ErrMemoryBudget) || errors.Is(err, tensor.ErrOutOfMemory)
}

func tooBigError(err error) error {
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("This model needs more memory than the server may give it: %v", err))
}

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

// cleanText checks the name of a thing, such as "model", and a
// description by the store's rules.
func cleanText(thing, name, desc string) (string, string, error) {
	n, err := store.ParseName(thing, name)
	if err != nil {
		return "", "", refused(err)
	}
	d, err := store.ParseDescription(desc)
	if err != nil {
		return "", "", refused(err)
	}
	return n.String(), d.String(), nil
}

// cleanName checks the name of a thing by the store's rules.
func cleanName(thing, name string) (string, error) {
	n, err := store.ParseName(thing, name)
	if err != nil {
		return "", refused(err)
	}
	return n.String(), nil
}

// refused turns a value the store's rules refuse into InvalidArgument,
// keeping its message for the person.
func refused(err error) error {
	return connect.NewError(connect.CodeInvalidArgument, err)
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
		Language:     languageProto(m),
		OnDevice:     m.OnDevice,
		Version:      m.WeightsVersion(),
		VersionId:    m.VersionID,
		BaseModelId:  m.BaseModelID,
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
