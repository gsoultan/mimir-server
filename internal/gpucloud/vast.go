package gpucloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Vast is the Cloud on Vast.ai: the cheapest verified offer of the GPU,
// running NVIDIA's CUDA image, through its REST API.
type Vast struct{ API restClient }

// NewVast returns a Cloud on Vast.ai.
func NewVast() *Vast { return &Vast{API: restClient{Base: "https://console.vast.ai/api/v0"}} }

type vastInstance struct {
	ID             int64  `json:"id"`
	Label          string `json:"label"`
	ActualStatus   string `json:"actual_status"`
	IntendedStatus string `json:"intended_status"`
}

func (v *Vast) Start(ctx context.Context, c Config, m Machine) (string, error) {
	// Vast's own listings write GPU names with spaces, its examples with
	// underscores; ask for both.
	names := []string{m.GPU, strings.ReplaceAll(m.GPU, " ", "_")}
	var offers struct {
		Offers []struct {
			ID       int64   `json:"id"`
			DPHTotal float64 `json:"dph_total"`
		} `json:"offers"`
	}
	err := v.API.do(ctx, http.MethodPost, "/bundles/", c.TokenSecret, map[string]any{
		"gpu_name":      map[string]any{"in": slices.Compact(names)},
		"num_gpus":      map[string]any{"eq": 1},
		"rentable":      map[string]any{"eq": true},
		"rented":        map[string]any{"eq": false},
		"verified":      map[string]any{"eq": true},
		"cuda_max_good": map[string]any{"gte": 12.4},
		"disk_space":    map[string]any{"gte": 20},
		"type":          "ondemand",
		"order":         [][]string{{"dph_total", "asc"}},
		"limit":         5,
	}, &offers)
	if err != nil {
		return "", fmt.Errorf("looking for a %s: %w", m.GPU, err)
	}
	if len(offers.Offers) == 0 {
		return "", fmt.Errorf("no %s is for rent on Vast.ai right now", m.GPU)
	}
	var env strings.Builder
	for _, kv := range []map[string]string{m.Env, m.Secrets} {
		for _, k := range sortedKeys(kv) {
			if strings.ContainsAny(kv[k], " \t\n'\"") {
				return "", fmt.Errorf("%s can't be passed to Vast.ai", k)
			}
			fmt.Fprintf(&env, "-e %s=%s ", k, kv[k])
		}
	}
	var res struct {
		Success     bool            `json:"success"`
		NewContract json.RawMessage `json:"new_contract"`
	}
	err = v.API.do(ctx, http.MethodPut, "/asks/"+strconv.FormatInt(offers.Offers[0].ID, 10)+"/", c.TokenSecret, map[string]any{
		"image":   containerImage,
		"label":   m.Name,
		"disk":    20,
		"env":     strings.TrimSpace(env.String()),
		"runtype": "args",
		"args":    []string{"sh", "-c", m.ContainerScript()},
	}, &res)
	if err != nil {
		return "", err
	}
	id := strings.Trim(string(res.NewContract), `"`)
	if !res.Success || id == "" || id == "null" {
		return "", errors.New("Vast.ai didn't rent the offer")
	}
	return id, nil
}

// instance returns instance id, which Vast answers as an object or a list
// of one.
func (v *Vast) instance(ctx context.Context, c Config, id string) (vastInstance, error) {
	var res struct {
		Instances json.RawMessage `json:"instances"`
	}
	if err := v.API.do(ctx, http.MethodGet, "/instances/"+url.PathEscape(id)+"/", c.TokenSecret, nil, &res); err != nil {
		return vastInstance{}, err
	}
	var one vastInstance
	if json.Unmarshal(res.Instances, &one) == nil && one.ID != 0 {
		return one, nil
	}
	var list []vastInstance
	if json.Unmarshal(res.Instances, &list) == nil && len(list) > 0 {
		return list[0], nil
	}
	return vastInstance{}, errNotFound
}

func (v *Vast) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	in, err := v.instance(ctx, c, id)
	if errors.Is(err, errNotFound) {
		return true, -1, nil
	}
	if err != nil {
		return false, 0, err
	}
	switch in.ActualStatus {
	case "exited", "offline":
		return true, -1, nil
	}
	return in.IntendedStatus == "stopped", -1, nil
}

func (v *Vast) Stop(ctx context.Context, c Config, id string) error {
	err := v.API.do(ctx, http.MethodDelete, "/instances/"+url.PathEscape(id)+"/", c.TokenSecret, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (v *Vast) Find(ctx context.Context, c Config, workerID string) (string, error) {
	var res struct {
		Instances []vastInstance `json:"instances"`
	}
	if err := v.API.do(ctx, http.MethodGet, "/instances/", c.TokenSecret, nil, &res); err != nil {
		return "", err
	}
	name := machineName(workerID)
	for _, in := range res.Instances {
		if in.Label == name {
			return strconv.FormatInt(in.ID, 10), nil
		}
	}
	return "", nil
}
