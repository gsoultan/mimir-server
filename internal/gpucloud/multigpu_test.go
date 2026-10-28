package gpucloud

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
)

func TestMachinesWithSeveralGPUs(t *testing.T) {
	l := NewLauncher(nil, slog.Default())
	l.ServesProgram = true
	base := Config{Enabled: true, ConsoleURL: "https://mimir.example.com", WorkerID: "0B9E6C3A-1111-4222-8333-944445555666", Token: "mimirw_abc", Key: "k"}
	for _, tc := range []struct {
		provider, gpu, name string
		gpus                int
	}{
		{ProviderModal, "H100:8", "modal-h100x8", 8},
		{ProviderModal, "A10:2", "modal-a10x2", 2}, // Modal takes any count
		{ProviderAWS, "g6.12xlarge", "aws-g6.12xlarge", 4},
		{ProviderGCP, "nvidia-l4:4", "gcp-nvidia-l4x4", 4},
		{ProviderRunPod, "NVIDIA A100 80GB PCIe:4", "runpod-nvidia-a100-80gb-pciex4", 4},
		{ProviderLambda, "gpu_8x_h100_sxm5", "lambda-gpu-8x-h100-sxm5", 8},
		{ProviderVast, "RTX 4090:4", "vast-rtx-4090x4", 4},
		{ProviderVast, "RTX 4090", "vast-rtx-4090", 1},
	} {
		c := base
		c.Provider, c.GPU, c.TokenSecret, c.SSHKey = tc.provider, tc.gpu, "x", "me"
		c = c.WithDefaults()
		c.GPU = tc.gpu
		m, err := l.machineFor(c)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.provider, tc.gpu, err)
		}
		if m.GPUs != tc.gpus || m.CPU != float64(tc.gpus) || m.MemoryMiB != tc.gpus*4<<10 || !strings.Contains(m.Run, "-name '"+tc.name+"'") {
			t.Errorf("%s %s: %d GPUs, %v CPUs, %d MiB, %s", tc.provider, tc.gpu, m.GPUs, m.CPU, m.MemoryMiB, m.Run)
		}
	}

	// Every machine offered with several GPUs passes the check, and says
	// how many in its label.
	for _, p := range Providers {
		for _, g := range p.GPUs {
			if g.GPUs() > 1 && !strings.HasPrefix(g.Label, strconv.Itoa(g.GPUs())+" × ") {
				t.Errorf("%s %s: label %q", p.ID, g.ID, g.Label)
			}
			if _, ok := gcpMachines[g.ID]; p.ID == ProviderGCP && !ok {
				t.Errorf("no GCP machine type for %s", g.ID)
			}
		}
	}
}

func TestProvidersAskForEveryGPU(t *testing.T) {
	ctx := context.Background()
	m := testMachine
	m.GPUs = 4

	srv, calls := fakeAPI(t, map[string]any{"POST /pods": map[string]any{"id": "pod1"}})
	m.GPU = "NVIDIA A100 80GB PCIe:4"
	if _, err := (&RunPod{API: restClient{Base: srv.URL}}).Start(ctx, Config{TokenSecret: "k"}, m); err != nil {
		t.Fatal(err)
	}
	if b := (*calls)[0].Body; b["gpuTypeIds"].([]any)[0] != "NVIDIA A100 80GB PCIe" || b["gpuCount"] != 4.0 {
		t.Errorf("runpod: %+v", b)
	}

	srv, calls = fakeAPI(t, map[string]any{
		"POST /bundles/": map[string]any{"offers": []map[string]any{{"id": 42}}},
		"PUT /asks/42/":  map[string]any{"success": true, "new_contract": 777},
	})
	m.GPU = "RTX 4090:4"
	if _, err := (&Vast{API: restClient{Base: srv.URL}}).Start(ctx, Config{TokenSecret: "k"}, m); err != nil {
		t.Fatal(err)
	}
	if b := (*calls)[0].Body; b["num_gpus"].(map[string]any)["eq"] != 4.0 || b["gpu_name"].(map[string]any)["in"].([]any)[0] != "RTX 4090" {
		t.Errorf("vast: %+v", b)
	}

	m.GPU, m.Region = "nvidia-l4:4", "us-central1-a"
	inst, err := gcpInstance(m)
	if err != nil || inst["machineType"] != "zones/us-central1-a/machineTypes/g2-standard-48" || inst["guestAccelerators"] != nil {
		t.Errorf("gcp: %v %v", inst["machineType"], err)
	}
}
