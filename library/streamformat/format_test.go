package streamformat_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"gofluentd/library/streamformat"
)

const event = `{"specversion":"1.0","id":"caller-42","source":"/tests","type":"example.test","data":{"n":9007199254740993,"u":18446744073709551615,"nested":[true,null,"世界"]},"traceparent":"00-abcdef"}`

func TestJSONDataDomain(t *testing.T) {
	v, e := streamformat.JSON([]byte(`{"n":9007199254740993,"max":18446744073709551615,"min":-9223372036854775808,"f":1.25,"zero":0e20,"a":[null,true,"\ud83d\ude00"]}`))
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]interface{}{"n": int64(9007199254740993), "max": uint64(math.MaxUint64), "min": int64(math.MinInt64), "f": 1.25, "zero": float64(0), "a": []interface{}{nil, true, "😀"}}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("%#v", v)
	}
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":{"x":1,"x":2}}`, `{} {}`, `{"x":18446744073709551616}`, `{"x":-9223372036854775809}`, `{"x":1e999}`, `{"x":1e-999}`, `{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0061"}`, "{\"x\":\"\xff\"}", `[}`, `{"x":`, strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66)} {
		if _, e = streamformat.JSON([]byte(raw)); e == nil {
			t.Errorf("accepted invalid input %q", raw)
		}
	}
}

func TestNDJSONIndependentWireContract(t *testing.T) {
	raw := []byte("\r\n{\"id\":1,\"text\":\"a\\nb\"}\r\n\n{\"id\":9007199254740993}\n")
	records, e := streamformat.Decode(streamformat.NDJSON, "application/x-ndjson; charset=UTF-8", nil, raw, 2)
	if e != nil || len(records) != 2 {
		t.Fatalf("%v %v", records, e)
	}
	if records[0]["text"] != "a\nb" || records[1]["id"] != int64(9007199254740993) {
		t.Fatal(records)
	}
	body, h, e := streamformat.Encode(streamformat.NDJSON, "", records)
	if e != nil || h.Get("Content-Type") != "application/x-ndjson" {
		t.Fatal(h, e)
	}
	if string(body) != "{\"id\":1,\"text\":\"a\\nb\"}\n{\"id\":9007199254740993}\n" {
		t.Fatalf("wire: %s", body)
	}
	cases := []struct {
		ct, body string
		limit    int
	}{
		{"application/json", "{}\n", 2}, {"application/x-ndjson; charset=latin1", "{}\n", 2},
		{"application/x-ndjson", "{}", 2}, {"application/x-ndjson", "{}\nBAD\n", 2},
		{"application/x-ndjson", "null\n", 2}, {"application/x-ndjson", "[]\n", 2},
		{"application/x-ndjson", "{}\r {}\n", 2}, {"application/x-ndjson", "{}\n{}\n", 1},
		{"application/x-ndjson", "{}\n", 0},
	}
	for _, tc := range cases {
		r, e := streamformat.Decode(streamformat.NDJSON, tc.ct, nil, []byte(tc.body), tc.limit)
		if e == nil || r != nil {
			t.Errorf("partially accepted %q: %v %v", tc.body, r, e)
		}
	}
}

func TestCloudEventsAllModes(t *testing.T) {
	r, e := streamformat.Decode(streamformat.CloudEvents, "application/cloudevents+json", nil, []byte(event), 10)
	if e != nil {
		t.Fatal(e)
	}
	original, _ := json.Marshal(r)
	for _, mode := range []string{"", streamformat.Structured, streamformat.Batch, streamformat.Binary} {
		body, h, e := streamformat.Encode(streamformat.CloudEvents, mode, r)
		if e != nil {
			t.Fatal(e)
		}
		if mode == streamformat.Binary {
			if h.Get("Ce-Id") != "caller-42" || h.Get("Ce-Source") != "/tests" || h.Get("Content-Type") != "application/json" || !bytes.Contains(body, []byte("9007199254740993")) {
				t.Fatal(h, string(body))
			}
		} else {
			var got interface{}
			d := json.NewDecoder(bytes.NewReader(body))
			d.UseNumber()
			if e = d.Decode(&got); e != nil {
				t.Fatal(e)
			}
			if mode == streamformat.Batch {
				got = got.([]interface{})[0]
			}
			m := got.(map[string]interface{})
			if m["id"] != "caller-42" || m["traceparent"] != "00-abcdef" || m["data"].(map[string]interface{})["u"] != json.Number("18446744073709551615") {
				t.Fatal(got)
			}
		}
	}
	after, _ := json.Marshal(r)
	if !bytes.Equal(original, after) {
		t.Fatal("encoder mutated caller event")
	}
	r, e = streamformat.Decode(streamformat.CloudEvents, "application/cloudevents-batch+json", nil, []byte("[]"), 1)
	if e != nil || len(r) != 0 {
		t.Fatal(r, e)
	}
	// Additional ce-* headers on structured-mode messages are permitted, but the body is authoritative.
	r, e = streamformat.Decode(streamformat.CloudEvents, "application/cloudevents+json", http.Header{"Ce-Id": {"other"}}, []byte(event), 1)
	if e != nil || r[0]["id"] != "caller-42" {
		t.Fatal(r, e)
	}
}

func TestBinaryCloudEventsBytesAndHeaderEscapes(t *testing.T) {
	h := http.Header{"Ce-Specversion": {"1.0"}, "Ce-Id": {"id+%2520"}, "Ce-Type": {"example.test"}, "Ce-Source": {"/tests"}, "Ce-Subject": {`"quoted\" value"`}, "Ce-Custom": {"%E4%B8%96%E7%95%8C"}}
	raw := []byte{0, 255, 10, 13, 0}
	r, e := streamformat.Decode(streamformat.CloudEvents, "application/octet-stream", h, raw, 1)
	if e != nil {
		t.Fatal(e)
	}
	if r[0]["data_base64"] != "AP8KDQA=" || r[0]["id"] != "id+%20" || r[0]["subject"] != "quoted\" value" || r[0]["custom"] != "世界" {
		t.Fatal(r)
	}
	body, out, e := streamformat.Encode(streamformat.CloudEvents, streamformat.Binary, r)
	if e != nil || !bytes.Equal(body, raw) || out.Get("Ce-Id") != "id+%2520" || out.Get("Ce-Subject") != "quoted%22%20value" || out.Get("Ce-Custom") != "%E4%B8%96%E7%95%8C" {
		t.Fatal(out, body, e)
	}
	r, e = streamformat.Decode(streamformat.CloudEvents, "", h, nil, 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := r[0]["data"]; ok {
		t.Fatal("invented empty data")
	}
	r, e = streamformat.Decode(streamformat.CloudEvents, "text/json", h, []byte(`null`), 1)
	if e != nil {
		t.Fatal(e)
	}
	if v, ok := r[0]["data"]; !ok || v != nil {
		t.Fatal("lost explicit null")
	}
	for _, edit := range []func(http.Header){
		func(h http.Header) { h["Ce-Id"] = []string{"a", "b"} },
		func(h http.Header) { h.Set("Ce-Id", "%C0%A0") }, func(h http.Header) { h.Set("Ce-Id", "%XX") },
		func(h http.Header) { h.Set("Ce-Id", "%0A") }, func(h http.Header) { h.Set("Ce-Datacontenttype", "application/json") },
		func(h http.Header) { h.Set("Ce-Data", "bad") }, func(h http.Header) { h.Set("Ce-Id", `"x"; other=y`) },
	} {
		copy := h.Clone()
		edit(copy)
		if _, e := streamformat.Decode(streamformat.CloudEvents, "text/plain", copy, raw, 1); e == nil {
			t.Fatal("invalid headers accepted", copy)
		}
	}
}

func TestCloudEventsInvalidEnvelopeDoesNotPartiallyDecode(t *testing.T) {
	base := func() map[string]interface{} {
		v, e := streamformat.JSON([]byte(event))
		if e != nil {
			t.Fatal(e)
		}
		return v.(map[string]interface{})
	}
	edits := []func(map[string]interface{}){
		func(m map[string]interface{}) { delete(m, "source") }, func(m map[string]interface{}) { m["id"] = "" }, func(m map[string]interface{}) { m["specversion"] = "0.3" },
		func(m map[string]interface{}) { m["source"] = "%" }, func(m map[string]interface{}) { m["time"] = "yesterday" },
		func(m map[string]interface{}) { m["dataschema"] = "/relative" }, func(m map[string]interface{}) { m["datacontenttype"] = "bad type" },
		func(m map[string]interface{}) { m["BadKey"] = "x" }, func(m map[string]interface{}) { m["custom"] = map[string]interface{}{} },
		func(m map[string]interface{}) { m["custom"] = 1.5 }, func(m map[string]interface{}) { m["custom"] = int64(math.MaxInt32) + 1 },
		func(m map[string]interface{}) { m["subject"] = "hello\n" }, func(m map[string]interface{}) { m["data_base64"] = "YQ==" },
		func(m map[string]interface{}) { delete(m, "data"); m["data_base64"] = "%%%" },
		func(m map[string]interface{}) { m["datacontenttype"] = "text/plain" },
	}
	for i, edit := range edits {
		m := base()
		edit(m)
		b, _ := json.Marshal(m)
		body := []byte("[" + event + "," + string(b) + "]")
		got, e := streamformat.Decode(streamformat.CloudEvents, "application/cloudevents-batch+json", nil, body, 10)
		if e == nil || got != nil {
			t.Errorf("case %d accepted %v %v", i, got, e)
		}
	}
	for _, mode := range []string{streamformat.Structured, streamformat.Binary, "invalid"} {
		if _, _, e := streamformat.Encode(streamformat.CloudEvents, mode, nil); e == nil {
			t.Fatal(mode)
		}
	}
	for _, ct := range []string{"application/cloudevents+avro", "application/cloudevents+json; charset=latin1", "application/cloudevents+json;broken"} {
		if _, e := streamformat.Decode(streamformat.CloudEvents, ct, nil, []byte(event), 1); e == nil {
			t.Fatal(ct)
		}
	}
	if _, _, e := streamformat.Encode(streamformat.NDJSON, "", []map[string]interface{}{{"x": make(chan int)}}); e == nil {
		t.Fatal("unsupported data accepted")
	}
	if _, _, e := streamformat.Encode("unknown", "", nil); e == nil {
		t.Fatal("unknown format")
	}
}

func FuzzJSON(f *testing.F) {
	for _, s := range []string{event, `{"x":9007199254740993}`, `{"x":"\ud800"}`, `null`, `[1,2]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<16 {
			t.Skip()
		}
		v, e := streamformat.JSON(b)
		if e != nil {
			return
		}
		out, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = streamformat.JSON(out); e != nil {
			t.Fatal(e)
		}
	})
}
