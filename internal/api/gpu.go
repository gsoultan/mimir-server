package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/auth"
	"github.com/gsoultan/mimir-server/internal/gpucloud"
	"github.com/gsoultan/mimir-server/internal/store"
	"github.com/gsoultan/mimir-server/worker"
)

// KeyCloudGPU is the setting that holds the rented GPU's config. It is
// sealed, like every "cloud." setting.
const KeyCloudGPU = "cloud.modal"

// CloudGPUWorkerName is the name of the worker a rented GPU joins as.
const CloudGPUWorkerName = "Modal GPU"

// LoadCloudGPU reads the rented GPU's config into s.GPU. Call it once at
// start.
func (s *Service) LoadCloudGPU(ctx context.Context) error {
	if s.GPU == nil {
		return nil
	}
	c, err := s.cloudGPU(ctx)
	if err != nil {
		return err
	}
	s.GPU.Configure(c)
	return nil
}

func (s *Service) cloudGPU(ctx context.Context) (gpucloud.Config, error) {
	var c gpucloud.Config
	raw, err := s.store.GetSetting(ctx, KeyCloudGPU)
	if err != nil || raw == "" {
		return c.WithDefaults(), err
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, err
	}
	return c.WithDefaults(), nil
}

func (s *Service) saveCloudGPU(ctx context.Context, c gpucloud.Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, KeyCloudGPU, string(b))
}

// cloudGPUWorker reports whether id is the rented GPU's worker.
func (s *Service) cloudGPUWorker(id string) bool {
	return s.GPU != nil && id != "" && s.GPU.Config().WorkerID == id
}

func (s *Service) cloudGPUProto(c gpucloud.Config) *mimirv1.CloudGpu {
	pc := &mimirv1.CloudGpu{
		Enabled: c.Enabled, TokenId: c.TokenID, HasTokenSecret: c.TokenSecret != "",
		Gpu: c.GPU, ConsoleUrl: c.ConsoleURL, IdleMinutes: int32(c.IdleMinutes), MaxHours: int32(c.MaxHours),
		Capacity: int32(c.Capacity), ProgramUrl: c.ProgramURL, CanServeProgram: canServeProgram(), WorkerId: c.WorkerID,
		State: gpucloud.StateOff,
	}
	if s.GPU != nil {
		st := s.GPU.Status()
		pc.State, pc.Error = st.State, st.Error
		if !st.Since.IsZero() {
			pc.StateTime = timestamppb.New(st.Since)
		}
	}
	return pc
}

func (s *Service) GetCloudGpu(ctx context.Context, _ *connect.Request[mimirv1.GetCloudGpuRequest]) (*connect.Response[mimirv1.GetCloudGpuResponse], error) {
	if s.GPU == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("This server doesn't take workers."))
	}
	return connect.NewResponse(&mimirv1.GetCloudGpuResponse{CloudGpu: s.cloudGPUProto(s.GPU.Config())}), nil
}

func (s *Service) UpdateCloudGpu(ctx context.Context, req *connect.Request[mimirv1.UpdateCloudGpuRequest]) (*connect.Response[mimirv1.UpdateCloudGpuResponse], error) {
	if s.GPU == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("This server doesn't take workers."))
	}
	p, _ := auth.From(ctx)
	s.gpuMu.Lock()
	defer s.gpuMu.Unlock()
	old, err := s.cloudGPU(ctx)
	if err != nil {
		return nil, s.internal(ctx, err)
	}
	m := req.Msg
	c := old
	c.Enabled = m.GetEnabled()
	c.TokenID = strings.TrimSpace(m.GetTokenId())
	if v := strings.TrimSpace(m.GetTokenSecret()); v != "" {
		c.TokenSecret = v
	}
	if c.TokenID == "" {
		c.TokenSecret = "" // the console's own environment then
	}
	c.GPU = strings.TrimSpace(m.GetGpu())
	c.ConsoleURL = strings.TrimRight(strings.TrimSpace(m.GetConsoleUrl()), "/")
	c.IdleMinutes, c.MaxHours, c.Capacity = int(m.GetIdleMinutes()), int(m.GetMaxHours()), int(m.GetCapacity())
	c.ProgramURL = strings.TrimSpace(m.GetProgramUrl())
	c = c.WithDefaults()
	if err := c.Check(); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if c.Enabled && c.ProgramURL == "" && !canServeProgram() {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("This console doesn't run on linux/amd64, so it can't hand the GPU machine its program. Give a download address for mimir-server for linux/amd64."))
	}
	if c.Enabled {
		if c, err = s.ensureCloudGPUWorker(ctx, c, p.Actor()); err != nil {
			return nil, err
		}
	}
	if err := s.saveCloudGPU(ctx, c); err != nil {
		return nil, s.internal(ctx, err)
	}
	s.GPU.Configure(c)
	what := "off"
	if c.Enabled {
		what = "rents a " + c.GPU + " on Modal when training waits; stops after " + strconv.Itoa(c.IdleMinutes) + " idle minutes"
	}
	s.audit(ctx, p.Actor(), "settings.cloud_gpu", "", what)
	if s.KickTraining != nil {
		s.KickTraining()
	}
	return connect.NewResponse(&mimirv1.UpdateCloudGpuResponse{CloudGpu: s.cloudGPUProto(c)}), nil
}

// ensureCloudGPUWorker makes the worker the rented GPU joins as, unless it
// has one already: a token, and a key the console makes and records now,
// so every machine it starts is the same worker.
func (s *Service) ensureCloudGPUWorker(ctx context.Context, c gpucloud.Config, actor string) (gpucloud.Config, error) {
	if c.WorkerID != "" && c.Token != "" && c.Key != "" {
		_, err := s.store.GetWorker(ctx, c.WorkerID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return c, s.internal(ctx, err)
		}
	}
	secret, hash, err := auth.NewToken(WorkerPrefix)
	if err != nil {
		return c, s.internal(ctx, err)
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return c, s.internal(ctx, err)
	}
	w, err := s.store.CreateWorker(ctx, store.Worker{Name: CloudGPUWorkerName, CreatedBy: actor}, hash)
	if err != nil {
		return c, s.internal(ctx, err)
	}
	if _, err := s.store.SetWorkerKey(ctx, w.ID, pub); err != nil {
		return c, s.internal(ctx, err)
	}
	if err := s.store.SetWorkerTasks(ctx, w.ID, "train"); err != nil {
		return c, s.internal(ctx, err)
	}
	s.audit(ctx, actor, "worker.create", w.Name, "for the GPU rented on Modal, with key "+Fingerprint(pub))
	c.WorkerID, c.Token, c.Key = w.ID, s.joinToken(secret), worker.EncodeKey(key)
	return c, nil
}

func (s *Service) StopCloudGpu(ctx context.Context, _ *connect.Request[mimirv1.StopCloudGpuRequest]) (*connect.Response[mimirv1.StopCloudGpuResponse], error) {
	if s.GPU == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("This server doesn't take workers."))
	}
	p, _ := auth.From(ctx)
	if err := s.GPU.Stop(ctx); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("Modal didn't stop the machine: "+err.Error()))
	}
	s.audit(ctx, p.Actor(), "worker.cloud_gpu.stop", CloudGPUWorkerName, "stopped the GPU rented on Modal")
	return connect.NewResponse(&mimirv1.StopCloudGpuResponse{CloudGpu: s.cloudGPUProto(s.GPU.Config())}), nil
}

// forgetCloudGPUWorker turns renting off when its worker is removed, so
// a GPU isn't started for a worker that no longer exists.
func (s *Service) forgetCloudGPUWorker(ctx context.Context, id string) error {
	if !s.cloudGPUWorker(id) {
		return nil
	}
	s.gpuMu.Lock()
	defer s.gpuMu.Unlock()
	c, err := s.cloudGPU(ctx)
	if err != nil {
		return err
	}
	s.GPU.Stop(ctx)
	c.Enabled, c.WorkerID, c.Token, c.Key = false, "", "", ""
	if err := s.saveCloudGPU(ctx, c); err != nil {
		return err
	}
	s.GPU.Configure(c)
	return nil
}

// CloudGPUWanted reports whether a training run waits that the rented
// GPU's worker would take: one placed on it, or one placed nowhere that
// nothing connected can take and this console won't train itself.
func (s *Service) CloudGPUWanted(ctx context.Context, workerID string) (bool, error) {
	jobs, err := s.store.QueuedJobs(ctx, 500)
	if err != nil {
		return false, err
	}
	// What this server's own program trains: every architecture but the
	// language models, as internal/worker.Builtin.
	trainable := map[string]bool{}
	for _, a := range arch.All() {
		trainable[a.Name] = a.Answers() != arch.Written
	}
	for _, j := range jobs {
		if !trainable[j.Architecture] {
			continue // only a custom worker trains it
		}
		if j.RunOn == workerID {
			return true, nil
		}
		if j.RunOn == "" && !s.TrainsHere() && (s.Pool == nil || !s.Pool.CanTrain(j.Architecture, "")) {
			return true, nil
		}
	}
	return false, nil
}

// CanServeProgram reports whether ProgramHandler can hand out this
// console's program for a rented GPU.
func CanServeProgram() bool { return canServeProgram() }

// canServeProgram reports whether this console's own program runs on a
// rented GPU machine, which is linux/amd64.
func canServeProgram() bool { return goruntime.GOOS == "linux" && goruntime.GOARCH == "amd64" }

// ProgramHandler hands this console's own program to a worker that shows
// its token, so a rented GPU runs the console's version.
func (s *Service) ProgramHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		secret, _, _ = strings.Cut(secret, ".")
		if !ok || !strings.HasPrefix(secret, WorkerPrefix) {
			http.Error(w, "Show a worker token: Authorization: Bearer mimirw_...", http.StatusUnauthorized)
			return
		}
		if _, err := s.store.WorkerByToken(r.Context(), auth.HashToken(secret)); err != nil {
			http.Error(w, "That worker token isn't valid.", http.StatusUnauthorized)
			return
		}
		if !canServeProgram() {
			http.Error(w, "This console doesn't run on linux/amd64; download mimir-server for linux/amd64 elsewhere.", http.StatusNotFound)
			return
		}
		path, err := os.Executable()
		if err != nil {
			http.Error(w, "The console can't find its own program.", http.StatusInternalServerError)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "The console can't read its own program.", http.StatusInternalServerError)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.Error(w, "The console can't read its own program.", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, "mimir-server", st.ModTime(), f)
	})
}
