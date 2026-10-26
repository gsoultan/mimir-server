package training

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
)

// MaxBenchmarkRows is the most rows a benchmark keeps, so scoring a model
// on one stays quick.
const MaxBenchmarkRows = 10000

// Freeze picks share of a table's rows (0 < share < 1), at most max, the
// same ones for the same seed, and returns them as a CSV with the header,
// with how many there are.
func Freeze(table []byte, share float64, seed uint64, max int) ([]byte, int, error) {
	header, rows, err := readRecords(table)
	if err != nil {
		return nil, 0, err
	}
	n := min(int(float64(len(rows))*share), max)
	if n < 1 {
		return nil, 0, fmt.Errorf("%w: too few rows to keep any aside", ErrBadTable)
	}
	if len(rows)-n < 2 {
		return nil, 0, fmt.Errorf("%w: keeping %d rows aside leaves too few to learn from", ErrBadTable, n)
	}
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	pick := r.Perm(len(rows))[:n]
	out := [][]string{header}
	slices.Sort(pick)
	for _, i := range pick {
		out = append(out, rows[i])
	}
	return writeRecords(out), n, nil
}

// Without returns a table with every row that is also in one of frozen
// (CSVs with a header) left out, and how many it left out, so a model
// never learns from the examples it will be tested on.
func Without(table []byte, frozen ...[]byte) ([]byte, int, error) {
	held := map[string]bool{}
	for _, f := range frozen {
		_, rows, err := readRecords(f)
		if err != nil {
			return nil, 0, err
		}
		for _, row := range rows {
			held[key(row)] = true
		}
	}
	if len(held) == 0 {
		return table, 0, nil
	}
	header, rows, err := readRecords(table)
	if err != nil {
		return nil, 0, err
	}
	out := [][]string{header}
	for _, row := range rows {
		if !held[key(row)] {
			out = append(out, row)
		}
	}
	return writeRecords(out), len(rows) + 1 - len(out), nil
}

// key is a row as one string; fields are trimmed, as the table reader
// trims them.
func key(row []string) string {
	t := make([]string, len(row))
	for i, f := range row {
		t[i] = strings.TrimSpace(f)
	}
	return strings.Join(t, "\x1f")
}

func readRecords(b []byte) ([]string, [][]string, error) {
	r := csv.NewReader(bytes.NewReader(b))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil, fmt.Errorf("%w: the table is empty", ErrBadTable)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrBadTable, err)
	}
	for i := range header {
		header[i] = strings.TrimSpace(strings.TrimPrefix(header[i], "\ufeff"))
	}
	var rows [][]string
	for {
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			return header, rows, nil
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrBadTable, err)
		}
		rows = append(rows, row)
	}
}

func writeRecords(rows [][]string) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.WriteAll(rows) // writing to memory doesn't fail
	return b.Bytes()
}
