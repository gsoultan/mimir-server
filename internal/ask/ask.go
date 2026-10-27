// Package ask turns a question asked by column name, and a photo, into the
// numbers a model reads. The server and the browser build (cmd/mimir-wasm)
// share it, so a model answers the same question the same way in both.
package ask

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/schema"
)

// ErrBadQuestion is what Encode returns for a question the model can't
// read; its message says why, for people.
var ErrBadQuestion = errors.New("ask: bad question")

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadQuestion, fmt.Sprintf(format, args...))
}

// Reason is err's message for people, without the error's prefix.
func Reason(err error) string {
	return strings.TrimPrefix(err.Error(), ErrBadQuestion.Error()+": ")
}

// Encode turns fields (by column name) and photo, or sound, into the
// numbers a model of architecture a reads, the same way its examples
// were. cols are the model's columns; a model taught before columns had
// kinds has only features, each a number.
func Encode(a arch.Architecture, cols []schema.Column, features []string, fields map[string]string, photo, sound []byte) ([]float32, error) {
	var out []float32
	if a.Input == arch.Sound {
		if len(sound) == 0 {
			return nil, bad("This model listens to a sound. Send one.")
		}
		s, err := schema.DecodeSound(sound)
		if err != nil {
			return nil, bad("That sound can't be read. Send a WAV file.")
		}
		return schema.SoundFloats(out, s), nil
	}
	if a.Input == arch.Image && a.Fixed() || a.Input == arch.Tensor {
		return nil, bad("This model reads numbers; send them as input.")
	}
	if a.Input == arch.Image || a.Input == arch.PhotoTable {
		if len(photo) == 0 {
			return nil, bad("This model looks at a photo. Send one.")
		}
		px, err := schema.DecodePhoto(photo)
		if err != nil {
			return nil, bad("That photo can't be read. Send a JPG, PNG or GIF.")
		}
		out = schema.Floats(out, px)
	}
	if a.Input == arch.Image {
		return out, nil
	}
	if len(cols) == 0 {
		for _, f := range features {
			cols = append(cols, schema.Column{Name: f, Kind: schema.Number})
		}
	}
	for name := range fields {
		if !slices.ContainsFunc(cols, func(c schema.Column) bool { return c.Name == name }) {
			return nil, bad("This model has no column called %q.", name)
		}
	}
	for _, c := range cols {
		if v := strings.TrimSpace(fields[c.Name]); c.Kind == schema.Number && v != "" {
			if _, ok := schema.ParseNumber(v); !ok {
				return nil, bad("%s needs a number; %q isn't one.", c.Name, v)
			}
		}
	}
	return schema.Encode(out, cols, func(i int) string { return fields[cols[i].Name] }), nil
}
