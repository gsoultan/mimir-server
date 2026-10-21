package arch

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gsoultan/mimir/nn"
)

// Layer is one step of a table model someone designed: Dense learns
// patterns with Units numbers, Dropout forgets a Rate of them while it
// learns, so it doesn't memorise.
type Layer struct {
	Kind  string // "dense" or "dropout"
	Units int
	Rate  float64
}

const (
	Dense   = "dense"
	Dropout = "dropout"

	// MaxLayers, MaxUnits and MaxRate bound a design.
	MaxLayers = 8
	MaxUnits  = 1024
	MaxRate   = 0.9
)

// LayersKey is the metadata key of a Mimir Model file that holds a
// designed model's layers, as FormatLayers writes them, so the file
// rebuilds into the same shape wherever it is loaded.
const LayersKey = "mimir.layers"

// ErrBadLayers is what CheckLayers and ParseLayers return for a design
// that can't be built.
var ErrBadLayers = errors.New("arch: bad layers")

// CheckLayers says whether ls can be built: at most MaxLayers steps, at
// least one Dense, units from 1 to MaxUnits and rates below MaxRate.
func CheckLayers(ls []Layer) error {
	if len(ls) > MaxLayers {
		return fmt.Errorf("%w: at most %d steps", ErrBadLayers, MaxLayers)
	}
	dense := false
	for _, l := range ls {
		switch l.Kind {
		case Dense:
			if l.Units < 1 || l.Units > MaxUnits {
				return fmt.Errorf("%w: a learn-patterns step has 1 to %d units", ErrBadLayers, MaxUnits)
			}
			dense = true
		case Dropout:
			if l.Rate <= 0 || l.Rate > MaxRate {
				return fmt.Errorf("%w: a forget step forgets more than 0%% and at most %.0f%%", ErrBadLayers, MaxRate*100)
			}
		default:
			return fmt.Errorf("%w: unknown step %q", ErrBadLayers, l.Kind)
		}
	}
	if len(ls) > 0 && !dense {
		return fmt.Errorf("%w: add at least one learn-patterns step", ErrBadLayers)
	}
	return nil
}

// FormatLayers writes ls as "dense:64,dropout:0.1,dense:32".
func FormatLayers(ls []Layer) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		if l.Kind == Dropout {
			parts[i] = Dropout + ":" + strconv.FormatFloat(l.Rate, 'g', -1, 64)
		} else {
			parts[i] = Dense + ":" + strconv.Itoa(l.Units)
		}
	}
	return strings.Join(parts, ",")
}

// ParseLayers reads what FormatLayers writes; "" is no design.
func ParseLayers(s string) ([]Layer, error) {
	if s == "" {
		return nil, nil
	}
	var out []Layer
	for _, p := range strings.Split(s, ",") {
		kind, v, ok := strings.Cut(p, ":")
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrBadLayers, p)
		}
		switch kind {
		case Dense:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrBadLayers, p)
			}
			out = append(out, Layer{Kind: Dense, Units: n})
		case Dropout:
			r, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: %q", ErrBadLayers, p)
			}
			out = append(out, Layer{Kind: Dropout, Rate: r})
		default:
			return nil, fmt.Errorf("%w: unknown step %q", ErrBadLayers, kind)
		}
	}
	return out, CheckLayers(out)
}

// layerNet is the hidden part of a designed table model, from in numbers
// to however many its last Dense step has.
func layerNet(in int, ls []Layer) ([]nn.Module, int) {
	var out []nn.Module
	for _, l := range ls {
		if l.Kind == Dropout {
			out = append(out, nn.NewDropout(l.Rate))
			continue
		}
		out = append(out, nn.NewLinear(in, l.Units), nn.ReLU())
		in = l.Units
	}
	return out, in
}
