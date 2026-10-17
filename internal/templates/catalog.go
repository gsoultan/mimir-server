// Package templates holds ready-made starting points for common
// questions: what data a model needs, how it is shaped, made-up example
// data to try, and a check of real data against it.
package templates

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/gsoultan/mimir-server/internal/schema"
)

//go:embed catalog.json
var catalogJSON []byte

// Category groups templates by line of business.
type Category struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Blurb string `json:"blurb"`
	Icon  string `json:"icon"`
}

// Column is one column of a template's data.
type Column struct {
	Name     string      `json:"name"`
	Kind     schema.Kind `json:"kind"`
	Meaning  string      `json:"meaning"`
	Examples []string    `json:"examples"`
}

// Folder is one answer of a photo template.
type Folder struct {
	Name    string `json:"name"`
	Meaning string `json:"meaning"`
	Count   int64  `json:"count"`
}

// What a template answers.
const (
	YesNo  = "yes_no"
	Group  = "group"
	Number = "number"
)

// What a template's data looks like.
const (
	Table      = "table"
	Text       = "text"
	Photos     = "photos"
	PhotoTable = "photo_table"
)

// Template is one starting point.
type Template struct {
	ID          string   `json:"id"`
	Category    string   `json:"category"`
	Kind        string   `json:"kind"`
	Data        string   `json:"data"`
	Name        string   `json:"name"`
	Question    string   `json:"question"`
	Predicts    string   `json:"predicts"`
	RowMeans    string   `json:"row_means"`
	Source      string   `json:"source"`
	SourceShort string   `json:"source_short"`
	RowsMin     int64    `json:"rows_min"`
	RowsGood    int64    `json:"rows_good"`
	RowsGreat   int64    `json:"rows_great"`
	Balance     string   `json:"balance"`
	Table       string   `json:"table"`
	Tips        []string `json:"tips"`
	Columns     []Column `json:"columns"`
	Folders     []Folder `json:"folders"`
}

var catalog struct {
	Categories []Category `json:"categories"`
	Templates  []Template `json:"templates"`
}

func init() {
	if err := json.Unmarshal(catalogJSON, &catalog); err != nil {
		panic("templates: bad catalog: " + err.Error())
	}
}

// All returns every category and template, in catalog order.
func All() ([]Category, []Template) {
	return slices.Clone(catalog.Categories), slices.Clone(catalog.Templates)
}

// Get returns the template with id.
func Get(id string) (Template, bool) {
	i := slices.IndexFunc(catalog.Templates, func(t Template) bool { return t.ID == id })
	if i < 0 {
		return Template{}, false
	}
	return catalog.Templates[i], true
}

// Answer returns the column that holds the answer: the last one.
func (t Template) Answer() Column {
	if len(t.Columns) == 0 {
		return Column{}
	}
	return t.Columns[len(t.Columns)-1]
}

// Inputs returns the columns the model reads.
func (t Template) Inputs() []Column {
	if len(t.Columns) == 0 {
		return nil
	}
	return t.Columns[:len(t.Columns)-1]
}

// Architecture returns the model type the template teaches.
func (t Template) Architecture() string {
	switch {
	case t.Data == Photos:
		return "photo-cnn"
	case t.Data == PhotoTable:
		return "photo-table"
	case t.Kind == Number:
		return "number-mlp"
	case slices.ContainsFunc(t.Inputs(), func(c Column) bool { return c.Kind == schema.Text }):
		return "text-mlp"
	}
	return "table-mlp"
}

// Query returns a starting database query that gives the template's
// columns, or "" for photos.
func (t Template) Query() string {
	if len(t.Columns) == 0 || t.Table == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("SELECT\n")
	for i, c := range t.Columns {
		b.WriteString("  " + c.Name)
		if i < len(t.Columns)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	per := strings.TrimSuffix(strings.TrimPrefix(t.RowMeans, "One "), ".")
	b.WriteString("FROM " + t.Table + "\n-- one row per " + strings.ToLower(per) + "\nLIMIT 100000;")
	return b.String()
}

// key reduces a column name to letters and digits, lower case, so
// "Speed (RPM)" and "speed_rpm" match.
func key(name string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, name)
}

// Match pairs each template column with a dataset column: given pairs
// first, then by name. A template column without a pair is left out.
func (t Template) Match(header []string, given map[string]string) map[string]string {
	out := map[string]string{}
	used := map[string]bool{}
	for _, c := range t.Columns {
		if h, ok := given[c.Name]; ok && slices.Contains(header, h) && !used[h] {
			out[c.Name], used[h] = h, true
		}
	}
	for _, c := range t.Columns {
		if _, ok := out[c.Name]; ok {
			continue
		}
		for _, h := range header {
			if !used[h] && key(h) == key(c.Name) {
				out[c.Name], used[h] = h, true
				break
			}
		}
	}
	return out
}

// Hints returns the kinds the template gives the matched dataset
// columns.
func (t Template) Hints(match map[string]string) map[string]schema.Kind {
	out := map[string]schema.Kind{}
	for _, c := range t.Inputs() {
		if h, ok := match[c.Name]; ok {
			out[h] = c.Kind
		}
	}
	return out
}
