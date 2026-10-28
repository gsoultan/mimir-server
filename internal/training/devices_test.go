package training

import (
	"context"
	"testing"

	"github.com/gsoultan/mimir/tensor"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/store"
)

func TestTakeDevices(t *testing.T) {
	t.Cleanup(func() { UseDevices(nil) })
	UseDevices(tensor.CPUs(1))
	if devs, _ := takeDevices(); devs != nil || Devices() != 1 {
		t.Fatalf("one device: took %v, Devices %d", devs, Devices())
	}

	UseDevices(tensor.CPUs(3))
	first, release := takeDevices()
	if len(first) != 3 || Devices() != 3 {
		t.Fatalf("took %d of 3", len(first))
	}
	if second, _ := takeDevices(); second != nil {
		t.Fatalf("a second run took %d devices the first holds", len(second))
	}
	release()
	if again, release := takeDevices(); len(again) != 3 {
		t.Fatalf("after release took %d", len(again))
	} else {
		release()
	}
}

// TestFitOnSeveralDevices trains a table run with each batch split between
// two devices, as on a machine with two GPUs, and checks it learns as
// well as on one.
func TestFitOnSeveralDevices(t *testing.T) {
	t.Cleanup(func() { UseDevices(nil) })
	a, _ := arch.Lookup("table-mlp")
	s := TableDefaults
	s.Epochs = 4
	task := Task{Arch: a, Source: Source{Kind: store.DatasetTable, CSV: largeTable(3000)}, Settings: s}

	alone, err := Fit(context.Background(), task, nil)
	if err != nil {
		t.Fatal(err)
	}
	UseDevices(tensor.CPUs(2))
	task.CheckpointDir = t.TempDir() // as the runner trains: checkpoints work alongside
	split, err := Fit(context.Background(), task, nil)
	if err != nil {
		t.Fatal(err)
	}
	if alone.Devices > 1 || split.Devices != 2 {
		t.Fatalf("devices: alone %d, split %d", alone.Devices, split.Devices)
	}
	got, want := split.History[s.Epochs-1].Accuracy, alone.History[s.Epochs-1].Accuracy
	t.Logf("accuracy: one device %.3f, two %.3f", want, got)
	if got < want-0.03 {
		t.Errorf("on two devices it got %.3f right, on one %.3f", got, want)
	}
	if split.Report == nil || len(weightsOf(t, split.Weights)) != len(weightsOf(t, alone.Weights)) {
		t.Errorf("the run on two devices saved a different model")
	}
	// The devices are free again for the next run.
	if devs, release := takeDevices(); len(devs) != 2 {
		t.Errorf("after the run %d devices are free", len(devs))
	} else {
		release()
	}
}
