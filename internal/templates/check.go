package templates

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	mimirv1 "github.com/gsoultan/mimir-server/gen/mimir/v1"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/training"
)

// Levels of a check note.
const (
	bad  = "bad"
	warn = "warn"
	info = "info"
)

// MaxMissingPhotos bounds the list of rows whose photo is missing.
const MaxMissingPhotos = 1000

type check struct{ *mimirv1.DataCheck }

func (c check) note(level, title, body string) {
	c.Notes = append(c.Notes, &mimirv1.CheckNote{Level: level, Title: title, Body: body})
}

// finish orders the notes worst first and sets the verdict.
func (c check) finish() *mimirv1.DataCheck {
	rank := map[string]int{bad: 0, warn: 1, info: 2}
	slices.SortStableFunc(c.Notes, func(a, b *mimirv1.CheckNote) int { return cmp.Compare(rank[a.Level], rank[b.Level]) })
	c.Verdict = "good"
	if len(c.Notes) > 0 {
		switch c.Notes[0].Level {
		case bad:
			c.Verdict = "bad"
		case warn:
			c.Verdict = "warn"
		}
	}
	return c.DataCheck
}

// CheckTable checks a parsed table, against a template when t is not nil.
// match pairs template columns with the table's, as Match returns, and
// missing names the rows whose photo isn't there, for photo_table data.
func CheckTable(t *Template, tab *training.Table, match map[string]string, missing []string) *mimirv1.DataCheck {
	c := check{&mimirv1.DataCheck{Rows: int64(tab.Rows), Skipped: int64(tab.Skipped), AnswerColumn: tab.Target, NumberAnswer: tab.Numeric}}
	byName := map[string]int{}
	for i, col := range tab.Columns {
		byName[col.Name] = i
	}
	column := func(name string, kind schema.Kind, found string) *mimirv1.ColumnCheck {
		cc := &mimirv1.ColumnCheck{Name: name, Kind: string(kind), Found: found, Status: "ok"}
		switch {
		case found == "":
			cc.Status = "missing"
		case found == tab.Target:
			cc.Answer = true
			if found != name {
				cc.Status = "renamed"
			}
		default:
			i := byName[found]
			col, p := tab.Columns[i], tab.Profile[i]
			cc.FoundKind, cc.Empty, cc.Unreadable, cc.Words = string(col.Kind), int64(p.Empty), int64(p.Unreadable), p.Words
			switch {
			case found != name:
				cc.Status = "renamed"
			case kind != "" && col.Kind != kind:
				cc.Status = "kind"
			}
		}
		return cc
	}

	if t != nil {
		c.RowsMin, c.RowsGood, c.RowsGreat = t.RowsMin, t.RowsGood, t.RowsGreat
		used := map[string]bool{}
		var lost []string
		for _, tc := range t.Columns {
			found := match[tc.Name]
			used[found] = true
			c.Columns = append(c.Columns, column(tc.Name, tc.Kind, found))
			if found == "" && tc.Name != t.Answer().Name {
				lost = append(lost, tc.Name)
			}
		}
		for _, h := range tab.Header {
			if !used[h] {
				c.ExtraColumns = append(c.ExtraColumns, h)
			}
		}
		answer := t.Answer()
		if match[answer.Name] != tab.Target {
			c.note(bad, "Which column is the answer?",
				fmt.Sprintf("There is no column called %s, so the last column, %s, is the answer for now. Pick the column that holds %s.", answer.Name, tab.Target, strings.ToLower(strings.TrimSuffix(answer.Meaning, "."))))
		}
		if len(lost) > 0 {
			c.note(warn, plural(len(lost), "column isn't there", "columns aren't there")+": "+strings.Join(lost, ", ")+".",
				"If they're under other names, pick them. Otherwise it learns from the columns you have, and may guess less well.")
		}
		for _, cc := range c.Columns {
			if cc.Status == "kind" {
				c.note(info, fmt.Sprintf("%s reads as %s.", cc.Found, kindWord(schema.Kind(cc.FoundKind))),
					fmt.Sprintf("The template expects %s. It still learns; check the column holds what the template describes.", kindWord(schema.Kind(cc.Kind))))
			}
		}
		if t.Kind == Number && !tab.Numeric {
			c.note(bad, "The answer needs a number in every row.", fmt.Sprintf("Some rows of %s hold words. Fix or remove them, then upload again.", tab.Target))
		}
	} else {
		for _, col := range tab.Columns {
			c.Columns = append(c.Columns, column(col.Name, col.Kind, col.Name))
		}
		c.Columns = append(c.Columns, column(tab.Target, "", tab.Target))
	}

	rowsNote(c, t, int64(tab.Rows), "rows")
	if tab.Skipped > 0 {
		c.note(info, plural(tab.Skipped, "row has", "rows have")+" no answer.", "They are left out.")
	}
	for _, cc := range c.Columns {
		if cc.Status == "missing" || cc.Answer {
			continue
		}
		if cc.Unreadable > 0 {
			c.note(warn, fmt.Sprintf("%s has %s that aren't numbers.", cc.Found, plural(int(cc.Unreadable), "cell", "cells")),
				"They read as the column's average. Fix them if they're typos.")
		}
		if share := float64(cc.Empty) / float64(max(tab.Rows, 1)); share > 0.2 {
			c.note(warn, fmt.Sprintf("%s is empty in %d%% of rows.", cc.Found, int(math.Round(share*100))),
				"Empty number cells read as the column's average; empty words read as unknown. Fill in what you can.")
		}
		if cc.FoundKind == string(schema.Text) {
			c.note(info, fmt.Sprintf("%s holds about %s per row.", cc.Found, plural(int(math.Round(cc.Words)), "word", "words")),
				"Text is read word by word. A few words per row is enough to help.")
		}
	}

	// The answers.
	type count struct {
		v string
		n int
	}
	var counts []count
	for v, n := range tab.Answers {
		counts = append(counts, count{v, n})
	}
	slices.SortFunc(counts, func(a, b count) int { return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.v, b.v)) })
	for _, k := range counts[:min(20, len(counts))] {
		c.Answers = append(c.Answers, &mimirv1.AnswerCount{Value: k.v, Count: int64(k.n)})
	}
	if t == nil || t.Kind != Number {
		if tab.Classes != nil {
			low := counts[len(counts)-1]
			switch {
			case low.n < 20:
				c.note(warn, fmt.Sprintf("Only %s say %s.", plural(low.n, "row", "rows"), low.v),
					"That's too few to learn that answer well. Aim for 100 or more."+balance(t))
			case low.n < 100:
				c.note(info, fmt.Sprintf("Only %s say %s.", plural(low.n, "row", "rows"), low.v),
					"That's enough to start, but 100 or more would make it much more reliable."+balance(t))
			}
		}
	} else if tab.Numeric {
		vs := slices.Clone(tab.V)
		slices.Sort(vs)
		median := math.Abs(float64(vs[len(vs)/2]))
		big := 0
		for _, v := range vs {
			if median > 0 && math.Abs(float64(v)) > 10*median {
				big++
			}
		}
		if big > 0 {
			c.note(info, "A few very large numbers.", fmt.Sprintf("%s more than ten times the usual value. Check they aren't typos; they're still used.", plural(big, "row is", "rows are")))
		}
	}

	if len(missing) > 0 {
		c.MissingPhotoCount = int64(len(missing))
		c.MissingPhotos = missing[:min(len(missing), MaxMissingPhotos)]
		c.note(warn, plural(len(missing), "row has", "rows have")+" no photo in the ZIP.",
			"They are left out. Download the list to see which, then add the photos and upload again.")
	}
	return c.finish()
}

// CheckPhotos checks a ZIP of photo folders, against a template when t
// is not nil.
func CheckPhotos(t *Template, folders map[string][]string, loose int) *mimirv1.DataCheck {
	return checkFolders(t, folders, loose, "photo", "photos")
}

// CheckSounds checks a ZIP of sound folders.
func CheckSounds(folders map[string][]string, loose int) *mimirv1.DataCheck {
	return checkFolders(nil, folders, loose, "sound", "sounds")
}

// checkFolders checks a ZIP of folders of one and many, one folder per
// answer.
func checkFolders(t *Template, folders map[string][]string, loose int, one, many string) *mimirv1.DataCheck {
	c := check{&mimirv1.DataCheck{}}
	var names []string
	for name, files := range folders {
		names = append(names, name)
		c.Rows += int64(len(files))
	}
	slices.Sort(names)
	expected := map[string]bool{}
	if t != nil {
		c.RowsMin, c.RowsGood, c.RowsGreat = t.RowsMin, t.RowsGood, t.RowsGreat
		var lost []string
		for _, f := range t.Folders {
			expected[f.Name] = true
			if _, ok := folders[f.Name]; !ok {
				lost = append(lost, f.Name)
			}
		}
		if len(lost) > 0 {
			c.note(info, plural(len(lost), "folder isn't there", "folders aren't there")+": "+strings.Join(lost, ", ")+".",
				"The model only learns the answers it has folders for. That's fine if those don't happen for you.")
		}
	}
	low, lowName := math.MaxInt, ""
	for _, name := range names {
		n := len(folders[name])
		c.Folders = append(c.Folders, &mimirv1.FolderCheck{Name: name, Photos: int64(n), Expected: expected[name]})
		c.Answers = append(c.Answers, &mimirv1.AnswerCount{Value: name, Count: int64(n)})
		if n < low {
			low, lowName = n, name
		}
	}
	switch {
	case len(names) == 0:
		c.note(bad, fmt.Sprintf("No %s in folders.", many), fmt.Sprintf("Put the %s of each answer in a folder named after it, then zip the folders together.", many))
	case len(names) == 1:
		c.note(bad, fmt.Sprintf("All %s are in one folder.", many), fmt.Sprintf("There's only %s, so there's nothing to tell apart. Add a folder for each other answer.", names[0]))
	default:
		need := int64(20)
		if t != nil {
			need = t.RowsMin
		}
		if int64(low) < need {
			c.note(warn, fmt.Sprintf("%s has only %s.", lowName, plural(low, one, many)),
				fmt.Sprintf("Aim for %d or more in every folder, or that answer will be guessed poorly.%s", need, balance(t)))
		}
	}
	if loose > 0 {
		c.note(info, plural(loose, one+" isn't", many+" aren't")+" in a folder.", fmt.Sprintf("They are left out, since a %s's folder is its answer.", one))
	}
	return c.finish()
}

// rowsNote compares how many examples there are with the template's
// targets.
func rowsNote(c check, t *Template, rows int64, unit string) {
	if t == nil || t.RowsMin == 0 {
		return
	}
	switch {
	case rows < t.RowsMin:
		c.note(warn, fmt.Sprintf("%s %s, fewer than this usually needs.", thousands(rows), unit),
			fmt.Sprintf("%s is a good start. It can still learn, but don't rely on its answers yet.", thousands(t.RowsMin)))
	case rows < t.RowsGood:
		c.note(info, fmt.Sprintf("%s %s, enough to start.", thousands(rows), unit),
			fmt.Sprintf("%s would make it better.", thousands(t.RowsGood)))
	}
}

func balance(t *Template) string {
	if t == nil || t.Balance == "" {
		return ""
	}
	return " " + t.Balance
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// kindWord names a column kind in plain words.
func kindWord(k schema.Kind) string {
	switch k {
	case schema.Number:
		return "numbers"
	case schema.Category:
		return "categories"
	case schema.YesNo:
		return "yes or no"
	case schema.Text:
		return "free text"
	case schema.Photo:
		return "photo names"
	}
	return string(k)
}

// thousands writes a count with commas: 12,000.
func thousands(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
