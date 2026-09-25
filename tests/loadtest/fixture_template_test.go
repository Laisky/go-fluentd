package main

import (
	"bytes"
	"testing"
)

func TestPreparedFixtureExactlyMatchesIndependentReference(t *testing.T) {
	for _, p := range []string{"ndjson", "cloudevents", "logs", "metrics", "traces"} {
		for _, size := range []int{0, 1, 4096, 32768} {
			tmp := prepareFixture(p, size)
			for _, id := range []int{0, 1, 9, 10, 9999999, 100000000} {
				got, want := tmp.render(id), makeFixture(p, id, size)
				if !bytes.Equal(got.Body, want.Body) || got.Path != want.Path || got.CT != want.CT {
					t.Fatalf("fixture changed: %s/%d/%d", p, size, id)
				}
			}
			a := tmp.render(7)
			copy(a.Body, []byte("bad"))
			b := tmp.render(7)
			if !bytes.Equal(b.Body, makeFixture(p, 7, size).Body) {
				t.Fatal("shared mutable fixture")
			}
		}
	}
}
