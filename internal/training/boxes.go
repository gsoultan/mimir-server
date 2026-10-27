package training

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/vision"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// MaxBoxPhotos bounds the photos a finder learns from.
const MaxBoxPhotos = 5000

// BoxTable is the table of a ZIP of photos and boxes: one row per thing
// found, naming its photo, what it is and its box in the photo's pixels.
// A row with nothing in the what column is a photo with nothing in it.
type BoxTable struct {
	Photos  []string            // the photos named, as found in the ZIP
	Boxes   map[string][]RawBox // by photo
	Labels  []string            // every kind of thing, sorted
	Missing []string            // photos named but not in the ZIP
	Things  int
}

// RawBox is one row of a BoxTable.
type RawBox struct {
	Label          string
	X1, Y1, X2, Y2 float64
}

// The names a box table's columns go by, lower case.
var boxColumns = map[string][]string{
	"photo":  {"photo", "image", "file", "filename", "path", "picture"},
	"what":   {"what", "label", "class", "kind", "category", "thing"},
	"left":   {"left", "x", "xmin", "x_min", "x1"},
	"top":    {"top", "y", "ymin", "y_min", "y1"},
	"width":  {"width", "w"},
	"height": {"height", "h"},
	"right":  {"right", "xmax", "x_max", "x2"},
	"bottom": {"bottom", "ymax", "y_max", "y2"},
}

// ReadBoxes reads the table of a ZIP of photos and boxes.
func ReadBoxes(arc *Archive) (BoxTable, error) {
	name, b, err := arc.Table()
	if err != nil {
		return BoxTable{}, fmt.Errorf("%w: the ZIP needs a boxes.csv saying where each thing is", ErrBadTable)
	}
	r := csv.NewReader(bytes.NewReader(b))
	r.TrimLeadingSpace, r.FieldsPerRecord = true, -1
	header, err := r.Read()
	if err != nil {
		return BoxTable{}, fmt.Errorf("%w: %s is empty", ErrBadTable, name)
	}
	col := map[string]int{}
	for role, names := range boxColumns {
		col[role] = slices.IndexFunc(header, func(h string) bool {
			return slices.Contains(names, strings.ToLower(strings.TrimSpace(h)))
		})
	}
	sized := col["width"] >= 0 && col["height"] >= 0
	cornered := col["right"] >= 0 && col["bottom"] >= 0
	if col["photo"] < 0 || col["what"] < 0 || col["left"] < 0 || col["top"] < 0 || !sized && !cornered {
		return BoxTable{}, fmt.Errorf("%w: %s needs the columns photo, what, left, top, width and height", ErrBadTable, name)
	}
	t := BoxTable{Boxes: map[string][]RawBox{}}
	labels := map[string]bool{}
	missing := map[string]bool{}
	for line := 2; ; line++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return BoxTable{}, fmt.Errorf("%w: line %d of %s: %v", ErrBadTable, line, name, err)
		}
		cell := func(role string) string {
			if j := col[role]; j >= 0 && j < len(row) {
				return strings.TrimSpace(row[j])
			}
			return ""
		}
		p := arc.Find(name, cell("photo"))
		if p == "" {
			if c := cell("photo"); c != "" && !missing[c] {
				missing[c] = true
				t.Missing = append(t.Missing, c)
			}
			continue
		}
		if _, ok := t.Boxes[p]; !ok {
			if len(t.Photos) >= MaxBoxPhotos {
				return BoxTable{}, fmt.Errorf("%w: at most %d photos", ErrBadTable, MaxBoxPhotos)
			}
			t.Photos = append(t.Photos, p)
			t.Boxes[p] = nil
		}
		what := cell("what")
		if what == "" {
			continue
		}
		num := func(role string) (float64, bool) {
			v, ok := schema.ParseNumber(cell(role))
			return float64(v), ok
		}
		x, okx := num("left")
		y, oky := num("top")
		var x2, y2 float64
		var ok2 bool
		if sized {
			w, okw := num("width")
			h, okh := num("height")
			x2, y2, ok2 = x+w, y+h, okw && okh
		} else {
			var okr, okb bool
			x2, okr = num("right")
			y2, okb = num("bottom")
			ok2 = okr && okb
		}
		if !okx || !oky || !ok2 || x2 <= x || y2 <= y {
			return BoxTable{}, fmt.Errorf("%w: line %d of %s doesn't say where the box is: left and top, then a width and height above 0", ErrBadTable, line, name)
		}
		t.Boxes[p] = append(t.Boxes[p], RawBox{Label: what, X1: x, Y1: y, X2: x2, Y2: y2})
		labels[what] = true
		t.Things++
	}
	for l := range labels {
		t.Labels = append(t.Labels, l)
	}
	slices.Sort(t.Labels)
	switch {
	case len(t.Photos) == 0:
		return BoxTable{}, fmt.Errorf("%w: no row of %s names a photo in the ZIP", ErrBadTable, name)
	case t.Things == 0:
		return BoxTable{}, fmt.Errorf("%w: %s has no boxes; each row says what a thing is and where", ErrBadTable, name)
	case len(t.Labels) > MaxClasses:
		return BoxTable{}, fmt.Errorf("%w: at most %d kinds of thing", ErrBadTable, MaxClasses)
	}
	return t, nil
}

// loadBoxed reads a ZIP of photos and boxes for a finder, shuffled, with
// holdOut of the photos last, kept aside.
func loadBoxed(ctx context.Context, src Source, holdOut float64, seed uint64) (arch.Boxed, int, error) {
	arc, err := OpenArchive(src.CSV)
	if err != nil {
		return arch.Boxed{}, 0, err
	}
	t, err := ReadBoxes(arc)
	if err != nil {
		return arch.Boxed{}, 0, err
	}
	class := map[string]int{}
	for i, l := range t.Labels {
		class[l] = i
	}
	photos := slices.Clone(t.Photos)
	rand.New(rand.NewPCG(seed, 0x626f78)).Shuffle(len(photos), func(i, j int) { photos[i], photos[j] = photos[j], photos[i] })
	b := arch.Boxed{Labels: t.Labels}
	skipped := 0
	for _, p := range photos {
		if err := ctx.Err(); err != nil {
			return arch.Boxed{}, 0, err
		}
		raw, err := arc.read(p, schema.MaxPhotoBytes)
		if err != nil {
			skipped++
			continue
		}
		px, w, h, err := schema.DecodeScene(raw)
		if err != nil || w == 0 || h == 0 {
			skipped++
			continue
		}
		sx, sy := float64(schema.SceneSize)/float64(w), float64(schema.SceneSize)/float64(h)
		var truth []vision.Detection
		for _, r := range t.Boxes[p] {
			truth = append(truth, vision.Detection{Class: class[r.Label], Box: vision.Box{X1: r.X1 * sx, Y1: r.Y1 * sy, X2: r.X2 * sx, Y2: r.Y2 * sy}})
		}
		b.Pixels = append(b.Pixels, px...)
		b.Truth = append(b.Truth, truth)
	}
	if n := b.Photos(); n < MinRows {
		return arch.Boxed{}, 0, fmt.Errorf("%w: only %d photos could be read; at least %d are needed", ErrBadTable, n, MinRows)
	}
	b.Held = int(float64(b.Photos())*holdOut + 0.5)
	return b, skipped, nil
}

// findEpochs is how many passes a finder makes over n photos: about
// 6,000 photos seen in all, between 10 and 60 passes.
func findEpochs(n int) int { return min(max((6000+n-1)/max(n, 1), 10), 60) }

// learnBoxes fits a Learner that finds things in photos.
func learnBoxes(ctx context.Context, t Task, progress Progress) (Result, error) {
	if t.Source.Kind != store.DatasetBoxes {
		return Result{}, fmt.Errorf("%w: %s learns from photos and their boxes; upload a ZIP of the photos and a boxes.csv", ErrBadTable, t.Arch.Title)
	}
	b, _, err := loadBoxed(ctx, t.Source, t.Settings.HoldOut, 42)
	if err != nil {
		return Result{}, err
	}
	start, last := time.Now(), time.Time{}
	progress(0, 0, 1, nil)
	got, err := t.Arch.Learner.Boxes(ctx, b, findEpochs(b.Photos()-b.Held), func(done, total int) {
		if time.Since(last) > time.Second {
			last = time.Now()
			progress(0, done, total, nil)
		}
	})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, err
	}
	history := []store.Epoch{{Epoch: 1, Millis: time.Since(start).Milliseconds(), Accuracy: got.Accuracy}}
	progress(1, 0, 1, history)
	var buf bytes.Buffer
	if err := mim.Save(ctx, &buf, got.Module, &mim.SaveOptions{Metadata: map[string]any{arch.ShapeKey: got.Shape}}); err != nil {
		return Result{}, fmt.Errorf("couldn't save the model: %w", err)
	}
	return Result{
		Weights: buf.Bytes(), History: history,
		Spec: arch.Spec{InputShape: []int{3, schema.SceneSize, schema.SceneSize}, Labels: got.Labels},
	}, nil
}
