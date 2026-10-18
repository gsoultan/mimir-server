package tables

import "github.com/gsoultan/storm"

// Setting is a small key/value pair, such as alert and retention choices.
type Setting struct {
	Key   string
	Value string
}

func (s *Setting) Schema(t *storm.Table) { t.PrimaryKey(&s.Key) }
