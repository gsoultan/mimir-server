package tables

import "github.com/gsoultan/storm"

// Team keeps a group of people and their things apart from other teams.
type Team struct {
	Record
	Named
}

func (t *Team) Schema(s *storm.Table) {
	s.Col(&t.Name).Unique() // also serves the list, ordered by name
}
