package schema

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/gsoultan/mimir/audio"
)

// Every sound is read as SoundSamples samples, one second at SoundRate a
// second: a longer clip keeps its middle second, a shorter one is padded
// with silence.
const (
	SoundRate    = 16000
	SoundSamples = SoundRate
)

// MaxSoundBytes bounds one sound file.
const MaxSoundBytes = 20 << 20

// ErrBadSound reports a file that isn't a sound the server can read.
var ErrBadSound = errors.New("schema: not a WAV sound")

// IsSoundName reports whether a file name looks like a sound.
func IsSoundName(name string) bool {
	return strings.EqualFold(path.Ext(name), ".wav")
}

// DecodeSound reads a WAV file and returns its SoundSamples samples, as
// 16-bit numbers to keep many in memory.
func DecodeSound(b []byte) ([]int16, error) {
	if len(b) > MaxSoundBytes {
		return nil, fmt.Errorf("%w: bigger than %d MB", ErrBadSound, MaxSoundBytes>>20)
	}
	c, err := audio.ReadWAV(bytes.NewReader(b))
	if err != nil || c.Rate < 1 {
		return nil, fmt.Errorf("%w: %v", ErrBadSound, err)
	}
	// Only the middle second is kept, so only it, and a little either
	// side for the resampling filter, is resampled.
	if keep := c.Rate + c.Rate/10; len(c.Samples) > keep {
		start := (len(c.Samples) - keep) / 2
		c.Samples = c.Samples[start : start+keep]
	}
	c = c.Resample(SoundRate).Fit(SoundSamples)
	out := make([]int16, SoundSamples)
	for i, v := range c.Samples {
		out[i] = int16(math.Round(float64(max(-1, min(1, v))) * math.MaxInt16))
	}
	return out, nil
}

// SoundFloats appends samples to dst as numbers from -1 to 1.
func SoundFloats(dst []float32, samples []int16) []float32 {
	for _, s := range samples {
		dst = append(dst, float32(s)/math.MaxInt16)
	}
	return dst
}
