package training

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

// largeTable writes a CSV of rows examples: 16 numbers, a category and a
// class that depends on them, so a model has something real to learn.
func largeTable(rows int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	for j := range 16 {
		fmt.Fprintf(&b, "f%d,", j)
	}
	b.WriteString("color,label\n")
	colors := []string{"red", "green", "blue", "gray"}
	for range rows {
		s := 0.0
		for j := range 16 {
			v := r.NormFloat64()*10 + float64(j)
			if j < 4 {
				s += v - float64(j)
			}
			b.WriteString(strconv.FormatFloat(v, 'f', 3, 64))
			b.WriteByte(',')
		}
		c := r.IntN(len(colors))
		s += float64(c) * 5
		label := "low"
		switch {
		case s > 15:
			label = "high"
		case s > -5:
			label = "mid"
		}
		b.WriteString(colors[c])
		b.WriteByte(',')
		b.WriteString(label)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// TestLargeTable trains on a large generated table and reports time,
// memory and CPU. It runs only when MIMIR_LARGE_ROWS is set; for example
//
//	MIMIR_LARGE_ROWS=500000 MIMIR_LARGE_EPOCHS=3 go test -run TestLargeTable -v ./internal/training
//
// MIMIR_LARGE_BATCH sets the batch size, which otherwise is the one
// SettingsFor picks.
func TestLargeTable(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("MIMIR_LARGE_ROWS"))
	if n == 0 {
		t.Skip("set MIMIR_LARGE_ROWS to run")
	}
	csv := largeTable(n)
	epochs, _ := strconv.Atoi(os.Getenv("MIMIR_LARGE_EPOCHS"))
	s := SettingsFor(store.Dataset{Kind: store.DatasetTable, Rows: int64(n)})
	s.Epochs = max(1, epochs)
	if b, _ := strconv.Atoi(os.Getenv("MIMIR_LARGE_BATCH")); b > 0 {
		s.BatchSize = b
	}
	a, _ := arch.Lookup("table-mlp")
	runtime.GC()
	start := time.Now()
	done, peaked := make(chan struct{}), make(chan uint64)
	go func() {
		var m runtime.MemStats
		var peak uint64
		for {
			select {
			case <-done:
				peaked <- peak
				return
			case <-time.After(50 * time.Millisecond):
			}
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapInuse)
		}
	}()
	var loadTime time.Duration
	res, err := Fit(context.Background(), Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: csv}, Settings: s},
		func(epoch, step, steps int, h []store.Epoch) {
			if loadTime == 0 {
				loadTime = time.Since(start)
			}
		})
	close(done)
	peak := <-peaked
	if err != nil {
		t.Fatal(err)
	}
	last := res.History[len(res.History)-1]
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	cpu := time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	t.Logf("cpu=%v maxRSS=%dMB cores-busy=%.2f", cpu.Round(time.Millisecond), ru.Maxrss/1024, cpu.Seconds()/time.Since(start).Seconds())
	t.Logf("rows=%d csv=%.1fMB load=%v total=%v peakHeap=%.0fMB acc=%.3f loss=%.4f epochMs=%v",
		n, float64(len(csv))/1e6, loadTime.Round(time.Millisecond), time.Since(start).Round(time.Millisecond),
		float64(peak)/1e6, last.Accuracy, last.Loss, func() (ms []int64) {
			for _, e := range res.History {
				ms = append(ms, e.Millis)
			}
			return
		}())
}
