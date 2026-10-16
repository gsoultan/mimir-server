// Package training teaches models from datasets, one job at a time.
package training

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gsoultan/mimir/data"
	"github.com/gsoultan/mimir/data/datasets"
	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// ErrBadTable reports a CSV the server can't learn from.
var ErrBadTable = errors.New("training: bad table")

// Limits on uploaded tables.
const (
	MaxRows     = 1_000_000
	MaxFeatures = 1000
	MaxClasses  = 1000
	MinRows     = 10
)

// Table is a parsed CSV: every column but the target is a number, and the
// target names a class.
type Table struct {
	Features []string
	Classes  []string
	X        []float32 // rows × features
	Y        []int32   // class index per row
	Rows     int
}

// ParseTable reads a CSV with a header row. target names the label column;
// empty means the last column.
func ParseTable(r io.Reader, target string) (*Table, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("%w: the first row must name the columns", ErrBadTable)
	}
	header = slices.Clone(header)
	for i := range header {
		header[i] = strings.TrimSpace(header[i])
	}
	if len(header) < 2 {
		return nil, fmt.Errorf("%w: a table needs at least one number column and one label column", ErrBadTable)
	}
	if len(header) > MaxFeatures+1 {
		return nil, fmt.Errorf("%w: at most %d columns", ErrBadTable, MaxFeatures+1)
	}
	ti := len(header) - 1
	if target != "" {
		ti = slices.Index(header, target)
		if ti < 0 {
			return nil, fmt.Errorf("%w: there is no column called %q", ErrBadTable, target)
		}
	}
	t := &Table{Features: slices.Delete(slices.Clone(header), ti, ti+1)}
	var labels []string
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %v", ErrBadTable, line, err)
		}
		if t.Rows >= MaxRows {
			return nil, fmt.Errorf("%w: at most %d rows", ErrBadTable, MaxRows)
		}
		for i, field := range rec {
			field = strings.TrimSpace(field)
			if i == ti {
				labels = append(labels, field)
				continue
			}
			v, err := strconv.ParseFloat(field, 32)
			if err != nil {
				return nil, fmt.Errorf("%w: line %d, column %q: %q is not a number", ErrBadTable, line, header[i], field)
			}
			t.X = append(t.X, float32(v))
		}
		t.Rows++
	}
	if t.Rows < MinRows {
		return nil, fmt.Errorf("%w: at least %d rows are needed to learn anything", ErrBadTable, MinRows)
	}
	t.Classes = slices.Compact(slices.Sorted(slices.Values(labels)))
	if len(t.Classes) < 2 {
		return nil, fmt.Errorf("%w: the %q column has only one value, so there is nothing to tell apart", ErrBadTable, header[ti])
	}
	if len(t.Classes) > MaxClasses {
		return nil, fmt.Errorf("%w: the %q column has %d different values; at most %d", ErrBadTable, header[ti], len(t.Classes), MaxClasses)
	}
	t.Y = make([]int32, t.Rows)
	for i, l := range labels {
		c, _ := slices.BinarySearch(t.Classes, l)
		t.Y[i] = int32(c)
	}
	return t, nil
}

// Sample returns the header and the first n rows of a CSV, as text.
func Sample(b []byte, n int) [][]string {
	cr := csv.NewReader(bytes.NewReader(b))
	cr.TrimLeadingSpace = true
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

// Split shuffles a table and keeps holdOut of it aside for scoring.
// Stats returns the mean and standard deviation of each feature over the
// whole table. A constant column gets a deviation of 1 so it divides safely.
func (t *Table) Stats() (mean, std []float32) {
	f := len(t.Features)
	mean, std = make([]float32, f), make([]float32, f)
	if t.Rows == 0 {
		for i := range std {
			std[i] = 1
		}
		return mean, std
	}
	sum := make([]float64, f)
	for i := range t.Rows {
		for j := range f {
			sum[j] += float64(t.X[i*f+j])
		}
	}
	for j := range f {
		mean[j] = float32(sum[j] / float64(t.Rows))
	}
	sq := make([]float64, f)
	for i := range t.Rows {
		for j := range f {
			d := float64(t.X[i*f+j]) - float64(mean[j])
			sq[j] += d * d
		}
	}
	for j := range f {
		s := math.Sqrt(sq[j] / float64(t.Rows))
		if s < 1e-6 {
			s = 1
		}
		std[j] = float32(s)
	}
	return mean, std
}

func (t *Table) Split(holdOut float64, seed uint64, batch int) (train, test *data.Tensors, err error) {
	f := len(t.Features)
	perm := rand.New(rand.NewPCG(seed, seed)).Perm(t.Rows)
	nTest := int(float64(t.Rows) * holdOut)
	if nTest > 0 && nTest < 1 {
		nTest = 1
	}
	nTrain := t.Rows - nTest
	if nTrain < 1 {
		return nil, nil, fmt.Errorf("%w: nothing left to learn from", ErrBadTable)
	}
	build := func(idx []int) (*data.Tensors, error) {
		if len(idx) == 0 {
			return nil, nil
		}
		x := make([]float32, 0, len(idx)*f)
		y := make([]int32, 0, len(idx))
		for _, i := range idx {
			x = append(x, t.X[i*f:(i+1)*f]...)
			y = append(y, t.Y[i])
		}
		return data.FromTensors(tensor.FromSlice(x, len(idx), f), tensor.FromSlice(y), data.BatchSize(min(batch, len(idx))), data.Shuffle(seed))
	}
	if train, err = build(perm[:nTrain]); err != nil {
		return nil, nil, err
	}
	if test, err = build(perm[nTrain:]); err != nil {
		return nil, nil, err
	}
	return train, test, nil
}

// Load returns a dataset's training and scoring batches and the spec a
// model learns for it. test is nil when nothing is held out.
func Load(ctx context.Context, st *store.Store, d store.Dataset, s store.Settings, seed uint64) (train, test *data.Tensors, spec arch.Spec, err error) {
	switch d.Kind {
	case store.DatasetTable:
		b, err := os.ReadFile(st.DatasetPath(d.ID))
		if err != nil {
			return nil, nil, spec, err
		}
		t, err := ParseTable(bytes.NewReader(b), d.Target)
		if err != nil {
			return nil, nil, spec, err
		}
		tr, te, err := t.Split(s.HoldOut, seed, s.BatchSize)
		if err != nil {
			return nil, nil, spec, err
		}
		mean, std := t.Stats()
		spec = arch.Spec{InputShape: []int{len(t.Features)}, Labels: t.Classes, Mean: mean, Std: std}
		if te == nil {
			return tr, nil, spec, nil
		}
		return tr, te, spec, nil
	case store.DatasetMNIST:
		dir := filepath.Join(st.DatasetDir(), "mnist")
		if err := datasets.DownloadMNIST(ctx, dir); err != nil {
			return nil, nil, spec, fmt.Errorf("couldn't download the handwritten digits: %w", err)
		}
		tr, te, err := datasets.MNIST(dir, data.BatchSize(s.BatchSize), data.Shuffle(seed))
		if err != nil {
			return nil, nil, spec, err
		}
		return tr, te, arch.Spec{InputShape: []int{1, 28, 28}, Labels: []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}}, nil
	}
	return nil, nil, spec, fmt.Errorf("training: unknown dataset kind %d", d.Kind)
}

// Architectures lists the model types that can learn from a dataset.
func Architectures(d store.Dataset) []string {
	var out []string
	for _, a := range arch.All() {
		switch d.Kind {
		case store.DatasetTable:
			if a.Input == arch.Table {
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
