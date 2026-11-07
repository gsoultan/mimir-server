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

package templates

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/gsoultan/mimir-server/internal/training"
)

// notes checks csv with no template and returns its notes, one per line.
func notes(t *testing.T, csv string) string {
	t.Helper()
	tab, err := training.ParseTable(strings.NewReader(csv), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for _, n := range CheckTable(nil, tab, nil, nil).Notes {
		fmt.Fprintf(&b, "%s %s\n", n.Title, n.Body)
	}
	return b.String()
}

func TestSuggest(t *testing.T) {
	var orders strings.Builder
	orders.WriteString("customer_id,customer_category,latitude,longitude,quantity,product\n")
	for i := range 60 {
		fmt.Fprintf(&orders, "C-%d,%s,-6.%d,106.%d,%d,P-%d\n", i%20, []string{"bakery", "cafe"}[i%2], i, i, 1+i%7, i%9)
	}
	got := notes(t, orders.String())
	for _, want := range []string{"Recommend from nearby customers", "The 10 products each customer is", "Groups of similar rows"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Add a column") {
		t.Errorf("asks for a column it has:\n%s", got)
	}

	// Without a place, it says what a place would give.
	var plain strings.Builder
	plain.WriteString("customer,quantity,product\n")
	for i := range 60 {
		fmt.Fprintf(&plain, "C-%d,%d,P-%d\n", i%20, 1+i%7, i%9)
	}
	if got := notes(t, plain.String()); !strings.Contains(got, "latitude and longitude of each customer") {
		t.Errorf("no ask for a place in\n%s", got)
	}

	// A yes or no answer says how often it is yes, and the templates
	// that fit.
	tp, _ := Get("account-churn")
	_, _, b, _ := Example(tp)
	got = notes(t, string(b))
	if !strings.Contains(got, "of these rows say") || !strings.Contains(got, tp.Name) {
		t.Errorf("no answer share or template in\n%s", got)
	}
}

// A table with only a template's needed columns, plus one of its own,
// hears which optional columns would help and that its own one is used.
func TestOptionalAndOwnColumns(t *testing.T) {
	tp, _ := Get("account-churn")
	var keep []Column
	for _, c := range tp.Inputs() {
		if !c.Optional {
			keep = append(keep, c)
		}
	}
	if len(keep) == len(tp.Inputs()) {
		t.Fatal("account-churn has no optional columns")
	}
	var csv strings.Builder
	for _, c := range keep {
		csv.WriteString(c.Name + ",")
	}
	csv.WriteString("region," + tp.Answer().Name + "\n")
	for i := range 80 {
		for _, c := range keep {
			csv.WriteString(c.Examples[i%len(c.Examples)] + ",")
		}
		fmt.Fprintf(&csv, "%s,%s\n", []string{"north", "south"}[i%2], []string{"yes", "no"}[i%3/2])
	}
	tab, err := training.ParseTable(strings.NewReader(csv.String()), tp.Answer().Name, nil)
	if err != nil {
		t.Fatal(err)
	}
	ck := CheckTable(&tp, tab, tp.Match(tab.Header, nil), nil)
	var got strings.Builder
	for _, n := range ck.Notes {
		fmt.Fprintf(&got, "%s %s\n", n.Title, n.Body)
	}
	for _, want := range []string{"More columns make answers more specific: months_customer", "Your own columns are used too: region"} {
		if !strings.Contains(got.String(), want) {
			t.Errorf("no %q in\n%s", want, got.String())
		}
	}
	if strings.Contains(got.String(), "columns aren't there") || ck.Verdict == "bad" {
		t.Errorf("optional columns count as lost, verdict %s:\n%s", ck.Verdict, got.String())
	}
}
