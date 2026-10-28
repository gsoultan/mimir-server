package gpucloud

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gcpImage is Google's Deep Learning VM base image, which carries
// NVIDIA's driver and CUDA.
const gcpImage = "projects/deeplearning-platform-release/global/images/family/common-cu129-ubuntu-2204-nvidia-580"

// gcpMachines are the machine types for each GPU; a T4 is added to an N1
// machine, the others come with theirs.
var gcpMachines = map[string]struct {
	Type  string
	Added bool
}{
	"nvidia-tesla-t4":   {"n1-standard-4", true},
	"nvidia-l4":         {"g2-standard-4", false},
	"nvidia-tesla-a100": {"a2-highgpu-1g", false},
	"nvidia-a100-80gb":  {"a2-ultragpu-1g", false},

	"nvidia-l4:4":         {"g2-standard-48", false},
	"nvidia-a100-80gb:4":  {"a2-ultragpu-4g", false},
	"nvidia-tesla-a100:8": {"a2-highgpu-8g", false},
}

// GCP is the Cloud on Google Compute Engine: a VM from the Deep Learning
// VM image, booted with Machine.VMScript, which Google deletes itself once
// it has run MaxHours. The machine id is the VM's name.
type GCP struct {
	API restClient
	// TokenURL is where a service account's token is fetched, which tests
	// point at a fake; empty takes the one in the key.
	TokenURL string

	mu     sync.Mutex
	tokens map[string]gcpToken // by client email
}

type gcpToken struct {
	Value   string
	Expires time.Time
}

// NewGCP returns a Cloud on Google Compute Engine.
func NewGCP() *GCP { return &GCP{API: restClient{Base: "https://compute.googleapis.com/compute/v1"}} }

// token returns an access token for c's service account, signing in again
// when the last is about to run out.
func (g *GCP) token(ctx context.Context, c Config) (string, serviceAccount, error) {
	sa, err := parseServiceAccount(c.TokenSecret)
	if err != nil {
		return "", sa, err
	}
	g.mu.Lock()
	t, ok := g.tokens[sa.ClientEmail+"\x00"+sa.PrivateKey]
	g.mu.Unlock()
	if ok && time.Until(t.Expires) > 5*time.Minute {
		return t.Value, sa, nil
	}
	aud := sa.TokenURI
	if aud == "" {
		aud = "https://oauth2.googleapis.com/token"
	}
	tokenURL := aud
	if g.TokenURL != "" {
		tokenURL = g.TokenURL
	}
	assertion, err := signJWT(sa, aud, time.Now())
	if err != nil {
		return "", sa, err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", sa, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.API.client().Do(req)
	if err != nil {
		return "", sa, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	if err := json.Unmarshal(b, &tok); err != nil || tok.AccessToken == "" {
		if tok.Error == "" {
			tok.Error = apiMessage(b)
		}
		return "", sa, fmt.Errorf("Google refused the service account: %s", tok.Error)
	}
	g.mu.Lock()
	if g.tokens == nil {
		g.tokens = map[string]gcpToken{}
	}
	g.tokens[sa.ClientEmail+"\x00"+sa.PrivateKey] = gcpToken{tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)}
	g.mu.Unlock()
	return tok.AccessToken, sa, nil
}

// signJWT makes the assertion a service account signs in with.
func signJWT(sa serviceAccount, aud string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return "", errors.New("the service account key has no private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok {
		return "", errors.New("the service account's private key isn't an RSA key")
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(map[string]any{
		"iss":   sa.ClientEmail,
		"scope": "https://www.googleapis.com/auth/compute",
		"aud":   aud,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	sum := sha256.Sum256([]byte(head))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return head + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func gcpZonePath(sa serviceAccount, zone string) string {
	return "/projects/" + url.PathEscape(sa.ProjectID) + "/zones/" + url.PathEscape(zone)
}

// gcpInstance is the VM to create for m.
func gcpInstance(m Machine) (map[string]any, error) {
	mt, ok := gcpMachines[m.GPU]
	if !ok {
		return nil, fmt.Errorf("no machine type for %s", m.GPU)
	}
	inst := map[string]any{
		"name":        m.Name,
		"machineType": "zones/" + m.Region + "/machineTypes/" + mt.Type,
		"disks": []any{map[string]any{
			"boot": true, "autoDelete": true,
			"initializeParams": map[string]any{"sourceImage": gcpImage, "diskSizeGb": "100"},
		}},
		"networkInterfaces": []any{map[string]any{
			"network":       "global/networks/default",
			"accessConfigs": []any{map[string]any{"type": "ONE_TO_ONE_NAT", "name": "External NAT"}},
		}},
		"scheduling": map[string]any{
			"onHostMaintenance":         "TERMINATE",
			"automaticRestart":          false,
			"instanceTerminationAction": "DELETE",
			"maxRunDuration":            map[string]any{"seconds": strconv.Itoa(int(m.Timeout.Seconds()))},
		},
		"metadata": map[string]any{"items": []any{map[string]any{"key": "startup-script", "value": m.VMScript()}}},
		"labels":   map[string]string{tag: m.WorkerID},
	}
	if mt.Added {
		inst["guestAccelerators"] = []any{map[string]any{"acceleratorType": "zones/" + m.Region + "/acceleratorTypes/" + gpuName(m.GPU), "acceleratorCount": max(1, m.GPUs)}}
	}
	return inst, nil
}

type gcpOperation struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  *struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	} `json:"error"`
}

func (op gcpOperation) err() error {
	if op.Error == nil || len(op.Error.Errors) == 0 {
		return nil
	}
	e := op.Error.Errors[0]
	return fmt.Errorf("%s: %s", e.Code, e.Message)
}

func (g *GCP) Start(ctx context.Context, c Config, m Machine) (string, error) {
	tok, sa, err := g.token(ctx, c)
	if err != nil {
		return "", err
	}
	inst, err := gcpInstance(m)
	if err != nil {
		return "", err
	}
	zone := gcpZonePath(sa, m.Region)
	var op gcpOperation
	if err := g.API.do(ctx, http.MethodPost, zone+"/instances", tok, inst, &op); err != nil {
		return "", err
	}
	// Creating can still fail, such as when the zone has no GPU free;
	// wait to hear.
	for i := 0; op.Status != "DONE" && op.Name != "" && i < 3; i++ {
		if err := g.API.do(ctx, http.MethodPost, zone+"/operations/"+url.PathEscape(op.Name)+"/wait", tok, nil, &op); err != nil {
			return "", err
		}
	}
	if err := op.err(); err != nil {
		return "", err
	}
	return m.Name, nil
}

func (g *GCP) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	tok, sa, err := g.token(ctx, c)
	if err != nil {
		return false, 0, err
	}
	var inst struct {
		Status string `json:"status"`
	}
	err = g.API.do(ctx, http.MethodGet, gcpZonePath(sa, c.Region)+"/instances/"+url.PathEscape(id), tok, nil, &inst)
	if errors.Is(err, errNotFound) {
		return true, -1, nil
	}
	if err != nil {
		return false, 0, err
	}
	switch inst.Status {
	case "STOPPING", "STOPPED", "SUSPENDING", "SUSPENDED", "TERMINATED":
		return true, -1, nil
	}
	return false, 0, nil
}

func (g *GCP) Stop(ctx context.Context, c Config, id string) error {
	tok, sa, err := g.token(ctx, c)
	if err != nil {
		return err
	}
	err = g.API.do(ctx, http.MethodDelete, gcpZonePath(sa, c.Region)+"/instances/"+url.PathEscape(id), tok, nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

func (g *GCP) Find(ctx context.Context, c Config, workerID string) (string, error) {
	tok, sa, err := g.token(ctx, c)
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"items"`
	}
	filter := url.QueryEscape(fmt.Sprintf("labels.%s = %q", tag, workerID))
	if err := g.API.do(ctx, http.MethodGet, gcpZonePath(sa, c.Region)+"/instances?filter="+filter, tok, nil, &list); err != nil {
		return "", err
	}
	for _, in := range list.Items {
		return in.Name, nil
	}
	return "", nil
}
