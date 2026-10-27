package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/gsoultan/mimir-server/internal/store/db/model"
	"github.com/gsoultan/mimir-server/internal/store/db/modelversion"
)

func TestVersions(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	first := []byte("weights one")
	m, err := s.Create(ctx, Model{Name: "fruit", Architecture: "table-mlp", JobID: "job-1", Accuracy: 0.8, Labels: []string{"apple", "pear"}}, first)
	if err != nil {
		t.Fatal(err)
	}
	vs, err := s.ListVersions(ctx, m.ID)
	if err != nil || len(vs) != 1 || vs[0].Number != 1 || !vs[0].Live || vs[0].Source != VersionTrained || vs[0].ID != m.VersionID || vs[0].SHA256 != m.SHA256 {
		t.Fatalf("ListVersions after Create = %+v, %v", vs, err)
	}

	// A better pass goes live; the first stays as version 1.
	second := []byte("weights two")
	v2, err := s.AddVersion(ctx, m.ID, Model{JobID: "job-2", Accuracy: 0.9, Labels: []string{"apple", "pear", "plum"}}, second, true, "")
	if err != nil || v2.Number != 2 || v2.Source != VersionKeepLearning {
		t.Fatalf("AddVersion live = %+v, %v", v2, err)
	}
	got, _ := s.Get(ctx, m.ID)
	if got.VersionID != v2.ID || got.Accuracy != 0.9 || len(got.Labels) != 3 || got.Name != "fruit" {
		t.Fatalf("model after a live version = %+v", got)
	}
	if b, _ := s.Weights(m.ID); !bytes.Equal(b, second) {
		t.Fatalf("live weights = %q", b)
	}

	// A worse pass is kept but not put live.
	v3, err := s.AddVersion(ctx, m.ID, Model{JobID: "job-3", Accuracy: 0.5}, []byte("weights three"), false, "worse")
	if err != nil || v3.Number != 3 || v3.Live {
		t.Fatalf("AddVersion kept = %+v, %v", v3, err)
	}
	if got, _ := s.Get(ctx, m.ID); got.VersionID != v2.ID {
		t.Fatalf("a kept version went live: %+v", got)
	}
	vs, _ = s.ListVersions(ctx, m.ID)
	if len(vs) != 3 || vs[0].Number != 3 || vs[0].Note != "worse" || vs[0].Live || !vs[1].Live {
		t.Fatalf("ListVersions = %+v", vs)
	}

	// Going back to version 1 brings its weights and what it reads back.
	back, err := s.UseVersion(ctx, m.ID, vs[2].ID)
	if err != nil || back.VersionID != vs[2].ID || back.Accuracy != 0.8 || len(back.Labels) != 2 || back.SHA256 != m.SHA256 {
		t.Fatalf("UseVersion = %+v, %v", back, err)
	}
	if b, _ := s.Weights(m.ID); !bytes.Equal(b, first) {
		t.Fatalf("weights after going back = %q", b)
	}
	// Writing the live file must not have touched the stored version.
	if b, _ := os.ReadFile(s.versionPath(v2.ID)); !bytes.Equal(b, second) {
		t.Fatalf("version 2 = %q", b)
	}

	other, err := s.Create(ctx, Model{Name: "other", Architecture: "mnist-mlp"}, []byte("other"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseVersion(ctx, other.ID, v2.ID); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("UseVersion of another model's version: %v", err)
	}

	if err := s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if _, err := os.Stat(s.versionPath(v.ID)); !os.IsNotExist(err) {
			t.Fatalf("version %d's file survived delete: %v", v.Number, err)
		}
	}
}

func TestVersionsPruneAndBackfill(t *testing.T) {
	ctx := context.Background()
	s := open(t)

	m, err := s.Create(ctx, Model{Name: "old", Architecture: "mnist-mlp"}, []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	// A model made before versions were kept has none.
	u := model.MutateKey(m.ID)
	u.SetVersionID("")
	if err := u.Update(ctx, s.ex); err != nil {
		t.Fatal(err)
	}
	if _, err := modelversion.New().Where(modelversion.ModelID.Eq(m.ID)).DeleteAll(ctx, s.ex); err != nil {
		t.Fatal(err)
	}
	vs, err := s.ListVersions(ctx, m.ID)
	if err != nil || len(vs) != 1 || !vs[0].Live || vs[0].Source != VersionUploaded {
		t.Fatalf("ListVersions of an old model = %+v, %v", vs, err)
	}

	// Version 1 is live and stays; the oldest of the rest go.
	if _, err := s.UseVersion(ctx, m.ID, vs[0].ID); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= KeptVersions+3; i++ {
		if _, err := s.AddVersion(ctx, m.ID, Model{}, []byte("v"+strconv.Itoa(i)), false, ""); err != nil {
			t.Fatal(err)
		}
	}
	vs, _ = s.ListVersions(ctx, m.ID)
	if len(vs) != KeptVersions+1 || vs[0].Number != KeptVersions+3 || vs[len(vs)-1].Number != 1 || !vs[len(vs)-1].Live {
		t.Fatalf("after pruning: %d versions, newest %d, oldest %d", len(vs), vs[0].Number, vs[len(vs)-1].Number)
	}
	if vs[len(vs)-2].Number != 4 {
		t.Fatalf("oldest kept besides the live one = %d, want 4", vs[len(vs)-2].Number)
	}
}
