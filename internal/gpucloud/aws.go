package gpucloud

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// awsImage names AWS's Deep Learning Base AMI, which carries NVIDIA's
// driver and CUDA; the newest is used.
const awsImage = "Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04) *"

// AWS is the Cloud on AWS EC2: an on-demand GPU instance from the Deep
// Learning Base AMI, booted with Machine.VMScript. It terminates itself
// when it powers off.
type AWS struct {
	mu      sync.Mutex
	clients map[[3]string]*ec2.Client
}

// NewAWS returns a Cloud on AWS EC2.
func NewAWS() *AWS { return &AWS{} }

func (a *AWS) connect(ctx context.Context, c Config) (*ec2.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := [3]string{c.Region, c.TokenID, c.TokenSecret}
	if cl := a.clients[key]; cl != nil {
		return cl, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(c.Region)}
	if c.TokenID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.TokenID, c.TokenSecret, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	cl := ec2.NewFromConfig(cfg)
	if a.clients == nil {
		a.clients = map[[3]string]*ec2.Client{}
	}
	a.clients[key] = cl
	return cl, nil
}

func (a *AWS) Start(ctx context.Context, c Config, m Machine) (string, error) {
	cl, err := a.connect(ctx, c)
	if err != nil {
		return "", err
	}
	img, err := cl.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{"amazon"},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{awsImage}},
			{Name: aws.String("architecture"), Values: []string{"x86_64"}},
			{Name: aws.String("state"), Values: []string{"available"}},
		},
	})
	if err != nil {
		return "", awsError(err)
	}
	ami, ok := newestImage(img.Images)
	if !ok {
		return "", fmt.Errorf("AWS has no Deep Learning Base AMI in %s", c.Region)
	}
	out, err := cl.RunInstances(ctx, runInput(m, ami))
	if err != nil {
		return "", awsError(err)
	}
	if len(out.Instances) == 0 || out.Instances[0].InstanceId == nil {
		return "", errors.New("AWS gave no instance id")
	}
	return *out.Instances[0].InstanceId, nil
}

// newestImage returns the image made last.
func newestImage(imgs []types.Image) (types.Image, bool) {
	if len(imgs) == 0 {
		return types.Image{}, false
	}
	return slices.MaxFunc(imgs, func(a, b types.Image) int {
		return strings.Compare(aws.ToString(a.CreationDate), aws.ToString(b.CreationDate))
	}), true
}

// runInput asks for one instance of m from image ami, with a root disk of
// at least 100 GB for the program, datasets and checkpoints.
func runInput(m Machine, ami types.Image) *ec2.RunInstancesInput {
	size := int32(100)
	for _, b := range ami.BlockDeviceMappings {
		if aws.ToString(b.DeviceName) == aws.ToString(ami.RootDeviceName) && b.Ebs != nil {
			size = max(size, aws.ToInt32(b.Ebs.VolumeSize))
		}
	}
	tags := []types.Tag{{Key: aws.String(tag), Value: aws.String(m.WorkerID)}, {Key: aws.String("Name"), Value: aws.String(m.Name)}}
	return &ec2.RunInstancesInput{
		ImageId:      ami.ImageId,
		InstanceType: types.InstanceType(m.GPU),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		UserData:     aws.String(base64.StdEncoding.EncodeToString([]byte(m.VMScript()))),
		// Powering off, when the worker stops or the time is up, ends it.
		InstanceInitiatedShutdownBehavior: types.ShutdownBehaviorTerminate,
		BlockDeviceMappings: []types.BlockDeviceMapping{{
			DeviceName: ami.RootDeviceName,
			Ebs:        &types.EbsBlockDevice{VolumeSize: aws.Int32(size), VolumeType: types.VolumeTypeGp3, DeleteOnTermination: aws.Bool(true)},
		}},
		MetadataOptions: &types.InstanceMetadataOptionsRequest{HttpTokens: types.HttpTokensStateRequired},
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tags},
			{ResourceType: types.ResourceTypeVolume, Tags: tags},
		},
	}
}

func (a *AWS) Exited(ctx context.Context, c Config, id string) (bool, int, error) {
	cl, err := a.connect(ctx, c)
	if err != nil {
		return false, 0, err
	}
	out, err := cl.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if isAWSNotFound(err) {
		return true, -1, nil
	}
	if err != nil {
		return false, 0, awsError(err)
	}
	for _, r := range out.Reservations {
		for _, in := range r.Instances {
			if in.State == nil {
				continue
			}
			switch in.State.Name {
			case types.InstanceStateNameShuttingDown, types.InstanceStateNameTerminated, types.InstanceStateNameStopping, types.InstanceStateNameStopped:
				return true, -1, nil
			}
			return false, 0, nil
		}
	}
	return true, -1, nil
}

func (a *AWS) Stop(ctx context.Context, c Config, id string) error {
	cl, err := a.connect(ctx, c)
	if err != nil {
		return err
	}
	_, err = cl.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: []string{id}})
	if isAWSNotFound(err) {
		return nil
	}
	return awsError(err)
}

func (a *AWS) Find(ctx context.Context, c Config, workerID string) (string, error) {
	cl, err := a.connect(ctx, c)
	if err != nil {
		return "", err
	}
	out, err := cl.DescribeInstances(ctx, &ec2.DescribeInstancesInput{Filters: []types.Filter{
		{Name: aws.String("tag:" + tag), Values: []string{workerID}},
		{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
	}})
	if err != nil {
		return "", awsError(err)
	}
	for _, r := range out.Reservations {
		for _, in := range r.Instances {
			return aws.ToString(in.InstanceId), nil
		}
	}
	return "", nil
}

func isAWSNotFound(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == "InvalidInstanceID.NotFound"
}

// awsError keeps what AWS said and drops the request details.
func awsError(err error) error {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		if ae.ErrorCode() == "VcpuLimitExceeded" {
			return fmt.Errorf("%s: ask AWS to raise the account's quota for G instances under Service Quotas → Amazon EC2 → Running On-Demand G and VT instances", ae.ErrorMessage())
		}
		return fmt.Errorf("%s: %s", ae.ErrorCode(), ae.ErrorMessage())
	}
	return err
}
