package main

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
)

type fixtureSlot struct {
	at, size int
	kind     string
}
type fixtureTemplate struct {
	prototype fixture
	slots     []fixtureSlot
}

// prepareFixture renders only known identity fields into the independently
// generated reference. Large immutable payload text is shared, so sustained
// load does not retain request_count * payload_size bytes or repeatedly invoke
// a JSON encoder in the generator. Tests require byte equality to makeFixture.
func prepareFixture(protocol string, payload int) fixtureTemplate {
	f := makeFixture(protocol, 0, payload)
	t := fixtureTemplate{prototype: f}
	add := func(pattern, token, kind string) {
		at := bytes.Index(f.Body, []byte(pattern))
		if at < 0 {
			panic("missing fixture marker: " + pattern)
		}
		off := bytes.Index([]byte(pattern), []byte(token))
		t.slots = append(t.slots, fixtureSlot{at + off, len(token), kind})
	}
	add("item-00000000 ", "00000000", "padded")
	switch protocol {
	case "ndjson", "cloudevents":
		add(`"bench_id":0`, "0", "decimal")
		add(`"msgid":"0"`, "0", "decimal")
		if protocol == "cloudevents" {
			add(`"id":"0"`, "0", "decimal")
		}
	case "metrics":
		add(`"asInt":"0"`, "0", "decimal")
	case "traces":
		add(`"traceId":"00000000000000000000000000000001"`, "00000000000000000000000000000001", "trace")
		add(`"spanId":"0000000000000001"`, "0000000000000001", "span")
	}
	sort.Slice(t.slots, func(i, j int) bool { return t.slots[i].at < t.slots[j].at })
	return t
}
func (t fixtureTemplate) render(id int) fixture {
	f := t.prototype
	dst := make([]byte, 0, len(f.Body)+64)
	start := 0
	for _, s := range t.slots {
		dst = append(dst, f.Body[start:s.at]...)
		switch s.kind {
		case "decimal":
			dst = strconv.AppendInt(dst, int64(id), 10)
		case "padded":
			dst = fmt.Appendf(dst, "%08d", id)
		case "trace":
			dst = fmt.Appendf(dst, "%032x", id+1)
		case "span":
			dst = fmt.Appendf(dst, "%016x", id+1)
		}
		start = s.at + s.size
	}
	f.Body = append(dst, f.Body[start:]...)
	f.Canonical = "" // only the reference tests need the canonical string
	return f
}
