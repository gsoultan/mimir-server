package arch

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/gsoultan/mimir/nn"
	"github.com/gsoultan/mimir/optim"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/vision"

	"github.com/gsoultan/mimir-server/internal/schema"
)

func init() {
	builtin = append(builtin, Architecture{
		Name:        "find-things",
		Title:       "Find things in photos",
		Description: "Finds each thing in a photo and draws a box around it, as CenterNet does: a ResNet scores where the centre of each kind of thing lies and how big it is. It learns from photos and a table of the boxes around what is in them.",
		Input:       Scene, Output: Boxes, Learner: finder,
	})
}

// Boxed is photos and where the things in them are, for a Boxes learner.
type Boxed struct {
	// Pixels holds schema.ScenePixels bytes a photo (see
	// schema.DecodeScene), and Truth each photo's boxes in those pixels.
	Pixels []uint8
	Truth  [][]vision.Detection
	Labels []string // what each class is
	// Held is how many of the photos, the last, are kept aside to score
	// the model on.
	Held int
}

// Photos returns how many photos b holds.
func (b Boxed) Photos() int { return len(b.Truth) }

// FindTop is the most things a finder answers a photo, and findBatch the
// photos it learns from at a time.
const (
	FindTop   = 20
	findBatch = 8
	findWidth = 0.25
)

// FindShape sizes a Find.
type FindShape struct {
	Classes int     `json:"classes"`
	Width   float64 `json:"width"` // the ResNet's width
}

func newFind(s FindShape) (*Find, error) {
	d, err := vision.NewDetector(s.Classes, vision.ResNet18(1, vision.Width(s.Width)).Backbone())
	if err != nil {
		return nil, err
	}
	return &Find{Det: d, shape: s}, nil
}

// finder learns to find things in photos, CenterNet's way, on a ResNet-18
// of a quarter the usual width.
var finder = &Learner{
	Boxes: func(ctx context.Context, b Boxed, epochs int, progress func(int, int)) (Learned, error) {
		k := len(b.Labels)
		if k < 1 {
			return Learned{}, cellsError("no box says what it holds")
		}
		n := b.Photos()
		train := n - b.Held
		if train < 1 {
			return Learned{}, cellsError("no photos to learn from")
		}
		shape := FindShape{Classes: k, Width: findWidth}
		f, err := newFind(shape)
		if err != nil {
			return Learned{}, err
		}
		opt := optim.Adam(nn.Parameters(f.Det), optim.LR(1e-3), optim.ClipNorm(10))
		r := rand.New(rand.NewPCG(11, uint64(n)))
		order := make([]int, train)
		for i := range order {
			order[i] = i
		}
		perEpoch := (train + findBatch - 1) / findBatch
		total := max(epochs, 1) * perEpoch
		done := 0
		for range max(epochs, 1) {
			r.Shuffle(train, func(i, j int) { order[i], order[j] = order[j], order[i] })
			for lo := 0; lo < train; lo += findBatch {
				if err := ctx.Err(); err != nil {
					return Learned{}, err
				}
				batch := order[lo:min(lo+findBatch, train)]
				x, truth := b.batch(batch, r)
				tg, err := f.Det.Targets(truth, schema.SceneSize, schema.SceneSize)
				if err != nil {
					return Learned{}, err
				}
				l := f.Det.Loss(f.Det.Forward(scene(x)), tg)
				if err := l.Backward(ctx); err != nil {
					return Learned{}, err
				}
				if err := opt.Step(ctx); err != nil {
					return Learned{}, err
				}
				opt.ZeroGrad()
				done++
				if progress != nil {
					progress(done, total)
				}
			}
		}
		nn.Eval(f.Det)
		var acc float64
		if b.Held > 0 {
			var found, truth [][]vision.Detection
			for lo := train; lo < n; lo += findBatch {
				idx := make([]int, 0, findBatch)
				for i := lo; i < min(lo+findBatch, n); i++ {
					idx = append(idx, i)
				}
				x, t := b.batch(idx, nil)
				got, err := f.Det.Detect(ctx, f.Det.Forward(scene(x)), 0.05, FindTop)
				if err != nil {
					return Learned{}, err
				}
				found, truth = append(found, got...), append(truth, t...)
			}
			acc = vision.AveragePrecision(found, truth, k, 0.5)
		}
		s, err := shapeJSON(shape)
		return Learned{Module: f, Shape: s, Labels: b.Labels, Accuracy: acc}, err
	},
	Build: func(shape string) (nn.Module, error) {
		var s FindShape
		if err := json.Unmarshal([]byte(shape), &s); err != nil {
			return nil, fmt.Errorf("a finder's shape: %w", err)
		}
		return newFind(s)
	},
}

// batch returns photos idx as [len(idx), 3, SceneSize, SceneSize] from 0
// to 1, and their boxes. With r, each photo is flipped left to right at
// random, so the model learns from twice the photos.
func (b Boxed) batch(idx []int, r *rand.Rand) (*tensor.Tensor, [][]vision.Detection) {
	const side, plane = schema.SceneSize, schema.SceneSize * schema.SceneSize
	x := make([]float32, 0, len(idx)*schema.ScenePixels)
	truth := make([][]vision.Detection, len(idx))
	for j, i := range idx {
		px := b.Pixels[i*schema.ScenePixels : (i+1)*schema.ScenePixels]
		flip := r != nil && r.IntN(2) == 1
		if !flip {
			x = schema.Floats(x, px)
			truth[j] = b.Truth[i]
			continue
		}
		for c := range 3 {
			for y := range side {
				row := px[c*plane+y*side : c*plane+(y+1)*side]
				for xx := side - 1; xx >= 0; xx-- {
					x = append(x, float32(row[xx])/255)
				}
			}
		}
		for _, d := range b.Truth[i] {
			d.X1, d.X2 = side-d.X2, side-d.X1
			truth[j] = append(truth[j], d)
		}
	}
	return tensor.FromSlice(x, len(idx), 3, side, side), truth
}

// scene centres photo pixels from 0 to 1 as PhotoModel does.
func scene(x *tensor.Tensor) *tensor.Tensor {
	return x.Sub(tensor.Scalar[float32](0.5)).Mul(tensor.Scalar[float32](4))
}

// Find is a finder's model: Forward maps photos [N, 3, SceneSize,
// SceneSize], from 0 to 1, to [N, FindTop·6]: for each of the FindTop
// likeliest things, its score from 0 to 1, its class, and its box's left,
// top, width and height as shares of the photo, surest first.
type Find struct {
	Det   *vision.Detector
	shape FindShape
}

// Forward finds the peaks of the centre scores, the best class of each
// cell, and reads their boxes, all as tensor operations so the model
// runs on any device.
func (f *Find) Forward(x *tensor.Tensor) *tensor.Tensor {
	if err := x.Err(); err != nil {
		return x
	}
	out := f.Det.Forward(scene(x))
	if err := out.Err(); err != nil {
		return out
	}
	s := out.Shape()
	n, k, gh, gw := s[0], f.shape.Classes, s[2], s[3]
	plane := gh * gw
	part := func(lo, hi int) *tensor.Tensor {
		return out.Shrink([][2]int{{0, n}, {lo, hi}, {0, gh}, {0, gw}})
	}
	heat := part(0, k).Sigmoid()
	peak := heat.MaxPool2d(3, tensor.ConvOptions{Stride: 1, Padding: 1})
	kept := tensor.Where(heat.Equal(peak), heat, tensor.Zeros(tensor.Float32)).Reshape(n, k, plane)
	best := kept.Max(1)                          // [n, plane]
	class := kept.Argmax(1).Cast(tensor.Float32) // [n, plane]
	top := min(FindTop, plane)
	score, cell := best.TopK(top, 1, true) // [n, top]
	cell = cell.Cast(tensor.Int32)
	gather := func(v *tensor.Tensor) *tensor.Tensor { return v.Gather(1, cell) }
	xs, ys := make([]float32, plane), make([]float32, plane)
	for i := range plane {
		xs[i], ys[i] = float32(i%gw), float32(i/gw)
	}
	rest := part(k, k+4).Reshape(n, 4, plane)
	ch := func(c int) *tensor.Tensor {
		return gather(rest.Shrink([][2]int{{0, n}, {c, c + 1}, {0, plane}}).Reshape(n, plane))
	}
	zero := tensor.Scalar[float32](0)
	w, h := ch(0).Maximum(zero), ch(1).Maximum(zero)
	cx := gather(tensor.FromSlice(xs, 1, plane).Expand(n, plane)).Add(ch(2))
	cy := gather(tensor.FromSlice(ys, 1, plane).Expand(n, plane)).Add(ch(3))
	half := tensor.Scalar[float32](0.5)
	gwf, ghf := tensor.Scalar(float32(gw)), tensor.Scalar(float32(gh))
	cols := []*tensor.Tensor{
		score, gather(class),
		cx.Sub(w.Mul(half)).Div(gwf), cy.Sub(h.Mul(half)).Div(ghf),
		w.Div(gwf), h.Div(ghf),
	}
	for i, c := range cols {
		cols[i] = c.Reshape(n, top, 1)
	}
	res := tensor.Cat(2, cols...).Reshape(n, top*6)
	if top < FindTop {
		res = tensor.Cat(1, res, tensor.Zeros(tensor.Float32, n, (FindTop-top)*6))
	}
	return res
}

// Found is one thing a finder found.
type Found struct {
	Label                    string
	Score                    float32
	Left, Top, Width, Height float32 // shares of the photo
}

// FindThreshold is the least score a thing found is answered with.
const FindThreshold = 0.3

// FoundIn reads a Find's output for one photo: the things scoring at
// least FindThreshold, surest first, their boxes kept within the photo.
func FoundIn(out []float32, labels []string) []Found {
	var res []Found
	for i := 0; i+6 <= len(out); i += 6 {
		v := out[i : i+6]
		c := int(v[1])
		if v[0] < FindThreshold || c < 0 || c >= len(labels) {
			continue
		}
		l, t := max(v[2], 0), max(v[3], 0)
		r, b := min(v[2]+v[4], 1), min(v[3]+v[5], 1)
		if r <= l || b <= t {
			continue
		}
		res = append(res, Found{Label: labels[c], Score: v[0], Left: l, Top: t, Width: r - l, Height: b - t})
	}
	slices.SortStableFunc(res, func(a, b Found) int {
		switch {
		case a.Score > b.Score:
			return -1
		case a.Score < b.Score:
			return 1
		}
		return 0
	})
	return res
}
