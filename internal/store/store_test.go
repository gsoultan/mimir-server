package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	weights := []byte("weights")
	m, err := s.Create(ctx, Model{Name: "digits", Architecture: "mnist-mlp"}, weights)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(m.ID) || m.SizeBytes != int64(len(weights)) {
		t.Fatalf("Create = %+v", m)
	}

	got, err := s.Get(ctx, m.ID)
	if err != nil || got.Name != "digits" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	b, err := s.Weights(m.ID)
	if err != nil || !bytes.Equal(b, weights) {
		t.Fatalf("Weights = %q, %v", b, err)
	}

	if _, err := s.Update(ctx, m.ID, "digits v2", "better"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, DefaultTeam)
	if err != nil || len(list) != 1 || list[0].Name != "digits v2" || list[0].Description != "better" {
		t.Fatalf("List = %+v, %v", list, err)
	}

	if err := s.Delete(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if _, err := s.Weights(m.ID); err == nil {
		t.Fatal("weights file survived delete")
	}
	if err := s.Delete(ctx, m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete: %v", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Create(ctx, Model{Name: "a", Architecture: "mnist-mlp"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Get(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
}
