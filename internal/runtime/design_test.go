package runtime_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/gsoultan/mimir/io/mim"

	"github.com/gsoultan/mimir-server/internal/arch"
	"github.com/gsoultan/mimir-server/internal/runtime"
)

// TestDesignedBuild saves a designed table model and builds it again from
// the file alone, the way a server or worker that never saw the run does.
func TestDesignedBuild(t *testing.T) {
	a, _ := arch.Lookup("table-mlp")
	design, err := arch.ParseLayers("dense:12,dropout:0.25,dense:5")
	if err != nil || arch.FormatLayers(design) != "dense:12,dropout:0.25,dense:5" {
		t.Fatalf("ParseLayers = %v, %v", design, err)
	}
	spec := arch.Spec{InputShape: []int{3}, Labels: []string{"a", "b"}, Layers: design}
	save := func(opts *mim.SaveOptions) []byte {
		var buf bytes.Buffer
		if err := mim.Save(context.Background(), &buf, a.New(spec), opts); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	plain := arch.Spec{InputShape: spec.InputShape, Labels: spec.Labels}
	if _, err := runtime.Build(a, plain, save(&mim.SaveOptions{Metadata: map[string]any{arch.LayersKey: "dense:12,dropout:0.25,dense:5"}})); err != nil {
		t.Fatalf("designed file: %v", err)
	}
	// Without its layers the file doesn't fit the usual shape.
	if _, err := runtime.Build(a, plain, save(nil)); !errors.Is(err, runtime.ErrBadWeights) {
		t.Fatalf("file without its layers: %v", err)
	}
	for _, bad := range []string{"dense:0", "dropout:0.5", "dense:4,pool:2", "dense"} {
		if _, err := arch.ParseLayers(bad); !errors.Is(err, arch.ErrBadLayers) {
			t.Errorf("ParseLayers(%q) = %v", bad, err)
		}
	}
}
