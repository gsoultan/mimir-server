package training

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"testing"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// squares writes a ZIP of n photos, a bright square in the given place of
// each, in a folder named after the place.
func squares(n int, places ...string) []byte {
	r := rand.New(rand.NewPCG(5, 6))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := range n {
		place := places[i%len(places)]
		img := image.NewRGBA(image.Rect(0, 0, 48, 48))
		for k := range img.Pix {
			img.Pix[k] = uint8(r.IntN(100))
		}
		x, y := r.IntN(30), r.IntN(8)
		switch place {
		case "bottom":
			y += 30
		case "middle":
			y += 15
		}
		for dy := range 10 {
			for dx := range 18 {
				img.Set(x+dx, y+dy, color.RGBA{230, 230, 60, 255})
			}
		}
		f, _ := z.Create(fmt.Sprintf("%s/%04d.png", place, i))
		png.Encode(f, img)
	}
	z.Close()
	return b.Bytes()
}

func TestPhotoNetsTrainAndTransfer(t *testing.T) {
	if testing.Short() {
		t.Skip("trains two photo networks")
	}
	ctx := context.Background()
	s := store.Settings{Epochs: 1, BatchSize: 16, LearningRate: 1e-3, HoldOut: 0.25}
	for _, name := range []string{"photo-mobilenet", "photo-resnet"} {
		t.Run(name, func(t *testing.T) {
			a, ok := arch.Lookup(name)
			if !ok {
				t.Fatalf("no architecture %q", name)
			}
			two := Source{Kind: store.DatasetPhotos, CSV: squares(32, "top", "bottom")}
			base, err := Fit(ctx, Task{Arch: a, Source: two, Settings: s}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(base.Spec.Labels) != 2 || len(base.History) != 1 {
				t.Fatalf("labels %v, %d epochs", base.Spec.Labels, len(base.History))
			}

			// A model of three answers starts from the two-answer one.
			three := Source{Kind: store.DatasetPhotos, CSV: squares(36, "top", "middle", "bottom")}
			task := Task{Arch: a, Source: three, Settings: s, BaseWeights: base.Weights, BaseLabels: base.Spec.Labels, BaseFeatures: base.Features}
			res, err := Fit(ctx, task, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.Fresh || len(res.Spec.Labels) != 3 {
				t.Fatalf("fresh %v, labels %v: want a transfer to three answers", res.Fresh, res.Spec.Labels)
			}
			mod, err := runtime.Build(a, res.Spec, res.Weights)
			if err != nil {
				t.Fatal(err)
			}
			x := tensor.Zeros(tensor.Float32, 2, 3, schema.PhotoSize, schema.PhotoSize)
			out, err := mod.Forward(x).Data[float32](ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != 6 {
				t.Errorf("%d answers for 2 photos of 3 labels", len(out))
			}
		})
	}
}

func TestTransferNeedsAHead(t *testing.T) {
	ctx := context.Background()
	a, ok := arch.Lookup("table-mlp")
	if !ok {
		t.Fatal("no table-mlp")
	}
	if _, ok := a.New(arch.Spec{InputShape: []int{3}, Labels: []string{"a", "b"}}).(arch.Headed); ok {
		t.Skip("table-mlp can take a new head")
	}
	s := store.Settings{Epochs: 1, BatchSize: 64, LearningRate: 1e-3, HoldOut: 0.2}
	csv := dropColumn(learnedTable(300, false), "amount")
	base, err := Fit(ctx, Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv, Target: "label"}, Settings: s}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Other answers and no head to replace: it learns from scratch.
	res, err := Fit(ctx, Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv, Target: "label"}, Settings: s,
		BaseWeights: base.Weights, BaseLabels: []string{"x", "y", "z"}, BaseFeatures: base.Features}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fresh {
		t.Error("a table model with other answers didn't start fresh")
	}
}
