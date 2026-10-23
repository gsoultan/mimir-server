package gpucloud

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// RunPod is the Cloud on runpod.io: a pod running NVIDIA's CUDA image,
// through its REST API.
type RunPod struct{ API restClient }

// NewRunPod returns a Cloud on runpod.io.
func NewRunPod() *RunPod { return &RunPod{API: restClient{Base: "https://rest.runpod.io/v1"}} }

type runpodPod struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	DesiredStatus string `json:"desiredStatus"`
}

func (r *RunPod) Start(ctx context.Context, c Config, m Machine) (string, error) {
	env := map[string]string{}
	for _, kv := range []map[string]string{m.Env, m.Secrets} {
		for k, v := range kv {
			env[k] = v
		}
	}
	var pod runpodPod
	err := r.API.do(ctx, http.MethodPost, "/pods", c.TokenSecret, map[string]any{
		"name":              m.Name,
		"imageName":         containerImage,
		"gpuTypeIds":        []string{m.GPU},
		"gpuCount":          1,
		"cloudType":         "SECURE",
		"containerDiskInGb": 20,
		"volumeInGb":        0,
		"env":               env,
		"dockerStartCmd":    []string{"sh", "-c", m.ContainerScript()},
	}, &pod)
	if err != nil {
		return "", err
	}
	if pod.ID == "" {
		return "", errors.New("RunPod gave no pod id")
	}
	return pod.ID, nil
}

func (r *RunPod) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	var pod runpodPod
	err := r.API.do(ctx, http.MethodGet, "/pods/"+url.PathEscape(id), c.TokenSecret, nil, &pod)
	if errors.Is(err, errNotFound) {
		return true, -1, nil
	}
	if err != nil {
		return false, 0, err
	}
	return pod.DesiredStatus == "EXITED" || pod.DesiredStatus == "TERMINATED", -1, nil
}

func (r *RunPod) Stop(ctx context.Context, c Config, id string) error {
	err := r.API.do(ctx, http.MethodDelete, "/pods/"+url.PathEscape(id), c.TokenSecret, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (r *RunPod) Find(ctx context.Context, c Config, workerID string) (string, error) {
	var pods []runpodPod
	name := machineName(workerID)
	if err := r.API.do(ctx, http.MethodGet, "/pods?name="+url.QueryEscape(name), c.TokenSecret, nil, &pods); err != nil {
		return "", err
	}
	for _, p := range pods {
		if p.Name == name && p.DesiredStatus != "TERMINATED" {
			return p.ID, nil
		}
	}
	return "", nil
}
