package main

import (
	"strings"
	"testing"
)

func TestEventOracleFastPathAndCanonicalFallback(t *testing.T) {
	for _, p := range []string{"ndjson", "cloudevents"} {
		f := makeFixture(p, 7, 512)
		key := p + ":" + hash([]byte(f.Canonical))
		tr := &trial{lookup: map[string]int{key: 0}}
		variants := [][]byte{f.Body, append([]byte(" \n"), f.Body...)}
		// Construct whitespace-only variant without decoding numbers.
		variants = append(variants, []byte(strings.ReplaceAll(string(f.Body), ",", ",\n")))
		for _, body := range variants {
			got, err := tr.eventKey(p, body)
			if err != nil || got != key {
				t.Fatalf("%s: %v", p, err)
			}
		}
		for _, body := range [][]byte{[]byte("{}{}"), []byte("null"), []byte(strings.Replace(string(f.Body), "9223372036854775807", "9223372036854775806", 1))} {
			got, err := tr.eventKey(p, body)
			if err == nil && got == key {
				t.Fatal("changed payload accepted")
			}
		}
		got, err := tr.eventKey("wrong-route", f.Body)
		if err == nil && got == key {
			t.Fatal("wrong route accepted")
		}
	}
}
