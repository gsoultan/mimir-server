package gpucloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Providers the console can rent a GPU from.
const (
	ProviderModal  = "modal"
	ProviderAWS    = "aws"
	ProviderGCP    = "gcp"
	ProviderRunPod = "runpod"
	ProviderLambda = "lambda"
	ProviderVast   = "vast"
)

// GPU is one machine a provider rents, as offered in the console.
type GPU struct {
	// ID is what the provider calls it: a GPU type, an instance type or a
	// machine type.
	ID string
	// Label names it with its memory and about what an hour costs.
	Label string
}

// Provider describes a place that rents GPUs.
type Provider struct {
	ID, Name string
	// GPUs are the machines offered, cheapest first; the first that
	// suits most training is DefaultGPU.
	GPUs       []GPU
	DefaultGPU string
	// Regions are suggested where the provider asks for one; the person
	// may name another. Empty means the provider places the machine.
	Regions       []string
	DefaultRegion string
	// Logs says where to look when a machine doesn't join.
	Logs string
	// OwnTimeout says the provider stops the machine at Machine.Timeout
	// itself; for the rest the launcher does.
	OwnTimeout bool
}

// Providers are the providers offered, in the order shown.
var Providers = []Provider{
	{
		ID: ProviderModal, Name: "Modal",
		GPUs: []GPU{
			{"T4", "T4, 16 GB · about $0.59/h"},
			{"L4", "L4, 24 GB · about $0.80/h"},
			{"A10", "A10, 24 GB · about $1.10/h"},
			{"L40S", "L40S, 48 GB · about $1.95/h"},
			{"A100-40GB", "A100, 40 GB · about $2.10/h"},
			{"A100-80GB", "A100, 80 GB · about $2.50/h"},
			{"H100", "H100, 80 GB · about $3.95/h"},
			{"H200", "H200, 141 GB · about $4.54/h"},
			{"B200", "B200, 180 GB · about $6.25/h"},
		},
		DefaultGPU: "L4",
		Logs:       "see its logs under Apps on modal.com",
		OwnTimeout: true,
	},
	{
		ID: ProviderAWS, Name: "AWS EC2",
		GPUs: []GPU{
			{"g4dn.xlarge", "T4, 16 GB (g4dn.xlarge) · about $0.53/h"},
			{"g6.xlarge", "L4, 24 GB (g6.xlarge) · about $0.81/h"},
			{"g5.xlarge", "A10G, 24 GB (g5.xlarge) · about $1.01/h"},
			{"g6e.xlarge", "L40S, 48 GB (g6e.xlarge) · about $1.86/h"},
		},
		DefaultGPU:    "g6.xlarge",
		Regions:       []string{"us-east-1", "us-east-2", "us-west-2", "eu-west-1", "eu-central-1", "ap-northeast-1", "ap-southeast-1"},
		DefaultRegion: "us-east-1",
		Logs:          "see its system log in the EC2 console (Actions → Monitor and troubleshoot → Get system log)",
	},
	{
		ID: ProviderGCP, Name: "Google Cloud",
		GPUs: []GPU{
			{"nvidia-tesla-t4", "T4, 16 GB (n1-standard-4) · about $0.54/h"},
			{"nvidia-l4", "L4, 24 GB (g2-standard-4) · about $0.71/h"},
			{"nvidia-tesla-a100", "A100, 40 GB (a2-highgpu-1g) · about $3.67/h"},
			{"nvidia-a100-80gb", "A100, 80 GB (a2-ultragpu-1g) · about $5.07/h"},
		},
		DefaultGPU:    "nvidia-l4",
		Regions:       []string{"us-central1-a", "us-central1-b", "us-east1-c", "us-east4-c", "us-west1-b", "us-west4-a", "europe-west4-a", "europe-west1-b", "asia-southeast1-b"},
		DefaultRegion: "us-central1-a",
		Logs:          "see its serial port output on the VM's page in the Compute Engine console",
	},
	{
		ID: ProviderRunPod, Name: "RunPod",
		GPUs: []GPU{
			{"NVIDIA RTX A5000", "RTX A5000, 24 GB · about $0.27/h"},
			{"NVIDIA L4", "L4, 24 GB · about $0.43/h"},
			{"NVIDIA RTX A6000", "RTX A6000, 48 GB · about $0.49/h"},
			{"NVIDIA GeForce RTX 4090", "RTX 4090, 24 GB · about $0.69/h"},
			{"NVIDIA L40S", "L40S, 48 GB · about $0.86/h"},
			{"NVIDIA A100 80GB PCIe", "A100, 80 GB · about $1.64/h"},
			{"NVIDIA H100 80GB HBM3", "H100, 80 GB · about $2.69/h"},
		},
		DefaultGPU: "NVIDIA L4",
		Logs:       "see the pod's logs on runpod.io",
	},
	{
		ID: ProviderLambda, Name: "Lambda Cloud",
		GPUs: []GPU{
			{"gpu_1x_a10", "A10, 24 GB · about $0.75/h"},
			{"gpu_1x_a6000", "RTX A6000, 48 GB · about $0.80/h"},
			{"gpu_1x_a100_sxm4", "A100, 40 GB · about $1.29/h"},
			{"gpu_1x_h100_pcie", "H100, 80 GB · about $2.49/h"},
		},
		DefaultGPU:    "gpu_1x_a10",
		Regions:       []string{"us-east-1", "us-west-1", "us-west-2", "us-west-3", "us-south-1", "us-midwest-1", "europe-central-1", "asia-northeast-1", "asia-south-1"},
		DefaultRegion: "us-east-1",
		Logs:          "its output is in /var/log/mimir-worker.log on the instance",
	},
	{
		ID: ProviderVast, Name: "Vast.ai",
		GPUs: []GPU{
			{"RTX 3090", "RTX 3090, 24 GB · often about $0.20/h"},
			{"RTX 4090", "RTX 4090, 24 GB · often about $0.35/h"},
			{"RTX A6000", "RTX A6000, 48 GB · often about $0.45/h"},
			{"L40S", "L40S, 48 GB · often about $0.70/h"},
			{"A100 SXM4", "A100, 80 GB · often about $1.00/h"},
			{"H100 SXM", "H100, 80 GB · often about $1.90/h"},
		},
		DefaultGPU: "RTX 4090",
		Logs:       "see the instance's logs on vast.ai",
	},
}

// ProviderByID returns the provider called id.
func ProviderByID(id string) (Provider, bool) {
	i := slices.IndexFunc(Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		return Provider{}, false
	}
	return Providers[i], true
}

func (p Provider) hasGPU(id string) bool {
	return slices.ContainsFunc(p.GPUs, func(g GPU) bool { return g.ID == id })
}

func (p Provider) gpuIDs() string {
	ids := make([]string, len(p.GPUs))
	for i, g := range p.GPUs {
		ids[i] = g.ID
	}
	return strings.Join(ids, ", ")
}

// checkProvider reports what is wrong with c's credentials, GPU and region
// for its provider.
func (c Config) checkProvider(p Provider) error {
	switch p.ID {
	case ProviderModal:
		if (c.TokenID == "") != (c.TokenSecret == "") {
			return errors.New("Give both the Modal token id and its secret, or neither to use the console's MODAL_TOKEN_ID and MODAL_TOKEN_SECRET.")
		}
		if c.TokenID != "" && (!strings.HasPrefix(c.TokenID, "ak-") || !strings.HasPrefix(c.TokenSecret, "as-")) {
			return errors.New("A Modal token id starts with ak- and its secret with as-. Make one under Settings → API tokens on modal.com.")
		}
		gpu, _, _ := strings.Cut(c.GPU, ":")
		if !validModalGPU(gpu) {
			return fmt.Errorf("Modal has no GPU called %q. Pick one of %s.", c.GPU, p.gpuIDs())
		}
		return nil
	case ProviderAWS:
		if (c.TokenID == "") != (c.TokenSecret == "") {
			return errors.New("Give both the AWS access key id and its secret, or neither to use the console's own AWS credentials.")
		}
		if c.TokenID != "" && !(strings.HasPrefix(c.TokenID, "AKIA") || strings.HasPrefix(c.TokenID, "ASIA")) {
			return errors.New("An AWS access key id starts with AKIA. Make one in IAM for a user allowed to run and terminate EC2 instances.")
		}
	case ProviderGCP:
		if c.TokenSecret == "" {
			return errors.New("Paste the JSON key of a Google Cloud service account allowed to create and delete VMs (Compute Instance Admin).")
		}
		if _, err := parseServiceAccount(c.TokenSecret); err != nil {
			return err
		}
	case ProviderRunPod:
		if c.TokenSecret == "" {
			return errors.New("Give a RunPod API key, from Settings → API Keys on runpod.io.")
		}
	case ProviderLambda:
		if c.TokenSecret == "" {
			return errors.New("Give a Lambda Cloud API key, from API keys in the Lambda Cloud dashboard.")
		}
		if strings.TrimSpace(c.SSHKey) == "" {
			return errors.New("Lambda Cloud needs the name of one of your SSH keys there to start a machine, even though the console never uses it.")
		}
	case ProviderVast:
		if c.TokenSecret == "" {
			return errors.New("Give a Vast.ai API key, from Account → Keys on vast.ai.")
		}
	}
	if !p.hasGPU(c.GPU) {
		return fmt.Errorf("Pick one of the GPUs offered on %s: %s.", p.Name, p.gpuIDs())
	}
	if len(p.Regions) > 0 && !validRegion(c.Region) {
		return fmt.Errorf("Name the %s to start the machine in, such as %s.", map[bool]string{true: "zone", false: "region"}[p.ID == ProviderGCP], p.DefaultRegion)
	}
	return nil
}

func validModalGPU(g string) bool {
	p, _ := ProviderByID(ProviderModal)
	for _, x := range p.GPUs {
		if strings.EqualFold(x.ID, g) {
			return true
		}
	}
	// Modal also takes other names for some.
	switch strings.ToUpper(g) {
	case "A10G", "A100", "H100!":
		return true
	}
	return false
}

// validRegion accepts names like us-east-1 and europe-west4-a.
func validRegion(r string) bool {
	if r == "" || len(r) > 40 {
		return false
	}
	for _, ch := range r {
		if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

// serviceAccount is the part of a Google service account key used.
type serviceAccount struct {
	ProjectID   string `json:"project_id"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func parseServiceAccount(s string) (serviceAccount, error) {
	var sa serviceAccount
	if err := json.Unmarshal([]byte(s), &sa); err != nil || sa.ClientEmail == "" || sa.PrivateKey == "" || sa.ProjectID == "" {
		return sa, errors.New("That isn't a Google Cloud service account key. Make one under IAM → Service accounts → Keys → Add key → JSON, and paste the whole file.")
	}
	return sa, nil
}
