package vecindex

import (
	"fmt"
	"math"
	"sync"
	"testing"
)

func TestEmbed(t *testing.T) {
	t.Parallel()
	v := Embed("Acme Bank, acme BANK; Знахідка")
	if len(v) != Dim {
		t.Fatalf("len = %d, want %d", len(v), Dim)
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if math.Abs(norm-1) > 1e-9 {
		t.Errorf("|v|² = %f, want 1", norm)
	}
	for i, x := range Embed("acme bank знахідка") {
		if x == 0 != (v[i] == 0) {
			t.Fatalf("case and punctuation changed the buckets at %d", i)
		}
	}
	for _, x := range Embed(" ,.; ") {
		if x != 0 {
			t.Fatal("Embed(no words) is not the zero vector")
		}
	}
}

func TestIndex(t *testing.T) {
	t.Parallel()
	var x Index // zero value is usable
	docs := map[string]string{
		"c1": "Acme Bank JSC SOC2 Type II findings",
		"c2": "Northwind Cloud hosting availability incidents",
		"c3": "Merchant tariffs fee NBU requirement",
	}
	for _, id := range []string{"c1", "c2", "c3"} {
		if err := x.Add(id, docs[id]); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Add("c1", "again"); err == nil {
		t.Error("duplicate id accepted")
	}
	if err := x.Add("", "text"); err == nil {
		t.Error("empty id accepted")
	}
	if x.Len() != 3 {
		t.Errorf("Len = %d, want 3", x.Len())
	}

	hits := x.Search("acme findings", 2)
	if len(hits) != 2 || hits[0].ID != "c1" {
		t.Fatalf("Search = %+v, want c1 first, 2 hits", hits)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("hits not sorted by score: %+v", hits)
	}
	all := x.Search("nothing matches here", 0)
	if len(all) != 3 || all[0].ID != "c1" || all[2].ID != "c3" {
		t.Errorf("ties must sort by id, got %+v", all)
	}
}

func TestIndexConcurrent(t *testing.T) {
	t.Parallel()
	x := New()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			_ = x.Add(fmt.Sprintf("c%d", i), "text")
			_ = x.Search("text", 1)
		})
	}
	wg.Wait()
	if x.Len() != 20 {
		t.Errorf("Len = %d, want 20", x.Len())
	}
}
