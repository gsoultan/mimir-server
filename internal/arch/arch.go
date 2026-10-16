// Package arch lists the model architectures the server can build.
//
// A mimir model is Go code, and a weights file alone cannot be run. Each
// Architecture pairs a name with a constructor, so stored weights can be
// loaded into a freshly built module, and a fresh module can be trained.
package arch

import (
	"slices"
	"strings"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/tensor"
)

// Input says what kind of example an architecture reads.
type Input string

const (
	Image Input = "image" // a picture, shape [channels, height, width]
	Table Input = "table" // one row of numbers, shape [features]
)

// Spec is what a model reads and answers. Fixed architectures carry their
// own; a tabular one takes the spec from the dataset it learns from.
type Spec struct {
	// InputShape is the shape of one example, without the batch dimension.
	InputShape []int
	// Labels names each output, in order.
	Labels []string
	// Mean and Std, when set, are the per-feature statistics a tabular
	// model standardizes its input with. They are baked into the model as
	// buffers, so they only matter when building a fresh module to train.
	Mean, Std []float32
}

// Standardize is a module that rescales each input feature to roughly zero
// mean and unit variance, so a model taught from raw spreadsheet columns
// (grams next to centimetres) learns evenly. Its statistics are buffers:
// saved with the model, never trained.
type Standardize struct {
	Mean *tensor.Param // [1, in]
	Std  *tensor.Param // [1, in]
}

// NewStandardize returns a Standardize for in features with the given
// statistics, or an identity when they are missing.
func NewStandardize(in int, mean, std []float32) *Standardize {
	if len(mean) != in || len(std) != in {
		mean = make([]float32, in)
		std = make([]float32, in)
		for i := range std {
			std[i] = 1
		}
	}
	return &Standardize{
		Mean: tensor.NewBuffer(tensor.FromSlice(slices.Clone(mean), 1, in)),
		Std:  tensor.NewBuffer(tensor.FromSlice(slices.Clone(std), 1, in)),
	}
}

// Forward returns (x - mean) / std.
func (s *Standardize) Forward(x *tensor.Tensor) *tensor.Tensor {
	return x.Sub(s.Mean.Value()).Div(s.Std.Value())
}

// Architecture is a model shape the server knows how to build.
type Architecture struct {
	Name        string
	Title       string
	Description string
	Input       Input
	// Spec is the fixed shape and labels, or zero when they come from the
	// dataset.
	Spec Spec
	// New builds the module with freshly initialized weights for spec.
	New func(spec Spec) nn.Module
}

// Fixed reports whether the architecture's shape and labels are built in.
func (a Architecture) Fixed() bool { return len(a.Spec.InputShape) > 0 }

// Trainable reports whether the server can teach models of this shape.
func (a Architecture) Trainable() bool { return true }

var digits = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

var builtin = []Architecture{
	{
		Name:        "mnist-mlp",
		Title:       "Handwritten digits (small)",
		Description: "Reads a 28×28 picture of one handwritten number. Small and fast.",
		Input:       Image,
		Spec:        Spec{InputShape: []int{1, 28, 28}, Labels: digits},
		New: func(Spec) nn.Module {
			return nn.NewSequential(
				nn.Flatten(),
				nn.NewLinear(28*28, 256), nn.ReLU(),
				nn.NewLinear(256, 10),
			)
		},
	},
	{
		Name:        "mnist-cnn",
		Title:       "Handwritten digits (accurate)",
		Description: "Reads a 28×28 picture of one handwritten number. Slower, more accurate.",
		Input:       Image,
		Spec:        Spec{InputShape: []int{1, 28, 28}, Labels: digits},
		New: func(Spec) nn.Module {
			return nn.NewSequential(
				nn.NewConv2d(1, 16, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
				nn.NewConv2d(16, 32, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
				nn.Flatten(),
				nn.NewLinear(32*7*7, 10),
			)
		},
	},
	{
		Name:        "table-mlp",
		Title:       "Table of numbers",
		Description: "Reads one row of numbers, like a spreadsheet line, and picks a category. Learns from a CSV you upload.",
		Input:       Table,
		New: func(s Spec) nn.Module {
			in := 1
			for _, d := range s.InputShape {
				in *= d
			}
			hidden := max(16, min(256, 4*in))
			return nn.NewSequential(
				NewStandardize(in, s.Mean, s.Std),
				nn.NewLinear(in, hidden), nn.ReLU(),
				nn.NewLinear(hidden, hidden), nn.ReLU(),
				nn.NewLinear(hidden, len(s.Labels)),
			)
		},
	},
}

// All returns every architecture, sorted by name.
func All() []Architecture {
	out := slices.Clone(builtin)
	slices.SortFunc(out, func(a, b Architecture) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Lookup returns the architecture called name.
func Lookup(name string) (Architecture, bool) {
	i := slices.IndexFunc(builtin, func(a Architecture) bool { return a.Name == name })
	if i < 0 {
		return Architecture{}, false
	}
	return builtin[i], true
}

// Resolve returns the spec a model of architecture a uses: the fixed one,
// or the stored one for a dataset-shaped model.
func Resolve(a Architecture, stored Spec) Spec {
	if a.Fixed() {
		return a.Spec
	}
	return stored
}
