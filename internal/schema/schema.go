// Package schema turns the columns of a table, and photos, into the
// numbers a model reads.
//
// A table's columns are found once, from the examples a model learns
// from: which hold numbers, which pick from a few values, which hold free
// text. The resulting []Column is saved with the model, so a question
// asked later is turned into numbers exactly the same way.
package schema

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Kind is what a column holds.
type Kind string

const (
	Number   Kind = "number"   // a measurement or count
	Category Kind = "category" // one of a few values, like a colour
	YesNo    Kind = "yesno"    // yes or no
	Text     Kind = "text"     // free text, like a message
	Photo    Kind = "photo"    // the file name of a photo
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case Number, Category, YesNo, Text, Photo:
		return true
	}
	return false
}

const (
	// MaxValues is how many values of a category column are kept, most
	// common first. Rarer values all read as "something else".
	MaxValues = 50
	// TextDims is how many numbers a text column becomes.
	TextDims = 128
	// MaxWidth bounds how many numbers one row becomes.
	MaxWidth = 4096
	// MaxWords is how much of a long text is read.
	MaxWords = 500
)

// Column is one input column and how it becomes numbers.
type Column struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Values are a category column's values, in the order they are
	// encoded.
	Values []string `json:"values,omitempty"`
	// Fill is the number an empty cell of a number column reads as: the
	// column's average.
	Fill float64 `json:"fill,omitempty"`
}

// Width is how many numbers the column becomes. A photo is read
// separately, so it adds none to the table part.
func (c Column) Width() int {
	switch c.Kind {
	case Category:
		return len(c.Values)
	case Text:
		return TextDims
	case Photo:
		return 0
	}
	return 1
}

// Features names each number a column becomes.
func (c Column) Features() []string {
	switch c.Kind {
	case Category:
		out := make([]string, len(c.Values))
		for i, v := range c.Values {
			out[i] = c.Name + "=" + v
		}
		return out
	case Text:
		out := make([]string, TextDims)
		for i := range out {
			out[i] = fmt.Sprintf("%s#%d", c.Name, i)
		}
		return out
	case Photo:
		return nil
	}
	return []string{c.Name}
}

// Width is how many numbers a row of cols becomes.
func Width(cols []Column) int {
	w := 0
	for _, c := range cols {
		w += c.Width()
	}
	return w
}

// Features names every number a row of cols becomes, in order.
func Features(cols []Column) []string {
	var out []string
	for _, c := range cols {
		out = append(out, c.Features()...)
	}
	return out
}

// PhotoColumn returns the index of the photo column, or -1.
func PhotoColumn(cols []Column) int {
	return slices.IndexFunc(cols, func(c Column) bool { return c.Kind == Photo })
}

// Plain reports whether every column is a number, as in tables taught
// before columns had kinds.
func Plain(cols []Column) bool {
	return !slices.ContainsFunc(cols, func(c Column) bool { return c.Kind != Number })
}

// HasText reports whether any column is free text.
func HasText(cols []Column) bool {
	return slices.ContainsFunc(cols, func(c Column) bool { return c.Kind == Text })
}

// Detect works out each column's kind from its values: values[j] holds
// every cell of column j. A hint, when given for a column, wins.
func Detect(names []string, values [][]string, hints map[string]Kind) []Column {
	d := NewDetector(names, hints)
	for j, cells := range values {
		for _, v := range cells {
			d.Add(j, v)
		}
	}
	return d.Columns(func(add func(j int, v string)) {
		for j, cells := range values {
			for _, v := range cells {
				add(j, v)
			}
		}
	})
}

// countCap bounds how many distinct values a Detector counts per column
// while it reads. A category column with more is counted again, on its
// own, once its kind is known.
const countCap = 1024

// Detector finds a table's columns from its cells, read one at a time, so
// a large table never needs all of its cells in memory at once. It reads
// the same as Detect.
type Detector struct {
	names []string
	hints map[string]Kind
	cols  []detected
}

// detected is what a Detector knows about one column so far.
type detected struct {
	kind                    Kind // from a hint, or "" to guess
	filled, length          int
	numbers, yesno, photos  bool
	seen                    map[string]bool
	sentences, sentenceLong int // cells looked at for wordy, and long ones
	sum                     float64
	nums                    int
	count                   map[string]int
	overflow                bool // count stopped at countCap
}

// NewDetector returns a Detector for the columns names. hints, when set,
// give a column's kind instead of guessing it from its cells.
func NewDetector(names []string, hints map[string]Kind) *Detector {
	d := &Detector{names: names, hints: hints, cols: make([]detected, len(names))}
	for j, name := range names {
		c := &d.cols[j]
		if k, ok := hints[name]; ok && k.Valid() {
			c.kind = k
		}
		c.numbers, c.yesno, c.photos = true, true, true
		c.seen, c.count = map[string]bool{}, map[string]int{}
	}
	return d
}

// Add reads cell v of column j.
func (d *Detector) Add(j int, v string) {
	c := &d.cols[j]
	v = strings.TrimSpace(v)
	if c.kind == Number {
		if f, ok := parseNumber(v); ok {
			c.sum += f
			c.nums++
		}
	}
	if v == "" {
		return
	}
	if (c.kind == "" || c.kind == Category) && !c.overflow {
		if _, ok := c.count[v]; ok {
			c.count[v]++
		} else if len(c.count) < countCap {
			c.count[strings.Clone(v)] = 1
		} else {
			c.overflow, c.count = true, nil
		}
	}
	if c.kind != "" {
		return
	}
	c.filled++
	c.length += len(v)
	if c.numbers {
		// A column guessed to hold numbers has only cells ParseFloat reads,
		// so its average needs nothing parseNumber reads besides.
		f, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil:
			c.numbers = false
		case !math.IsNaN(f) && !math.IsInf(f, 0):
			c.sum += f
			c.nums++
		}
	}
	if c.yesno {
		if _, ok := parseYesNo(v); !ok {
			c.yesno = false
		}
	}
	c.photos = c.photos && IsPhotoName(v)
	if len(c.seen) <= MaxValues*4 {
		if k := strings.ToLower(v); !c.seen[k] {
			c.seen[strings.Clone(k)] = true
		}
	}
	if c.sentences < 1000 {
		c.sentences++
		if len(strings.Fields(v)) >= 3 {
			c.sentenceLong++
		}
	}
}

// Columns returns the columns of every cell added. replay adds the cells
// again, in any order, to count the values of a category column with too
// many to count the first time; it is not called otherwise.
func (d *Detector) Columns(replay func(add func(j int, v string))) []Column {
	cols := make([]Column, len(d.names))
	var recount []int
	for j, name := range d.names {
		c := &d.cols[j]
		k := c.kind
		if k == "" {
			k = c.guess()
		}
		cols[j] = Column{Name: name, Kind: k}
		switch {
		case k == Number && c.nums > 0:
			cols[j].Fill = c.sum / float64(c.nums)
		case k == Category && c.overflow:
			recount = append(recount, j)
		case k == Category:
			cols[j].Values = topValues(c.count)
		}
	}
	if len(recount) > 0 {
		counts := map[int]map[string]int{}
		for _, j := range recount {
			counts[j] = map[string]int{}
		}
		replay(func(j int, v string) {
			if count, ok := counts[j]; ok {
				if v = strings.TrimSpace(v); v != "" {
					if _, ok := count[v]; ok {
						count[v]++
					} else {
						count[strings.Clone(v)] = 1
					}
				}
			}
		})
		for _, j := range recount {
			cols[j].Values = topValues(counts[j])
		}
	}
	return cols
}

// guess picks a column's kind from what Add saw of its cells.
func (c *detected) guess() Kind {
	switch {
	case c.filled == 0 || c.numbers:
		return Number
	case c.yesno:
		return YesNo
	case c.photos:
		return Photo
	case len(c.seen) <= MaxValues*4 && c.length/c.filled <= 30 && !(c.sentences > 0 && c.sentenceLong*2 > c.sentences):
		return Category
	}
	return Text
}

// topValues returns the MaxValues most common values, most common first.
func topValues(count map[string]int) []string {
	vals := make([]string, 0, len(count))
	for v := range count {
		vals = append(vals, v)
	}
	slices.SortFunc(vals, func(a, b string) int {
		return cmp.Or(cmp.Compare(count[b], count[a]), cmp.Compare(a, b))
	})
	return vals[:min(len(vals), MaxValues)]
}

// Encode appends the numbers for one row to dst. cell returns the row's
// value for column i.
func Encode(dst []float32, cols []Column, cell func(i int) string) []float32 {
	for i, c := range cols {
		v := strings.TrimSpace(cell(i))
		switch c.Kind {
		case Number:
			f, ok := parseNumber(v)
			if !ok {
				f = c.Fill
			}
			dst = append(dst, float32(f))
		case YesNo:
			b, _ := parseYesNo(v)
			dst = append(dst, b)
		case Category:
			at := slices.Index(c.Values, v)
			for j := range c.Values {
				dst = append(dst, float32(b2i(j == at)))
			}
		case Text:
			dst = append(dst, encodeText(v)...)
		}
	}
	return dst
}

// ParseNumber reads a number cell. Thousands separators and spaces are
// allowed; empty or unreadable cells are not ok.
func ParseNumber(v string) (float64, bool) { return parseNumber(v) }

// separators are what a number cell may hold between its digits.
var separators = strings.NewReplacer(",", "", " ", "", "_", "")

func parseNumber(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		f, err = strconv.ParseFloat(separators.Replace(v), 64)
	}
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// parseYesNo reads yes/no words as 1 or 0.
func parseYesNo(v string) (float32, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "y", "true", "t", "1":
		return 1, true
	case "no", "n", "false", "f", "0":
		return 0, true
	}
	return 0, false
}

// Words splits text into lowercase words.
func Words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// encodeText turns text into TextDims numbers: each word lands in a slot
// chosen by its hash, so no vocabulary needs keeping, and the counts are
// scaled to length 1 so long and short messages compare. Only the first
// MaxWords words count.
func encodeText(s string) []float32 {
	out := make([]float32, TextDims)
	words := Words(s)
	for _, w := range words[:min(len(words), MaxWords)] {
		h := fnv.New32a()
		h.Write([]byte(w))
		out[h.Sum32()%TextDims]++
	}
	var norm float64
	for i, n := range out {
		if n > 0 {
			out[i] = float32(math.Log1p(float64(n)))
			norm += float64(out[i] * out[i])
		}
	}
	if norm > 0 {
		scale := float32(1 / math.Sqrt(norm))
		for i := range out {
			out[i] *= scale
		}
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
