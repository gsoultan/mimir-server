// Package arch lists the model architectures the server can build.
//
// A mimir model is Go code, and a weights file alone cannot be run. Each
// Architecture pairs a name with a constructor, so stored weights can be
// loaded into a freshly built module.
package arch

import (
	"slices"
	"strings"

	"github.com/gsoultan/mimir/nn"
)

// Architecture is a model shape the server knows how to build.
type Architecture struct {
	Name        string
	Title       string
	Description string
	// InputShape is the shape of one example, without the batch dimension.
	InputShape []int
	// Labels names each output, in order.
	Labels []string
	// New builds the module with freshly initialized weights.
	New func() nn.Module
}

var digits = []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}

var builtin = []Architecture{
	{
		Name:        "mnist-mlp",
		Title:       "Handwritten digits (small)",
		Description: "Reads a 28×28 picture of one handwritten number. Small and fast.",
		InputShape:  []int{1, 28, 28},
		Labels:      digits,
		New: func() nn.Module {
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
		InputShape:  []int{1, 28, 28},
		Labels:      digits,
		New: func() nn.Module {
			return nn.NewSequential(
				nn.NewConv2d(1, 16, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
				nn.NewConv2d(16, 32, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
				nn.Flatten(),
				nn.NewLinear(32*7*7, 10),
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
