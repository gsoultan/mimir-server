package arch

import (
	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/vision"
)

// Headed is a model whose last layer can be replaced, so a new one learns
// other answers from what an earlier model learned to see: transfer
// learning.
type Headed interface {
	nn.Module
	// SetHead starts a new last layer for classes answers.
	SetHead(classes int)
}

// PhotoModel is one of package vision's networks reading the server's
// photos, whose pixels run from 0 to 1.
type PhotoModel struct {
	Net vision.Model
}

// Forward centers x, [N, 3, PhotoSize, PhotoSize], as photoLayers does,
// and reads it.
func (p *PhotoModel) Forward(x *tensor.Tensor) *tensor.Tensor {
	x = x.Sub(tensor.Scalar[float32](0.5)).Mul(tensor.Scalar[float32](4))
	return p.Net.Forward(x)
}

// SetHead starts a new last layer for classes answers.
func (p *PhotoModel) SetHead(classes int) { p.Net.SetHead(classes) }

// photoNets are the photo architectures built from package vision.
var photoNets = []Architecture{
	{
		Name:        "photo-resnet",
		Title:       "Picture classifier (ResNet)",
		Description: "An 18-layer residual network, the classic choice for photos: the most accurate here, and the slowest to teach. It can start from what another ResNet model learned.",
		Input:       Image,
		New: func(s Spec) nn.Module {
			return &PhotoModel{Net: vision.ResNet18(len(s.Labels), vision.Small())}
		},
	},
	{
		Name:        "photo-mobilenet",
		Title:       "Picture classifier (MobileNet)",
		Description: "A network made for phones: nearly as accurate as a ResNet at a fraction of the work, so it teaches quickly and answers fast on small devices. It can start from what another MobileNet model learned.",
		Input:       Image,
		New: func(s Spec) nn.Module {
			return &PhotoModel{Net: vision.NewMobileNetV2(len(s.Labels), vision.Small(), vision.Width(0.5))}
		},
	},
}

func init() { builtin = append(builtin, photoNets...) }
