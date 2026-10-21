package store

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxName and MaxDescription are the most characters a display name and a
// model's description hold: the limits the tables' CHECK constraints keep.
const (
	MaxName        = 100
	MaxDescription = 500
)

// InvalidError reports a value the store's rules refuse. Its message is
// written for the person who typed the value.
type InvalidError struct{ msg string }

func (e *InvalidError) Error() string { return e.msg }

func invalidf(format string, args ...any) error {
	return &InvalidError{msg: fmt.Sprintf(format, args...)}
}

// Name is a display name of a team, person, model, key, run, webhook or
// worker: trimmed, valid UTF-8, 1 to MaxName characters. Only ParseName
// makes one, so a Name in hand is one the database accepts.
type Name struct{ s string }

// ParseName checks s as the name of a thing, such as "model" or "team",
// which the refusal names.
func ParseName(thing, s string) (Name, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxName {
		return Name{}, invalidf("Give the %s a name of up to %d characters.", thing, MaxName)
	}
	return Name{s: s}, nil
}

func (n Name) String() string { return n.s }

// Description is a model's or run's description: trimmed, valid UTF-8, at
// most MaxDescription characters, and possibly empty.
type Description struct{ s string }

// ParseDescription checks s as a description.
func ParseDescription(s string) (Description, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxDescription {
		return Description{}, invalidf("Descriptions can be at most %d characters of text.", MaxDescription)
	}
	return Description{s: s}, nil
}

func (d Description) String() string { return d.s }
