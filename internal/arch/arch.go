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

	"github.com/gsoultan/mimir-server/internal/schema"
)

// Input says what kind of example an architecture reads.
type Input string

const (
	Image Input = "image" // a picture, shape [channels, height, width]
	Table Input = "table" // one row of numbers, shape [features]
	// PhotoTable is a photo and a row of numbers side by side, flattened
	// into one shape [schema.PhotoPixels + features].
	PhotoTable Input = "photo_table"
	// Tensor is raw numbers in the shape a Mimir Model file declares.
	Tensor Input = "tensor"
	// Prompt is text for a language model to continue.
	Prompt Input = "text"
)

// Output is what an architecture answers.
type Output string

const (
	Class  Output = "class"  // which label fits, with a probability each
	Number Output = "number" // one number, like a price or a count
	// Written is text a language model writes, one token at a time.
	Written Output = "text"
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
	// TargetMean and TargetStd scale the answer of a number model, so it
	// learns as evenly for prices in millions as for counts in tens.
	TargetMean, TargetStd float32
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

// Unscale is the last layer of a number model: it turns the network's
// output, about zero on average, back into the answer's own units.
type Unscale struct {
	Mean *tensor.Param // [1, 1]
	Std  *tensor.Param // [1, 1]
}

// NewUnscale returns an Unscale for an answer with the given statistics.
func NewUnscale(mean, std float32) *Unscale {
	if std == 0 {
		std = 1
	}
	return &Unscale{
		Mean: tensor.NewBuffer(tensor.FromSlice([]float32{mean}, 1, 1)),
		Std:  tensor.NewBuffer(tensor.FromSlice([]float32{std}, 1, 1)),
	}
}

// Forward returns x * std + mean.
func (u *Unscale) Forward(x *tensor.Tensor) *tensor.Tensor {
	return x.Mul(u.Std.Value()).Add(u.Mean.Value())
}

// PhotoNet reads a photo and a row of numbers together: a small picture
// network and a small table network each sum up their part, and a last
// layer weighs both.
type PhotoNet struct {
	Photo *nn.Sequential
	Table *nn.Sequential
	Head  *nn.Sequential
	F     int // numbers in the table part
}

// Forward splits x, shape [batch, schema.PhotoPixels + F], into its photo
// and table parts and reads both.
func (p *PhotoNet) Forward(x *tensor.Tensor) *tensor.Tensor {
	n := x.Shape()[0]
	const px, side = schema.PhotoPixels, schema.PhotoSize
	photo := x.Shrink([][2]int{{0, n}, {0, px}}).Reshape(n, 3, side, side)
	parts := []*tensor.Tensor{p.Photo.Forward(photo)}
	if p.F > 0 {
		parts = append(parts, p.Table.Forward(x.Shrink([][2]int{{0, n}, {px, px + p.F}})))
	}
	return p.Head.Forward(tensor.Cat(1, parts...))
}

// photoLayers is the picture network shared by the photo architectures:
// three rounds of looking for patterns and halving the size, from
// schema.PhotoSize down to a sixth of it.
func photoLayers() []nn.Module {
	const side = schema.PhotoSize / 8
	return []nn.Module{
		nn.Func(func(x *tensor.Tensor) *tensor.Tensor {
			return x.Sub(tensor.Scalar[float32](0.5)).Mul(tensor.Scalar[float32](4))
		}),
		nn.NewConv2d(3, 16, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
		nn.NewConv2d(16, 32, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
		nn.NewConv2d(32, 64, 3, nn.Padding(1)), nn.ReLU(), nn.MaxPool2d(2),
		nn.Flatten(),
		nn.NewLinear(64*side*side, 128), nn.ReLU(),
	}
}

func size(shape []int) int {
	n := 1
	for _, d := range shape {
		n *= d
	}
	return n
}

// tableNet is the network for a row of numbers: out is how many numbers
// it answers.
func tableNet(s Spec, out int) []nn.Module {
	in := size(s.InputShape)
	hidden := max(16, min(256, 4*in))
	return []nn.Module{
		NewStandardize(in, s.Mean, s.Std),
		nn.NewLinear(in, hidden), nn.ReLU(),
		nn.NewLinear(hidden, hidden), nn.ReLU(),
		nn.NewLinear(hidden, out),
	}
}

// Architecture is a model shape the server knows how to build.
type Architecture struct {
	Name        string
	Title       string
	Description string
	Input       Input
	// Output is what the model answers; empty means Class.
	Output Output
	// Text marks a table architecture made for rows with free text.
	Text bool
	// Spec is the fixed shape and labels, or zero when they come from the
	// dataset.
	Spec Spec
	// New builds the module with freshly initialized weights for spec.
	// It is nil for a File architecture.
	New func(spec Spec) nn.Module
	// File marks the architecture of Mimir Model (.mim) files, which hold
	// their model's graph as well as its weights and so run as they are.
	// Their shape and labels come from the file.
	File bool
}

// Answers returns what the architecture answers.
func (a Architecture) Answers() Output {
	if a.Output == "" {
		return Class
	}
	return a.Output
}

// Fixed reports whether the architecture's shape and labels are built in.
func (a Architecture) Fixed() bool { return len(a.Spec.InputShape) > 0 }

// Trainable reports whether the server can teach models of this shape.
func (a Architecture) Trainable() bool { return !a.File }

// LanguageModel is the name of the architecture of language models, which
// come as whole Mimir Model files.
const LanguageModel = "language-model"

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
			return nn.NewSequential(tableNet(s, len(s.Labels))...)
		},
	},
	{
		Name:        "text-mlp",
		Title:       "Text and table",
		Description: "Reads a row with free text, like a message or a review, next to numbers and categories, and picks a category.",
		Input:       Table,
		Text:        true,
		New: func(s Spec) nn.Module {
			return nn.NewSequential(tableNet(s, len(s.Labels))...)
		},
	},
	{
		Name:        "number-mlp",
		Title:       "Number predictor",
		Description: "Reads one row of a table and answers with a number, like a price, a count or a number of days.",
		Input:       Table,
		Output:      Number,
		New: func(s Spec) nn.Module {
			return nn.NewSequential(append(tableNet(s, 1), NewUnscale(s.TargetMean, s.TargetStd))...)
		},
	},
	{
		Name:        "photo-cnn",
		Title:       "Picture classifier",
		Description: "Looks at a photo and picks a category. Learns from photos sorted into one folder per answer.",
		Input:       Image,
		New: func(s Spec) nn.Module {
			return nn.NewSequential(append(photoLayers(), nn.NewLinear(128, len(s.Labels)))...)
		},
	},
	{
		Name:        "photo-table",
		Title:       "Photo and table classifier",
		Description: "Looks at a photo and reads the details next to it, like a price or a note, and picks a category.",
		Input:       PhotoTable,
		New: func(s Spec) nn.Module {
			f := size(s.InputShape) - schema.PhotoPixels
			hidden := max(16, min(128, 4*f))
			p := &PhotoNet{Photo: nn.NewSequential(photoLayers()...), F: f}
			width := 128
			if f > 0 {
				p.Table = nn.NewSequential(NewStandardize(f, s.Mean, s.Std), nn.NewLinear(f, hidden), nn.ReLU())
				width += hidden
			}
			p.Head = nn.NewSequential(nn.NewLinear(width, 64), nn.ReLU(), nn.NewLinear(64, len(s.Labels)))
			return p
		},
	},
	{
		Name:        "mim",
		Title:       "Mimir Model file",
		Description: "Any model saved as a .mim file by mimir's mim.Export, or an ONNX model, which becomes a .mim file when you upload it. The file holds the whole model, so it runs as it is.",
		Input:       Tensor,
		File:        true,
	},
	{
		Name:        LanguageModel,
		Title:       "Language model",
		Description: "Give it the start of a text and it carries on writing. Upload a GGUF file of a Llama, TinyLlama, Mistral, SmolLM or Qwen 2 model; it becomes a .mim file when you upload it.",
		Input:       Prompt,
		Output:      Written,
		File:        true,
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
