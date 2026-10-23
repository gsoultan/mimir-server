package gpucloud

import (
	"context"
	"io"
	"log/slog"
	"sync"

	modal "github.com/modal-labs/modal-client/go"
)

// AppName is the Modal app the machines run under, as listed under Apps
// on modal.com.
const AppName = "mimir-worker"

// Image is the container the machines run: NVIDIA's CUDA runtime, which
// carries the libraries mimir's GPU backend loads, and curl to fetch the
// program. Modal builds it once and keeps it.
var Image = struct {
	Base     string
	Commands []string
}{
	Base: "nvidia/cuda:12.6.3-runtime-ubuntu24.04",
	Commands: []string{
		"RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*",
	},
}

// tag marks the machines started for a worker.
const tag = "mimir-worker-id"

// Modal is the Cloud on modal.com.
type Modal struct {
	Log *slog.Logger

	mu     sync.Mutex
	client *modal.Client
	tokens [2]string // the token client was made with
}

// NewModal returns a Cloud on modal.com.
func NewModal(log *slog.Logger) *Modal { return &Modal{Log: log} }

// connect returns a client for c's token, reusing the last one while the
// token is the same.
func (m *Modal) connect(c Config) (*modal.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tokens := [2]string{c.TokenID, c.TokenSecret}
	if m.client != nil && m.tokens == tokens {
		return m.client, nil
	}
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
	// Quiet: the SDK logs at info for every call.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, err := modal.NewClientWithOptions(&modal.ClientParams{TokenID: c.TokenID, TokenSecret: c.TokenSecret, Logger: log})
	if err != nil {
		return nil, err
	}
	m.client, m.tokens = client, tokens
	return client, nil
}

func (m *Modal) Start(ctx context.Context, c Config, mc Machine) (string, error) {
	client, err := m.connect(c)
	if err != nil {
		return "", err
	}
	app, err := client.Apps.FromName(ctx, AppName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		return "", err
	}
	image := client.Images.FromRegistry(Image.Base, nil).DockerfileCommands(Image.Commands, nil)
	secret, err := client.Secrets.FromMap(ctx, mc.Secrets, nil)
	if err != nil {
		return "", err
	}
	sb, err := client.Sandboxes.Create(ctx, app, image, &modal.SandboxCreateParams{
		GPU:       mc.GPU,
		CPU:       mc.CPU,
		MemoryMiB: mc.MemoryMiB,
		Timeout:   mc.Timeout,
		Command:   mc.Command,
		Env:       mc.Env,
		Secrets:   []*modal.Secret{secret},
		Tags:      map[string]string{tag: mc.WorkerID},
	})
	if err != nil {
		return "", err
	}
	return sb.SandboxID, nil
}

func (m *Modal) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	client, err := m.connect(c)
	if err != nil {
		return false, 0, err
	}
	sb, err := client.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return false, 0, err
	}
	code, err := sb.Poll(ctx, nil)
	if err != nil || code == nil {
		return false, 0, err
	}
	return true, *code, nil
}

func (m *Modal) Stop(ctx context.Context, c Config, id string) error {
	client, err := m.connect(c)
	if err != nil {
		return err
	}
	sb, err := client.Sandboxes.FromID(ctx, id, nil)
	if err != nil {
		return err
	}
	_, err = sb.Terminate(ctx, nil)
	return err
}

func (m *Modal) Find(ctx context.Context, c Config, workerID string) (string, error) {
	client, err := m.connect(c)
	if err != nil {
		return "", err
	}
	app, err := client.Apps.FromName(ctx, AppName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		return "", err
	}
	all, err := client.Sandboxes.List(ctx, &modal.SandboxListParams{AppID: app.AppID, Tags: map[string]string{tag: workerID}})
	if err != nil {
		return "", err
	}
	for sb, err := range all {
		if err != nil {
			return "", err
		}
		return sb.SandboxID, nil
	}
	return "", nil
}
