// Copyright (c) 2026 Gembit Soultan Shirazi
// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0 OR PolyForm-Small-Business-1.0.0 OR LicenseRef-Commercial
//
// This file is part of mimir-server, a server that trains, serves and manages mimir models,
// created and owned by Gembit Soultan Shirazi.
// It is free to use for personal and other noncommercial purposes, for
// nonprofits, and for companies with fewer than 100 people and under
// 1,000,000 USD in yearly revenue, under the PolyForm Noncommercial and
// PolyForm Small Business licenses. Any other use needs a commercial
// license from Gembit Soultan Shirazi. See the LICENSE and LICENSING.md files
// at the root of this repository.

package arch

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/gsoultan/mimir/automl"
	"github.com/gsoultan/mimir/io/mim"
	"github.com/gsoultan/mimir/linear"
	"github.com/gsoultan/mimir/tabular"
	"github.com/gsoultan/mimir/tensor"
	"github.com/gsoultan/mimir/tree"

	"github.com/gsoultan/mimir-server/internal/schema"
)

func TestCategoriesFollowTheColumns(t *testing.T) {
	cols := []schema.Column{
		{Name: "price", Kind: schema.Number},
		{Name: "plan", Kind: schema.Category, Values: []string{"basic", "pro", "team"}},
		{Name: "paid", Kind: schema.YesNo},
		{Name: "one", Kind: schema.Category, Values: []string{"only"}},
		{Name: "region", Kind: schema.Category, Values: []string{"n", "s"}},
	}
	got := categories(cols)
	want := []tree.Category{{Feature: 1, Width: 3}, {Feature: 6, Width: 2}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("categories %v, want %v", got, want)
	}
	if categoryOption(cols, tabular.Data{Features: 5}) != nil {
		t.Error("a category option for rows that don't match the columns")
	}
}

func TestBlendSavesAndLoads(t *testing.T) {
	ctx := context.Background()
	d := tabular.Data{Features: 2}
	for i := range 200 {
		a, b := float32(i%17)/8-1, float32(i%11)/5-1
		d.X = append(d.X, a, b)
		d.Y = append(d.Y, float32(map[bool]int{false: 0, true: 1}[a+b > 0]))
	}
	tm, err := tree.GradientBoosting(ctx, d, tabular.Classification, tree.Trees(10), tree.Classes(2))
	if err != nil {
		t.Fatal(err)
	}
	lm, err := linear.Logistic(ctx, d, linear.Classes(2))
	if err != nil {
		t.Fatal(err)
	}
	b := &automl.Blend{Models: []tabular.Model{tm, lm}, Weights: []float64{0.6, 0.4}}
	mod, shape, err := autoModule(b, 2)
	if err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := mim.Save(ctx, &buf, mod, nil); err != nil {
		t.Fatal(err)
	}
	built, err := auto.Build(string(js))
	if err != nil {
		t.Fatal(err)
	}
	f, err := mim.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Load(built); err != nil {
		t.Fatal(err)
	}
	want := b.Predict(d.X)
	got, err := built.Forward(tensor.FromSlice(d.X, d.Rows(), 2)).Softmax(1).Data[float32](ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-4 {
			t.Fatalf("answer %d is %v, want %v", i, got[i], want[i])
		}
	}
}
