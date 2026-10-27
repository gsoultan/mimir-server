package training

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/store"
)

// scene draws a photo of w by h with one or two patches, yellow or blue,
// and returns it as a PNG with the boxes around them.
func scene(r *rand.Rand, w, h int) ([]byte, []RawBox) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for k := range img.Pix {
		img.Pix[k] = uint8(40 + r.IntN(40))
	}
	var boxes []RawBox
	for range 1 + r.IntN(2) {
		label, c := "yellow", color.RGBA{230, 220, 40, 255}
		if r.IntN(2) == 1 {
			label, c = "blue", color.RGBA{40, 80, 230, 255}
		}
		bw, bh := w/6+r.IntN(w/5), h/6+r.IntN(h/5)
		x, y := r.IntN(w-bw), r.IntN(h-bh)
		clash := false
		for _, b := range boxes {
			if float64(x) < b.X2 && float64(x+bw) > b.X1 && float64(y) < b.Y2 && float64(y+bh) > b.Y1 {
				clash = true
			}
		}
		if clash {
			continue
		}
		for yy := y; yy < y+bh; yy++ {
			for xx := x; xx < x+bw; xx++ {
				img.Set(xx, yy, c)
			}
		}
		boxes = append(boxes, RawBox{Label: label, X1: float64(x), Y1: float64(y), X2: float64(x + bw), Y2: float64(y + bh)})
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes(), boxes
}

// scenes writes a ZIP of n photos and their boxes.csv.
func scenes(n int) []byte {
	r := rand.New(rand.NewPCG(9, 10))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	var table strings.Builder
	table.WriteString("photo,what,left,top,width,height\n")
	for i := range n {
		name := fmt.Sprintf("photos/%04d.png", i)
		px, boxes := scene(r, 160+r.IntN(80), 120+r.IntN(60))
		f, _ := z.Create("shapes/" + name)
		f.Write(px)
		for _, bx := range boxes {
			fmt.Fprintf(&table, "%s,%s,%g,%g,%g,%g\n", name, bx.Label, bx.X1, bx.Y1, bx.X2-bx.X1, bx.Y2-bx.Y1)
		}
	}
	f, _ := z.Create("shapes/boxes.csv")
	f.Write([]byte(table.String()))
	z.Close()
	return b.Bytes()
}

func TestReadBoxes(t *testing.T) {
	arc, err := OpenArchive(scenes(5))
	if err != nil {
		t.Fatal(err)
	}
	bt, err := ReadBoxes(arc)
	if err != nil {
		t.Fatal(err)
	}
	if len(bt.Photos) != 5 || bt.Things < 5 || len(bt.Labels) == 0 {
		t.Errorf("%d photos, %d things, labels %v", len(bt.Photos), bt.Things, bt.Labels)
	}
}

func TestFindThings(t *testing.T) {
	if testing.Short() {
		t.Skip("trains a finder")
	}
	ctx := context.Background()
	a, ok := arch.Lookup("find-things")
	if !ok {
		t.Fatal("no find-things architecture")
	}
	if got := Architectures(store.Dataset{Kind: store.DatasetBoxes}); len(got) != 1 || got[0] != "find-things" {
		t.Fatalf("photos with boxes offer %v", got)
	}
	src := Source{Kind: store.DatasetBoxes, CSV: scenes(120)}
	res, err := Fit(ctx, Task{Arch: a, Source: src, Settings: store.Settings{HoldOut: 0.2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Spec.Labels; len(got) != 2 || got[0] != "blue" || got[1] != "yellow" {
		t.Fatalf("labels %v", got)
	}
	ap := res.History[0].Accuracy
	t.Logf("held-out mAP@0.5 %.2f", ap)
	if ap < 0.6 {
		t.Errorf("held-out mAP@0.5 %.2f, want at least 0.6", ap)
	}

	// A new photo, of its own size, answers with boxes as shares of it.
	cache := runtime.NewCache(1 << 30)
	load := func() ([]byte, error) { return res.Weights, nil }
	r := rand.New(rand.NewPCG(3, 4))
	hit := 0
	for range 10 {
		w, h := 300, 200
		px, truth := scene(r, w, h)
		in, err := ask.Encode(a, nil, nil, nil, px, nil)
		if err != nil {
			t.Fatal(err)
		}
		out, err := cache.Predict(ctx, "m", "1", a, res.Spec, load, in)
		if err != nil {
			t.Fatal(err)
		}
		found := arch.FoundIn(out.Output, res.Spec.Labels)
		if len(found) == 0 {
			continue
		}
		f := found[0]
		best := 0.0
		for _, b2 := range truth {
			if f.Label != b2.Label {
				continue
			}
			l, tp := float64(f.Left)*float64(w), float64(f.Top)*float64(h)
			rr, bb := l+float64(f.Width)*float64(w), tp+float64(f.Height)*float64(h)
			iw, ih := math.Min(rr, b2.X2)-math.Max(l, b2.X1), math.Min(bb, b2.Y2)-math.Max(tp, b2.Y1)
			if iw > 0 && ih > 0 {
				inter := iw * ih
				best = math.Max(best, inter/((rr-l)*(bb-tp)+(b2.X2-b2.X1)*(b2.Y2-b2.Y1)-inter))
			}
		}
		if best >= 0.5 {
			hit++
		}
	}
	if hit < 7 {
		t.Errorf("the surest box matched a true one in %d of 10 new photos, want 7", hit)
	}
}
