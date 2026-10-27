package training

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/gsoultan/mimir/audio"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/ask"
	"github.com/gsoultan/mimir-server/internal/runtime"
	"github.com/gsoultan/mimir-server/internal/schema"
	"github.com/gsoultan/mimir-server/internal/store"
)

// tone is a WAV of a noisy tone of hz, of the given rate and length.
func tone(r *rand.Rand, hz float64, rate int, seconds float64) []byte {
	c := audio.Clip{Rate: rate, Samples: make([]float32, int(float64(rate)*seconds))}
	phase := r.Float64() * 2 * math.Pi
	for i := range c.Samples {
		c.Samples[i] = float32(0.5*math.Sin(phase+2*math.Pi*hz*float64(i)/float64(rate)) + 0.1*r.NormFloat64())
	}
	var b bytes.Buffer
	audio.WriteWAV(&b, c)
	return b.Bytes()
}

// tones writes a ZIP of n sounds, a low hum or a high whistle, in a
// folder named after it, at rates and lengths that vary.
func tones(n int) []byte {
	r := rand.New(rand.NewPCG(7, 8))
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	rates := []int{8000, 16000, 22050, 44100}
	for i := range n {
		name, hz := "hum", 150+r.Float64()*100
		if i%2 == 1 {
			name, hz = "whistle", 2000+r.Float64()*1000
		}
		f, _ := z.Create(fmt.Sprintf("sounds/%s/%04d.wav", name, i))
		f.Write(tone(r, hz, rates[i%len(rates)], 0.5+r.Float64()))
	}
	z.Close()
	return b.Bytes()
}

func TestSoundsTrainAndAnswer(t *testing.T) {
	if testing.Short() {
		t.Skip("trains a sound network")
	}
	ctx := context.Background()
	a, ok := arch.Lookup("sound")
	if !ok {
		t.Fatal("no sound architecture")
	}
	if !slicesContain(Architectures(store.Dataset{Kind: store.DatasetSounds}), "sound") {
		t.Fatal("sounds don't offer the sound architecture")
	}
	src := Source{Kind: store.DatasetSounds, CSV: tones(160)}
	res, err := Fit(ctx, Task{Arch: a, Source: src, Settings: store.Settings{Epochs: 6, BatchSize: 16, LearningRate: 3e-3, HoldOut: 0.25}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Spec.Labels; len(got) != 2 || got[0] != "hum" || got[1] != "whistle" {
		t.Fatalf("labels %v", got)
	}
	last := res.History[len(res.History)-1]
	t.Logf("held-out accuracy %.2f", last.Accuracy)
	if last.Accuracy < 0.9 {
		t.Errorf("tells a hum from a whistle %.0f%% of the time, want 90%%", 100*last.Accuracy)
	}

	// A question is a WAV file, of any rate.
	cache := runtime.NewCache(1 << 30)
	load := func() ([]byte, error) { return res.Weights, nil }
	r := rand.New(rand.NewPCG(1, 2))
	for want, hz := range map[string]float64{"hum": 200, "whistle": 2500} {
		in, err := ask.Encode(a, nil, nil, nil, nil, tone(r, hz, 48000, 2))
		if err != nil {
			t.Fatal(err)
		}
		if len(in) != schema.SoundSamples {
			t.Fatalf("%d samples", len(in))
		}
		out, err := cache.Predict(ctx, "m", "1", a, res.Spec, load, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := out.Top(res.Spec.Labels, 1)[0].Label; got != want {
			t.Errorf("a %v Hz tone heard as %s, want %s", hz, got, want)
		}
	}
	if _, err := ask.Encode(a, nil, nil, nil, nil, []byte("not a sound")); err == nil {
		t.Error("a file that isn't a WAV was read")
	}
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
