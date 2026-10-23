package gpucloud

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

var testMachine = Machine{
	WorkerID: "0b9e6c3a-1111-4222-8333-944445555666",
	Name:     "mimir-worker-0b9e6c3a",
	Timeout:  6 * time.Hour,
	Setup:    "curl -o /usr/local/bin/mimir-server https://example.com/m",
	Run:      "/usr/local/bin/mimir-server -role=worker",
	Env:      map[string]string{"MIMIR_DEVICE": "gpu"},
	Secrets:  map[string]string{"MIMIR_WORKER_SECRET": "mimirw_abc"},
}

// call is one request a fake provider got.
type call struct {
	Method, Path, Auth string
	Body               map[string]any
}

// fakeAPI answers each "METHOD /path" with a status and JSON, and records
// the calls.
func fakeAPI(t *testing.T, answers map[string]any) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &c.Body)
		}
		calls = append(calls, c)
		a, ok := answers[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"no such thing"}`)
			return
		}
		if code, ok := a.(int); ok {
			w.WriteHeader(code)
			return
		}
		json.NewEncoder(w).Encode(a)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRunPod(t *testing.T) {
	ctx := context.Background()
	srv, calls := fakeAPI(t, map[string]any{
		"POST /pods":        map[string]any{"id": "pod1"},
		"GET /pods/pod1":    map[string]any{"id": "pod1", "desiredStatus": "RUNNING"},
		"DELETE /pods/pod1": 204,
		"GET /pods":         []map[string]any{{"id": "pod0", "name": "other"}, {"id": "pod1", "name": "mimir-worker-0b9e6c3a", "desiredStatus": "RUNNING"}},
	})
	r := &RunPod{API: restClient{Base: srv.URL}}
	c := Config{TokenSecret: "rpa_key"}
	m := testMachine
	m.GPU = "NVIDIA L4"
	id, err := r.Start(ctx, c, m)
	if err != nil || id != "pod1" {
		t.Fatalf("start: %q, %v", id, err)
	}
	body := (*calls)[0].Body
	if (*calls)[0].Auth != "Bearer rpa_key" || body["imageName"] != containerImage || body["gpuTypeIds"].([]any)[0] != "NVIDIA L4" ||
		body["env"].(map[string]any)["MIMIR_WORKER_SECRET"] != "mimirw_abc" || !strings.Contains(body["dockerStartCmd"].([]any)[2].(string), "sleep infinity") {
		t.Fatalf("create: %+v", (*calls)[0])
	}
	if exited, _, err := r.Exited(ctx, c, "pod1"); exited || err != nil {
		t.Fatalf("running pod exited: %v", err)
	}
	if exited, code, err := r.Exited(ctx, c, "gone"); !exited || code != -1 || err != nil {
		t.Fatalf("missing pod: %v %d %v", exited, code, err)
	}
	if found, err := r.Find(ctx, c, m.WorkerID); found != "pod1" || err != nil {
		t.Fatalf("find: %q %v", found, err)
	}
	if err := r.Stop(ctx, c, "pod1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx, c, "gone"); err != nil {
		t.Fatalf("stopping a pod already gone: %v", err)
	}
}

func TestLambda(t *testing.T) {
	ctx := context.Background()
	srv, calls := fakeAPI(t, map[string]any{
		"POST /instance-operations/launch":    map[string]any{"data": map[string]any{"instance_ids": []string{"i1"}}},
		"GET /instances/i1":                   map[string]any{"data": map[string]any{"id": "i1", "status": "terminated"}},
		"POST /instance-operations/terminate": map[string]any{"data": map[string]any{}},
		"GET /instances":                      map[string]any{"data": []map[string]any{{"id": "i1", "name": "mimir-worker-0b9e6c3a", "status": "active"}}},
	})
	l := &Lambda{API: restClient{Base: srv.URL}}
	c := Config{TokenSecret: "secret_k", SSHKey: "me"}
	m := testMachine
	m.GPU, m.Region = "gpu_1x_a10", "us-east-1"
	if id, err := l.Start(ctx, c, m); id != "i1" || err != nil {
		t.Fatalf("start: %q %v", id, err)
	}
	b := (*calls)[0].Body
	if b["region_name"] != "us-east-1" || b["instance_type_name"] != "gpu_1x_a10" || b["ssh_key_names"].([]any)[0] != "me" || !strings.HasPrefix(b["user_data"].(string), "#!/bin/bash") {
		t.Fatalf("launch: %+v", b)
	}
	if exited, _, err := l.Exited(ctx, c, "i1"); !exited || err != nil {
		t.Fatalf("terminated: %v", err)
	}
	if found, _ := l.Find(ctx, c, m.WorkerID); found != "i1" {
		t.Fatalf("find: %q", found)
	}
	if err := l.Stop(ctx, c, "i1"); err != nil || (*calls)[len(*calls)-1].Body["instance_ids"].([]any)[0] != "i1" {
		t.Fatalf("stop: %v", err)
	}
}

func TestVast(t *testing.T) {
	ctx := context.Background()
	srv, calls := fakeAPI(t, map[string]any{
		"POST /bundles/":         map[string]any{"offers": []map[string]any{{"id": 42, "dph_total": 0.31}}},
		"PUT /asks/42/":          map[string]any{"success": true, "new_contract": 777},
		"GET /instances/777/":    map[string]any{"instances": map[string]any{"id": 777, "actual_status": "running"}},
		"DELETE /instances/777/": map[string]any{"success": true},
		"GET /instances/":        map[string]any{"instances": []map[string]any{{"id": 777, "label": "mimir-worker-0b9e6c3a"}}},
	})
	v := &Vast{API: restClient{Base: srv.URL}}
	c := Config{TokenSecret: "vk"}
	m := testMachine
	m.GPU = "RTX 4090"
	id, err := v.Start(ctx, c, m)
	if id != "777" || err != nil {
		t.Fatalf("start: %q %v", id, err)
	}
	if names := (*calls)[0].Body["gpu_name"].(map[string]any)["in"].([]any); len(names) != 2 || names[1] != "RTX_4090" {
		t.Fatalf("search: %+v", (*calls)[0].Body)
	}
	ask := (*calls)[1].Body
	if ask["label"] != m.Name || ask["env"] != "-e MIMIR_DEVICE=gpu -e MIMIR_WORKER_SECRET=mimirw_abc" || ask["runtype"] != "args" {
		t.Fatalf("ask: %+v", ask)
	}
	if exited, _, err := v.Exited(ctx, c, "777"); exited || err != nil {
		t.Fatalf("running: %v", err)
	}
	if exited, _, _ := v.Exited(ctx, c, "1"); !exited {
		t.Fatal("a missing instance is gone")
	}
	if found, _ := v.Find(ctx, c, m.WorkerID); found != "777" {
		t.Fatalf("find: %q", found)
	}
	if err := v.Stop(ctx, c, "777"); err != nil {
		t.Fatal(err)
	}

	// None for rent.
	srv2, _ := fakeAPI(t, map[string]any{"POST /bundles/": map[string]any{"offers": []any{}}})
	if _, err := (&Vast{API: restClient{Base: srv2.URL}}).Start(ctx, c, m); err == nil || !strings.Contains(err.Error(), "no RTX 4090") {
		t.Fatalf("no offers: %v", err)
	}
}

func TestGCP(t *testing.T) {
	ctx := context.Background()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	sa, _ := json.Marshal(map[string]string{
		"project_id":   "proj",
		"client_email": "mimir@proj.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	zone := "/projects/proj/zones/us-central1-a"
	srv, calls := fakeAPI(t, map[string]any{
		"POST /token":                                         map[string]any{"access_token": "ya29", "expires_in": 3600},
		"POST " + zone + "/instances":                         map[string]any{"name": "op1", "status": "RUNNING"},
		"POST " + zone + "/operations/op1/wait":               map[string]any{"name": "op1", "status": "DONE"},
		"GET " + zone + "/instances/mimir-worker-0b9e6c3a":    map[string]any{"status": "TERMINATED"},
		"DELETE " + zone + "/instances/mimir-worker-0b9e6c3a": map[string]any{"name": "op2"},
		"GET " + zone + "/instances":                          map[string]any{"items": []map[string]any{{"name": "mimir-worker-0b9e6c3a"}}},
	})
	g := &GCP{API: restClient{Base: srv.URL}, TokenURL: srv.URL + "/token"}
	c := Config{TokenSecret: string(sa), Region: "us-central1-a"}
	m := testMachine
	m.GPU, m.Region = "nvidia-tesla-t4", "us-central1-a"
	id, err := g.Start(ctx, c, m)
	if err != nil || id != m.Name {
		t.Fatalf("start: %q %v", id, err)
	}
	// The assertion is signed by the service account's key.
	if (*calls)[0].Path != "/token" {
		t.Fatalf("first call %+v", (*calls)[0])
	}
	ins := (*calls)[1]
	if ins.Auth != "Bearer ya29" || ins.Body["machineType"] != "zones/us-central1-a/machineTypes/n1-standard-4" ||
		ins.Body["guestAccelerators"].([]any)[0].(map[string]any)["acceleratorType"] != "zones/us-central1-a/acceleratorTypes/nvidia-tesla-t4" ||
		ins.Body["scheduling"].(map[string]any)["maxRunDuration"].(map[string]any)["seconds"] != "21600" {
		t.Fatalf("insert: %+v", ins)
	}
	if exited, _, err := g.Exited(ctx, c, id); !exited || err != nil {
		t.Fatalf("terminated: %v", err)
	}
	if found, _ := g.Find(ctx, c, m.WorkerID); found != id {
		t.Fatalf("find %q", found)
	}
	if err := g.Stop(ctx, c, id); err != nil {
		t.Fatal(err)
	}
	tokens := 0
	for _, c := range *calls {
		if c.Path == "/token" {
			tokens++
		}
	}
	if tokens != 1 {
		t.Fatalf("signed in %d times", tokens)
	}

	// The JWT verifies with the account's public key.
	jwt, _ := signJWT(serviceAccount{ClientEmail: "a@b", PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))}, "aud", time.Now())
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !strings.Contains(string(claims), `"scope":"https://www.googleapis.com/auth/compute"`) {
		t.Fatalf("claims %s", claims)
	}

	// The zone having no GPU free fails the start.
	srv2, _ := fakeAPI(t, map[string]any{
		"POST /token":                 map[string]any{"access_token": "ya29", "expires_in": 3600},
		"POST " + zone + "/instances": map[string]any{"name": "op1", "status": "DONE", "error": map[string]any{"errors": []map[string]any{{"code": "ZONE_RESOURCE_POOL_EXHAUSTED", "message": "no T4"}}}},
	})
	g2 := &GCP{API: restClient{Base: srv2.URL}, TokenURL: srv2.URL + "/token"}
	if _, err := g2.Start(ctx, c, m); err == nil || !strings.Contains(err.Error(), "ZONE_RESOURCE_POOL_EXHAUSTED") {
		t.Fatalf("exhausted: %v", err)
	}
}

func TestAWSRunInput(t *testing.T) {
	m := testMachine
	m.GPU = "g6.xlarge"
	img, ok := newestImage([]types.Image{
		{ImageId: aws.String("ami-old"), CreationDate: aws.String("2025-01-01T00:00:00.000Z")},
		{ImageId: aws.String("ami-new"), CreationDate: aws.String("2026-09-01T00:00:00.000Z"), RootDeviceName: aws.String("/dev/sda1"),
			BlockDeviceMappings: []types.BlockDeviceMapping{{DeviceName: aws.String("/dev/sda1"), Ebs: &types.EbsBlockDevice{VolumeSize: aws.Int32(120)}}}},
	})
	if !ok || aws.ToString(img.ImageId) != "ami-new" {
		t.Fatalf("newest: %v", img.ImageId)
	}
	in := runInput(m, img)
	ud, _ := base64.StdEncoding.DecodeString(aws.ToString(in.UserData))
	if in.InstanceType != "g6.xlarge" || in.InstanceInitiatedShutdownBehavior != types.ShutdownBehaviorTerminate ||
		aws.ToInt32(in.BlockDeviceMappings[0].Ebs.VolumeSize) != 120 || !strings.Contains(string(ud), "shutdown -h now") ||
		aws.ToString(in.TagSpecifications[0].Tags[0].Value) != m.WorkerID {
		t.Fatalf("run input: %+v", in)
	}
	if _, ok := newestImage(nil); ok {
		t.Fatal("no images")
	}
}
