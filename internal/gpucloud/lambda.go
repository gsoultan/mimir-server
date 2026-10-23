package gpucloud

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Lambda is the Cloud on Lambda Cloud (lambda.ai): an instance with
// Lambda Stack, which carries NVIDIA's driver and CUDA, booted with
// Machine.VMScript.
type Lambda struct{ API restClient }

// NewLambda returns a Cloud on Lambda Cloud.
func NewLambda() *Lambda { return &Lambda{API: restClient{Base: "https://cloud.lambda.ai/api/v1"}} }

type lambdaInstance struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (l *Lambda) Start(ctx context.Context, c Config, m Machine) (string, error) {
	var res struct {
		Data struct {
			InstanceIDs []string `json:"instance_ids"`
		} `json:"data"`
	}
	err := l.API.do(ctx, http.MethodPost, "/instance-operations/launch", c.TokenSecret, map[string]any{
		"region_name":        m.Region,
		"instance_type_name": m.GPU,
		"ssh_key_names":      []string{c.SSHKey},
		"name":               m.Name,
		"user_data":          m.VMScript(),
		"tags":               []map[string]string{{"key": tag, "value": m.WorkerID}},
	}, &res)
	if err != nil {
		return "", err
	}
	if len(res.Data.InstanceIDs) == 0 {
		return "", errors.New("Lambda Cloud gave no instance id")
	}
	return res.Data.InstanceIDs[0], nil
}

func (l *Lambda) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	var res struct {
		Data lambdaInstance `json:"data"`
	}
	err := l.API.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id), c.TokenSecret, nil, &res)
	if errors.Is(err, errNotFound) {
		return true, -1, nil
	}
	if err != nil {
		return false, 0, err
	}
	switch res.Data.Status {
	case "terminated", "terminating", "preempted":
		return true, -1, nil
	}
	return false, 0, nil
}

func (l *Lambda) Stop(ctx context.Context, c Config, id string) error {
	err := l.API.do(ctx, http.MethodPost, "/instance-operations/terminate", c.TokenSecret, map[string]any{"instance_ids": []string{id}}, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (l *Lambda) Find(ctx context.Context, c Config, workerID string) (string, error) {
	var res struct {
		Data []lambdaInstance `json:"data"`
	}
	if err := l.API.do(ctx, http.MethodGet, "/instances", c.TokenSecret, nil, &res); err != nil {
		return "", err
	}
	name := machineName(workerID)
	for _, in := range res.Data {
		if in.Name == name && in.Status != "terminated" && in.Status != "terminating" {
			return in.ID, nil
		}
	}
	return "", nil
}
