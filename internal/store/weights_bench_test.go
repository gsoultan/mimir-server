package store

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The two ways a model's weights come off the disk: read whole onto the
// Go heap, or mapped into memory for the model to read in place.
func BenchmarkWeights(b *testing.B) {
	s, id := benchWeights(b)
	b.ResetTimer()
	for range b.N {
		w, err := s.Weights(id)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(w)))
	}
	reportHeap(b)
}

func BenchmarkOpenWeights(b *testing.B) {
	s, id := benchWeights(b)
	b.ResetTimer()
	for range b.N {
		w, err := s.OpenWeights(id)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(w.Bytes())))
		w.Close()
	}
	reportHeap(b)
}

// benchWeights stores a 256 MB weights file and returns its id.
func benchWeights(b *testing.B) (*Store, string) {
	b.Helper()
	dir := b.TempDir()
	s := &Store{weights: filepath.Join(dir, "weights")}
	if err := os.MkdirAll(s.weights, 0o755); err != nil {
		b.Fatal(err)
	}
	id := "00000000-0000-0000-0000-000000000000"
	f, err := os.Create(s.path(id))
	if err != nil {
		b.Fatal(err)
	}
	if err := f.Truncate(256 << 20); err != nil {
		b.Fatal(err)
	}
	f.Close()
	return s, id
}

func reportHeap(b *testing.B) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	b.ReportMetric(float64(ms.HeapAlloc)/(1<<20), "heapMB")
}
