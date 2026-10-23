package training

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// bigTable writes a CSV of rows rows: 16 number columns, 3 category
// columns, one yes/no column and a class answer that depends on them.
func bigTable(rows int) []byte {
	rng := rand.New(rand.NewPCG(7, 7))
	var b strings.Builder
	b.Grow(rows * 120)
	for i := range 16 {
		fmt.Fprintf(&b, "n%d,", i)
	}
	b.WriteString("colour,size,region,member,label\n")
	colours := []string{"red", "green", "blue", "black", "white", "grey"}
	sizes := []string{"S", "M", "L", "XL"}
	regions := []string{"north", "south", "east", "west", "central"}
	for range rows {
		sum := 0.0
		for range 16 {
			v := rng.NormFloat64()
			sum += v
			fmt.Fprintf(&b, "%.3f,", v)
		}
		c := rng.IntN(len(colours))
		fmt.Fprintf(&b, "%s,%s,%s,", colours[c], sizes[rng.IntN(len(sizes))], regions[rng.IntN(len(regions))])
		if rng.IntN(2) == 0 {
			b.WriteString("yes,")
		} else {
			b.WriteString("no,")
		}
		switch {
		case sum > 2:
			b.WriteString("high\n")
		case sum < -2:
			b.WriteString("low\n")
		case c < 3:
			b.WriteString("mid-a\n")
		default:
			b.WriteString("mid-b\n")
		}
	}
	return []byte(b.String())
}

// BenchmarkParseTable times reading a large table into numbers.
func BenchmarkParseTable(b *testing.B) {
	csv := bigTable(200_000)
	b.SetBytes(int64(len(csv)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseTable(strings.NewReader(string(csv)), "", nil); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFitTable times one epoch of table-mlp on a large table, from
// the CSV bytes to the saved weights.
func BenchmarkFitTable(b *testing.B) {
	csv := bigTable(200_000)
	a, _ := arch.Lookup("table-mlp")
	s := TableDefaults
	s.Epochs = 1
	b.ReportAllocs()
	for b.Loop() {
		res, err := Fit(context.Background(), Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv}, Settings: s}, nil)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(res.History[0].Accuracy, "acc")
	}
}

// textTable writes a CSV of rows rows with a short sentence per row, a
// number column and a class answer.
func textTable(rows int) []byte {
	rng := rand.New(rand.NewPCG(3, 3))
	words := strings.Fields("the quick brown fox jumps over a lazy dog while Seven Ravens watch from the OLD oak counting 12 ships at sea while nobody noticed the weather turning")
	var b strings.Builder
	b.Grow(rows * 100)
	b.WriteString("message,score,label\n")
	for range rows {
		n := 6 + rng.IntN(12)
		for i := range n {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(words[rng.IntN(len(words))])
		}
		fmt.Fprintf(&b, ",%d,", rng.IntN(100))
		if rng.IntN(2) == 0 {
			b.WriteString("spam\n")
		} else {
			b.WriteString("ham\n")
		}
	}
	return []byte(b.String())
}

// BenchmarkParseTextTable times reading a table whose main column is
// free text.
func BenchmarkParseTextTable(b *testing.B) {
	csv := textTable(100_000)
	b.SetBytes(int64(len(csv)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ParseTable(strings.NewReader(string(csv)), "", nil); err != nil {
			b.Fatal(err)
		}
	}
}
