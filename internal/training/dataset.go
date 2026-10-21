// Package training teaches models from datasets, one job at a time.
package training

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"iter"
	"math"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/gsoultan/mimir/data"
	"github.com/gsoultan/mimir/data/datasets"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// ErrBadTable reports examples the server can't learn from.
var ErrBadTable = errors.New("training: bad table")

// Limits on uploaded tables.
const (
	MaxRows     = 1_000_000
	MaxFeatures = 1000
	MaxClasses  = 1000
	MinRows     = 10
	// MaxTableBytes bounds one table's CSV.
	MaxTableBytes = 64 << 20
)

// Table is a parsed CSV: input columns of any kind, and one answer column.
type Table struct {
	Header   []string        // every column, as written
	Target   string          // the answer column
	Columns  []schema.Column // the input columns, in order
	Features []string        // names each number a row becomes
	// Classes are the answer's values in order, or nil when there are
	// more than MaxClasses of them.
	Classes []string
	// Numeric says every answer is a number, so a number model can learn
	// it.
	Numeric bool
	X       []float32 // rows × len(Features)
	Y       []int32   // class index per row, when Classes is set
	V       []float32 // the answer as a number, when Numeric
	Photos  []string  // the photo column's cell per row, if it has one
	Rows    int
	// Profile says how full and readable each input column is.
	Profile []Profile
	// Answers counts each answer value, up to MaxClasses of them.
	Answers map[string]int
	// Skipped counts rows left out because their answer was empty.
	Skipped int
}

// Profile describes one input column's cells.
type Profile struct {
	Empty      int     // cells with nothing in them
	Unreadable int     // cells of a number column that aren't numbers
	Words      float64 // words per cell, for a text column
}

// ParseTable reads a CSV with a header row. target names the answer
// column; empty means the last column. hints, when set, give a column's
// kind instead of guessing it from its cells.
func ParseTable(r io.Reader, target string, hints map[string]schema.Kind) (*Table, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: the first row must name the columns", ErrBadTable)
	}
	header = slices.Clone(header)
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	if len(header) < 2 {
		return nil, fmt.Errorf("%w: a table needs at least one column to read and one answer column", ErrBadTable)
	}
	if len(header) > MaxFeatures+1 {
		return nil, fmt.Errorf("%w: at most %d columns", ErrBadTable, MaxFeatures+1)
	}
	seen := map[string]bool{}
	for _, h := range header {
		if h == "" {
			return nil, fmt.Errorf("%w: every column needs a name in the first row", ErrBadTable)
		}
		if seen[h] {
			return nil, fmt.Errorf("%w: two columns are called %q", ErrBadTable, h)
		}
		seen[h] = true
	}
	ti := len(header) - 1
	if target != "" {
		ti = slices.Index(header, target)
		if ti < 0 {
			return nil, fmt.Errorf("%w: there is no column called %q", ErrBadTable, target)
		}
	}
	t := &Table{Header: header, Target: header[ti]}
	inputs := slices.Delete(slices.Clone(header), ti, ti+1)
	cells := make([][]string, len(inputs))
	var answers []string
	skipped := 0
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrBadTable, line, err)
		}
		if len(answers) >= MaxRows {
			return nil, fmt.Errorf("%w: at most %d rows", ErrBadTable, MaxRows)
		}
		answer := strings.TrimSpace(rec[ti])
		if answer == "" {
			skipped++
			continue
		}
		answers = append(answers, answer)
		j := 0
		for i, field := range rec {
			if i != ti {
				cells[j] = append(cells[j], strings.TrimSpace(field))
				j++
			}
		}
	}
	t.Rows, t.Skipped = len(answers), skipped
	if t.Rows < MinRows {
		return nil, fmt.Errorf("%w: at least %d rows with an answer are needed to learn anything", ErrBadTable, MinRows)
	}

	t.Columns = schema.Detect(inputs, cells, hints)
	if photos := slices.IndexFunc(t.Columns, func(c schema.Column) bool { return c.Kind == schema.Photo }); photos >= 0 {
		t.Photos = cells[photos]
		if slices.ContainsFunc(t.Columns[photos+1:], func(c schema.Column) bool { return c.Kind == schema.Photo }) {
			return nil, fmt.Errorf("%w: only one photo column per row works here", ErrBadTable)
		}
	}
	t.Features = schema.Features(t.Columns)
	if len(t.Features) > schema.MaxWidth {
		return nil, fmt.Errorf("%w: the columns make %d numbers per row; at most %d. Leave out some text or category columns", ErrBadTable, len(t.Features), schema.MaxWidth)
	}
	t.Profile = profile(t.Columns, cells)

	// The answer: a class, a number, or both.
	t.Answers = map[string]int{}
	t.Numeric = true
	for _, a := range answers {
		if t.Numeric {
			if _, ok := schema.ParseNumber(a); !ok {
				t.Numeric = false
			}
		}
		if _, ok := t.Answers[a]; ok || len(t.Answers) <= MaxClasses {
			t.Answers[a]++
		}
	}
	if len(t.Answers) < 2 {
		return nil, fmt.Errorf("%w: the %q column has only one value, so there is nothing to tell apart", ErrBadTable, t.Target)
	}
	if len(t.Answers) <= MaxClasses {
		t.Classes = slices.Sorted(func(yield func(string) bool) {
			for a := range t.Answers {
				if !yield(a) {
					return
				}
			}
		})
		t.Y = make([]int32, t.Rows)
		for i, a := range answers {
			c, _ := slices.BinarySearch(t.Classes, a)
			t.Y[i] = int32(c)
		}
	}
	if !t.Numeric && t.Classes == nil {
		return nil, fmt.Errorf("%w: the %q column has more than %d different values; for a category answer, at most %d", ErrBadTable, t.Target, MaxClasses, MaxClasses)
	}
	if t.Numeric {
		t.V = make([]float32, t.Rows)
		for i, a := range answers {
			f, _ := schema.ParseNumber(a)
			t.V[i] = float32(f)
		}
	}

	t.X = make([]float32, 0, t.Rows*len(t.Features))
	for i := range t.Rows {
		t.X = schema.Encode(t.X, t.Columns, func(j int) string { return cells[j][i] })
	}
	return t, nil
}

// profile counts the empty and unreadable cells of each column.
func profile(cols []schema.Column, cells [][]string) []Profile {
	out := make([]Profile, len(cols))
	for j, c := range cols {
		words := 0
		for _, v := range cells[j] {
			switch {
			case v == "":
				out[j].Empty++
			case c.Kind == schema.Number:
				if _, ok := schema.ParseNumber(v); !ok {
					out[j].Unreadable++
				}
			case c.Kind == schema.Text:
				words += len(schema.Words(v))
			}
		}
		if n := len(cells[j]) - out[j].Empty; c.Kind == schema.Text && n > 0 {
			out[j].Words = float64(words) / float64(n)
		}
	}
	return out
}

// keep narrows the table to the given rows, in order.
func (t *Table) keep(rows []int) {
	f := len(t.Features)
	x := make([]float32, 0, len(rows)*f)
	var y []int32
	var v []float32
	var photos []string
	for _, i := range rows {
		x = append(x, t.X[i*f:(i+1)*f]...)
		if t.Y != nil {
			y = append(y, t.Y[i])
		}
		if t.V != nil {
			v = append(v, t.V[i])
		}
		if t.Photos != nil {
			photos = append(photos, t.Photos[i])
		}
	}
	t.X, t.Y, t.V, t.Photos, t.Rows = x, y, v, photos, len(rows)
}

// Sample returns the header and the first n rows of a CSV, as text.
func Sample(b []byte, n int) [][]string {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1
	var out [][]string
	for len(out) < n+1 {
		rec, err := cr.Read()
		if err != nil {
			break
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
		}
		out = append(out, rec)
	}
	return out
}

// Stats returns the mean and standard deviation of each feature over the
// whole table. A constant column gets a deviation of 1 so it divides safely.
func (t *Table) Stats() (mean, std []float32) {
	return stats(t.X, len(t.Features), t.Rows)
}

func stats(x []float32, f, rows int) (mean, std []float32) {
	mean, std = make([]float32, f), make([]float32, f)
	if rows == 0 {
		for i := range std {
			std[i] = 1
		}
		return mean, std
	}
	sum := make([]float64, f)
	for i := range rows {
		for j := range f {
			sum[j] += float64(x[i*f+j])
		}
	}
	for j := range f {
		mean[j] = float32(sum[j] / float64(rows))
	}
	sq := make([]float64, f)
	for i := range rows {
		for j := range f {
			d := float64(x[i*f+j]) - float64(mean[j])
			sq[j] += d * d
		}
	}
	for j := range f {
		s := math.Sqrt(sq[j] / float64(rows))
		if s < 1e-6 {
			s = 1
		}
		std[j] = float32(s)
	}
	return mean, std
}

// Describe fills in what a dataset row says about a parsed table.
func (t *Table) Describe(d *store.Dataset) {
	d.Rows, d.Features, d.Labels, d.Target = int64(t.Rows), t.Features, t.Classes, t.Target
	d.InputShape = []int{len(t.Features)}
	if schema.PhotoColumn(t.Columns) >= 0 {
		d.InputShape = []int{schema.PhotoPixels + len(t.Features)}
	}
	d.Columns, d.NumberTarget, d.Skipped = t.Columns, t.Numeric, int64(t.Skipped)
}

// Set is a dataset a model learns from or is scored on.
type Set interface {
	data.Dataset
	Batches() int
}

// examples is a Set that builds each batch when it is needed, so photos
// stay as bytes until a batch of them is read.
type examples struct {
	rows  []int // which examples, by index into the source
	shape []int // one example's input shape
	input func(dst []float32, i int) []float32
	cls   []int32   // the class of each source example, or
	num   []float32 // its answer as a number
	batch int
	seed  uint64
	epoch atomic.Int64
}

// Len returns the number of examples.
func (e *examples) Len() int { return len(e.rows) }

// Batches returns the number of batches in an epoch.
func (e *examples) Batches() int { return (len(e.rows) + e.batch - 1) / e.batch }

// SetEpoch sets the epoch the next call to All visits, which decides its
// order, so a resumed run sees the same order an unbroken one would.
func (e *examples) SetEpoch(epoch int) { e.epoch.Store(int64(epoch)) }

// All yields the batches of the next epoch, in a new shuffled order.
func (e *examples) All(ctx context.Context) iter.Seq2[data.Batch, error] {
	epoch := e.epoch.Add(1) - 1
	return func(yield func(data.Batch, error) bool) {
		order := rand.New(rand.NewPCG(e.seed, uint64(epoch))).Perm(len(e.rows))
		width := 1
		for _, d := range e.shape {
			width *= d
		}
		for b := range e.Batches() {
			if err := ctx.Err(); err != nil {
				yield(data.Batch{}, err)
				return
			}
			idx := order[b*e.batch : min((b+1)*e.batch, len(order))]
			x := make([]float32, 0, len(idx)*width)
			for _, k := range idx {
				x = e.input(x, e.rows[k])
			}
			batch := data.Batch{X: tensor.FromSlice(x, append([]int{len(idx)}, e.shape...)...)}
			if e.cls != nil {
				y := make([]int32, len(idx))
				for i, k := range idx {
					y[i] = e.cls[e.rows[k]]
				}
				batch.Y = tensor.FromSlice(y)
			} else {
				y := make([]float32, len(idx))
				for i, k := range idx {
					y[i] = e.num[e.rows[k]]
				}
				batch.Y = tensor.FromSlice(y, len(idx), 1)
			}
			if !yield(batch, nil) {
				return
			}
		}
	}
}

// split shuffles n examples and keeps holdOut of them aside for scoring.
// test is nil when nothing is held out.
func split(n int, holdOut float64, seed uint64, batch int, proto *examples) (train, test Set, err error) {
	perm := rand.New(rand.NewPCG(seed, seed)).Perm(n)
	nTest := int(float64(n) * holdOut)
	nTrain := n - nTest
	if nTrain < 1 {
		return nil, nil, fmt.Errorf("%w: nothing left to learn from", ErrBadTable)
	}
	build := func(rows []int) *examples {
		e := &examples{rows: rows, shape: proto.shape, input: proto.input, cls: proto.cls, num: proto.num, batch: max(1, min(batch, len(rows))), seed: seed}
		return e
	}
	train = build(perm[:nTrain])
	if nTest > 0 {
		test = build(perm[nTrain:])
	}
	return train, test, nil
}

// Split shuffles a table and keeps holdOut of it aside for scoring, with
// class answers.
func (t *Table) Split(holdOut float64, seed uint64, batch int) (train, test Set, err error) {
	f := len(t.Features)
	return split(t.Rows, holdOut, seed, batch, &examples{
		shape: []int{f},
		input: func(dst []float32, i int) []float32 { return append(dst, t.X[i*f:(i+1)*f]...) },
		cls:   t.Y,
	})
}

// Source is where a job's examples come from: a CSV table or a ZIP of
// photos held in memory, or the built-in handwritten digits in a folder.
type Source struct {
	Kind store.DatasetKind
	// CSV is the table for DatasetTable, or the ZIP for the photo kinds.
	CSV    []byte
	Target string // the answer column, or "" for the last
	// Columns are the dataset's columns as first found; their kinds are
	// kept, so a column read as text stays text.
	Columns  []schema.Column
	MNISTDir string // where the digits are (or get downloaded), for DatasetMNIST
}

// Hints returns the column kinds a source was first read with.
func (s Source) Hints() map[string]schema.Kind {
	if len(s.Columns) == 0 {
		return nil
	}
	out := map[string]schema.Kind{}
	for _, c := range s.Columns {
		out[c.Name] = c.Kind
	}
	return out
}

// SourceOf returns the Source for a stored dataset.
func SourceOf(st *store.Store, d store.Dataset) (Source, error) {
	switch d.Kind {
	case store.DatasetTable, store.DatasetPhotos, store.DatasetPhotoTable:
		b, err := st.ReadDataset(d.ID)
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: d.Kind, CSV: b, Target: d.Target, Columns: d.Columns}, nil
	case store.DatasetMNIST:
		return Source{Kind: d.Kind, MNISTDir: filepath.Join(st.DatasetDir(), "mnist")}, nil
	}
	return Source{}, fmt.Errorf("training: unknown dataset kind %d", d.Kind)
}

// Loaded is a source made ready for one architecture.
type Loaded struct {
	Train, Test Set // Test is nil when nothing is held out
	Spec        arch.Spec
	Features    []string        // the numbers of a table row
	Columns     []schema.Column // the input columns
	// Skipped counts examples left out: an empty answer, or a photo
	// missing or unreadable.
	Skipped int
}

// Load returns a source's training and scoring batches for architecture
// a and the spec a model learns for it: shape, answers, and for tables
// the columns and their statistics.
func Load(ctx context.Context, src Source, a arch.Architecture, s store.Settings, seed uint64) (Loaded, error) {
	switch src.Kind {
	case store.DatasetTable:
		t, err := ParseTable(bytes.NewReader(src.CSV), src.Target, src.Hints())
		if err != nil {
			return Loaded{}, err
		}
		if schema.PhotoColumn(t.Columns) >= 0 {
			return Loaded{}, fmt.Errorf("%w: a table with a photo column needs its photos; upload a ZIP with the table and the photos", ErrBadTable)
		}
		f := len(t.Features)
		return fromTable(t, a, s, seed, []int{f}, func(dst []float32, i int) []float32 { return append(dst, t.X[i*f:(i+1)*f]...) })
	case store.DatasetPhotos:
		return loadPhotos(ctx, src, a, s, seed)
	case store.DatasetPhotoTable:
		arc, err := OpenArchive(src.CSV)
		if err != nil {
			return Loaded{}, err
		}
		name, b, err := arc.Table()
		if err != nil {
			return Loaded{}, err
		}
		t, err := ParseTable(bytes.NewReader(b), src.Target, src.Hints())
		if err != nil {
			return Loaded{}, err
		}
		if schema.PhotoColumn(t.Columns) < 0 {
			return Loaded{}, fmt.Errorf("%w: no column of data.csv names the photos", ErrBadTable)
		}
		var keep []int
		var pixels []uint8
		for i, cell := range t.Photos {
			if err := ctx.Err(); err != nil {
				return Loaded{}, err
			}
			p := arc.Find(name, cell)
			if p == "" {
				continue
			}
			px, err := arc.Photo(p)
			if err != nil {
				continue
			}
			keep = append(keep, i)
			pixels = append(pixels, px...)
			if len(keep) > MaxPhotos {
				return Loaded{}, fmt.Errorf("%w: at most %d photos", ErrBadTable, MaxPhotos)
			}
		}
		skipped := t.Rows - len(keep)
		t.keep(keep)
		t.Skipped += skipped
		if t.Rows < MinRows {
			return Loaded{}, fmt.Errorf("%w: only %d rows have a photo the server can read; at least %d are needed", ErrBadTable, t.Rows, MinRows)
		}
		f := len(t.Features)
		return fromTable(t, a, s, seed, []int{schema.PhotoPixels + f}, func(dst []float32, i int) []float32 {
			dst = schema.Floats(dst, pixels[i*schema.PhotoPixels:(i+1)*schema.PhotoPixels])
			return append(dst, t.X[i*f:(i+1)*f]...)
		})
	case store.DatasetMNIST:
		if err := datasets.DownloadMNIST(ctx, src.MNISTDir); err != nil {
			return Loaded{}, fmt.Errorf("couldn't download the handwritten digits: %w", err)
		}
		tr, te, err := datasets.MNIST(src.MNISTDir, data.BatchSize(s.BatchSize), data.Shuffle(seed))
		if err != nil {
			return Loaded{}, err
		}
		return Loaded{Train: tr, Test: te, Spec: arch.Spec{InputShape: []int{1, 28, 28}, Labels: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}}}, nil
	}
	return Loaded{}, fmt.Errorf("training: unknown dataset kind %d", src.Kind)
}

// fromTable splits a parsed table for architecture a; input writes one
// example of the given shape.
func fromTable(t *Table, a arch.Architecture, s store.Settings, seed uint64, shape []int, input func([]float32, int) []float32) (Loaded, error) {
	mean, std := t.Stats()
	l := Loaded{Features: t.Features, Columns: t.Columns, Skipped: t.Skipped,
		Spec: arch.Spec{InputShape: shape, Mean: mean, Std: std}}
	proto := &examples{shape: shape, input: input}
	switch a.Answers() {
	case arch.Number:
		if !t.Numeric {
			return Loaded{}, fmt.Errorf("%w: a number model needs a number in every row of %q", ErrBadTable, t.Target)
		}
		m, sd := stats(t.V, 1, t.Rows)
		l.Spec.Labels, l.Spec.TargetMean, l.Spec.TargetStd = []string{t.Target}, m[0], sd[0]
		proto.num = t.V
	default:
		if t.Classes == nil {
			return Loaded{}, fmt.Errorf("%w: the %q column has too many different values to pick from; try a number model", ErrBadTable, t.Target)
		}
		l.Spec.Labels = t.Classes
		proto.cls = t.Y
	}
	var err error
	l.Train, l.Test, err = split(t.Rows, s.HoldOut, seed, s.BatchSize, proto)
	return l, err
}

// loadPhotos reads a ZIP of photos sorted into one folder per answer.
func loadPhotos(ctx context.Context, src Source, a arch.Architecture, s store.Settings, seed uint64) (Loaded, error) {
	arc, err := OpenArchive(src.CSV)
	if err != nil {
		return Loaded{}, err
	}
	folders, _ := arc.Folders()
	labels, err := PhotoLabels(folders)
	if err != nil {
		return Loaded{}, err
	}
	var pixels []uint8
	var cls []int32
	skipped := 0
	for c, label := range labels {
		for _, name := range folders[label] {
			if err := ctx.Err(); err != nil {
				return Loaded{}, err
			}
			px, err := arc.Photo(name)
			if err != nil {
				skipped++
				continue
			}
			pixels = append(pixels, px...)
			cls = append(cls, int32(c))
		}
	}
	if len(cls) < MinRows {
		return Loaded{}, fmt.Errorf("%w: only %d photos could be read; at least %d are needed", ErrBadTable, len(cls), MinRows)
	}
	shape := []int{3, schema.PhotoSize, schema.PhotoSize}
	l := Loaded{Spec: arch.Spec{InputShape: shape, Labels: labels}, Skipped: skipped}
	l.Train, l.Test, err = split(len(cls), s.HoldOut, seed, s.BatchSize, &examples{
		shape: shape,
		input: func(dst []float32, i int) []float32 {
			return schema.Floats(dst, pixels[i*schema.PhotoPixels:(i+1)*schema.PhotoPixels])
		},
		cls: cls,
	})
	return l, err
}

// PhotoLabels returns the answers a ZIP of photo folders teaches: every
// folder with a photo in it, sorted.
func PhotoLabels(folders map[string][]string) ([]string, error) {
	var labels []string
	total := 0
	for name, files := range folders {
		if len(files) > 0 {
			labels = append(labels, name)
			total += len(files)
		}
	}
	slices.Sort(labels)
	switch {
	case len(labels) == 0:
		return nil, fmt.Errorf("%w: the ZIP has no photos in folders. Put the photos of each answer in a folder named after it", ErrBadTable)
	case len(labels) == 1:
		return nil, fmt.Errorf("%w: all photos are in one folder, %q, so there is nothing to tell apart. Add a folder for each other answer", ErrBadTable, labels[0])
	case len(labels) > MaxClasses:
		return nil, fmt.Errorf("%w: at most %d folders", ErrBadTable, MaxClasses)
	case total > MaxPhotos:
		return nil, fmt.Errorf("%w: at most %d photos", ErrBadTable, MaxPhotos)
	}
	return labels, nil
}

// Architectures lists the model types that can learn from a dataset.
func Architectures(d store.Dataset) []string {
	var out []string
	for _, a := range arch.Every() {
		switch d.Kind {
		case store.DatasetTable:
			if a.Input != arch.Table {
				continue
			}
			switch {
			case a.Answers() == arch.Number:
				if d.NumberTarget {
					out = append(out, a.Name)
				}
			case len(d.Labels) >= 2 && (a.Custom || a.Text == schema.HasText(d.Columns)):
				out = append(out, a.Name)
			}
		case store.DatasetPhotos:
			if a.Input == arch.Image && !a.Fixed() {
				out = append(out, a.Name)
			}
		case store.DatasetPhotoTable:
			if a.Input == arch.PhotoTable {
				out = append(out, a.Name)
			}
		case store.DatasetMNIST:
			if a.Input == arch.Image && slices.Equal(a.Spec.InputShape, d.InputShape) {
				out = append(out, a.Name)
			}
		}
	}
	return out
}
