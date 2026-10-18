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
	cols := make([]Column, len(names))
	for j, name := range names {
		k, ok := hints[name]
		if !ok || !k.Valid() {
			k = guess(values[j])
		}
		cols[j] = finish(Column{Name: name, Kind: k}, values[j])
	}
	return cols
}

// guess picks a column's kind from its cells.
func guess(cells []string) Kind {
	seen := map[string]bool{}
	numbers, yesno, photos, filled, length := true, true, true, 0, 0
	for _, v := range cells {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		filled++
		length += len(v)
		if numbers {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				numbers = false
			}
		}
		if yesno {
			if _, ok := parseYesNo(v); !ok {
				yesno = false
			}
		}
		photos = photos && IsPhotoName(v)
		if len(seen) <= MaxValues*4 {
			seen[strings.ToLower(v)] = true
		}
	}
	switch {
	case filled == 0 || numbers:
		return Number
	case yesno:
		return YesNo
	case photos:
		return Photo
	case len(seen) <= MaxValues*4 && length/filled <= 30 && !wordy(cells):
		return Category
	}
	return Text
}

// wordy reports whether a column's cells read like sentences: most of
// them hold three words or more.
func wordy(cells []string) bool {
	n, long := 0, 0
	for _, v := range cells {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		n++
		if len(strings.Fields(v)) >= 3 {
			long++
		}
		if n >= 1000 {
			break
		}
	}
	return n > 0 && long*2 > n
}

// finish fills in what encoding a column needs from its cells.
func finish(c Column, cells []string) Column {
	switch c.Kind {
	case Number:
		sum, n := 0.0, 0
		for _, v := range cells {
			if f, ok := parseNumber(v); ok {
				sum += f
				n++
			}
		}
		if n > 0 {
			c.Fill = sum / float64(n)
		}
	case Category:
		count := map[string]int{}
		for _, v := range cells {
			if v = strings.TrimSpace(v); v != "" {
				count[v]++
			}
		}
		vals := make([]string, 0, len(count))
		for v := range count {
			vals = append(vals, v)
		}
		slices.SortFunc(vals, func(a, b string) int {
			return cmp.Or(cmp.Compare(count[b], count[a]), cmp.Compare(a, b))
		})
		c.Values = vals[:min(len(vals), MaxValues)]
	}
	return c
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

func parseNumber(v string) (float64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		f, err = strconv.ParseFloat(strings.NewReplacer(",", "", " ", "", "_", "").Replace(v), 64)
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
