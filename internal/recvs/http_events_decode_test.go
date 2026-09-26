package recvs

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gofluentd/library/streamformat"
)

func TestEventDecodeScratchOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, format, ct, body string
		h                      http.Header
	}{
		{"ndjson", "ndjson", "application/x-ndjson", "{\"raw世界\":\"café 世界\",\"escape\":\"\\ud83d\\ude00\",\"items\":[{\"nested\":\"private\"}],\"max\":18446744073709551615}\n", nil},
		{"structured", "cloudevents", "application/cloudevents+json", `{"specversion":"1.0","id":"test","source":"/tests","type":"test.event","data":{"value":"世界"}}`, nil},
		{"batch", "cloudevents", "application/cloudevents-batch+json", `[{"specversion":"1.0","id":"test","source":"/tests","type":"test.event","data":["世界",1,true]}]`, nil},
		{"binary-json", "cloudevents", "application/json", `{"nested":{"value":"世界"}}`, http.Header{"Ce-Specversion": {"1.0"}, "Ce-Id": {"binary"}, "Ce-Source": {"/tests"}, "Ce-Type": {"test.event"}}},
		{"binary-opaque", "cloudevents", "application/octet-stream", "\x00\xffprivate-world", http.Header{"Ce-Specversion": {"1.0"}, "Ce-Id": {"opaque"}, "Ce-Source": {"/tests"}, "Ce-Type": {"test.event"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			got, err := decodeEventInputBuffer(&buf, strings.NewReader(tc.body), int64(len(tc.body)), tc.format, tc.ct, tc.h, 10)
			if err != nil {
				t.Fatal(err)
			}
			want, err := streamformat.Decode(tc.format, tc.ct, tc.h, []byte(tc.body), 10)
			if err != nil {
				t.Fatal(err)
			}
			clear(buf.Bytes())
			buf.Reset()
			buf.WriteString(strings.Repeat("overwritten", 8192))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded values alias scratch: %#v != %#v", got, want)
			}
		})
	}
}

func TestEventDecodeScratchRejectsPartialAndOversizedInput(t *testing.T) {
	for _, hint := range []int64{-1, 0, 1, 64 << 10, 1 << 30} {
		t.Run(fmt.Sprint(hint), func(t *testing.T) {
			var buf bytes.Buffer
			for _, body := range []string{"{\"ok\":1}\n{bad}\n", "{\"same\":1,\"same\":2}\n", "{\"surrogate\":\"\\ud800\"}\n", "{}\n{}\n"} {
				records, err := decodeEventInputBuffer(&buf, strings.NewReader(body), hint, "ndjson", "application/x-ndjson", nil, 1)
				if err == nil || len(records) != 0 {
					t.Fatal("invalid batch published a prefix")
				}
			}
			r := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(strings.NewReader("{\"x\":\"long\"}\n")), 4)
			_, err := decodeEventInputBuffer(&buf, r, hint, "ndjson", "application/x-ndjson", nil, 1)
			var limit *http.MaxBytesError
			if !errors.As(err, &limit) {
				t.Fatalf("body limit bypassed: %v", err)
			}
			// The same dirty buffer must accept and exactly decode the next
			// valid body, without either a failed prefix or a stale suffix.
			got, err := decodeEventInputBuffer(&buf, strings.NewReader("{\"next\":true}\n"), hint, "ndjson", "application/x-ndjson", nil, 1)
			if err != nil || !reflect.DeepEqual(got, []map[string]interface{}{{"next": true}}) {
				t.Fatalf("failed read poisoned reuse: %#v %v", got, err)
			}
		})
	}
}

func TestEventDecodeScratchConcurrentReuse(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				value := fmt.Sprintf("private-%d-%d-世界", worker, n)
				body, _ := stdjson.Marshal(map[string]interface{}{"value": value, "nested": map[string]interface{}{"text": value}})
				body = append(body, '\n')
				got, err := decodeEventInput(bytes.NewReader(body), int64(len(body)), "ndjson", "application/x-ndjson", nil, 1)
				if err != nil {
					t.Error(err)
					return
				}
				clear(body)
				// Force another complete parse before inspecting the first.
				_, err = decodeEventInput(strings.NewReader("{\"other\":null}\n"), -1, "ndjson", "application/x-ndjson", nil, 1)
				if err != nil || got[0]["value"] != value || got[0]["nested"].(map[string]interface{})["text"] != value {
					t.Error("cross-request payload contamination")
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestEventInputScratchRetentionCap(t *testing.T) {
	b := new(bytes.Buffer)
	b.Grow(maxRetainedEventInput + 1)
	b.WriteString("large request")
	recycleEventInput(b)
	if b.String() != "large request" {
		t.Fatal("oversized scratch returned to pool")
	}
}

func BenchmarkEventDecodeScratch(b *testing.B) {
	for _, size := range []int{512, 16384, 262144} {
		body := []byte("{\"text\":\"" + strings.Repeat("x", size) + "\"}\n")
		for _, pooled := range []bool{false, true} {
			b.Run(fmt.Sprintf("size-%d/pooled-%t", size, pooled), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var err error
					if pooled {
						_, err = decodeEventInput(bytes.NewReader(body), int64(len(body)), "ndjson", "application/x-ndjson", nil, 1)
					} else {
						var buf bytes.Buffer
						_, err = decodeEventInputBuffer(&buf, bytes.NewReader(body), int64(len(body)), "ndjson", "application/x-ndjson", nil, 1)
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func FuzzEventDecodeScratch(f *testing.F) {
	for _, seed := range [][]byte{[]byte("{\"text\":\"世界\"}\n"), []byte("{\"x\":[1,true,null]}\n{}\n"), []byte("{\"x\":\"\\ud800\"}\n"), nil} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 32768 {
			return
		}
		want, werr := streamformat.Decode("ndjson", "application/x-ndjson", nil, body, 3)
		var scratch bytes.Buffer
		for _, hint := range []int64{-1, 1, int64(len(body))} {
			got, err := decodeEventInputBuffer(&scratch, bytes.NewReader(body), hint, "ndjson", "application/x-ndjson", nil, 3)
			if (err == nil) != (werr == nil) {
				t.Fatalf("decode acceptance changed: %v / %v", err, werr)
			}
			clear(scratch.Bytes())
			scratch.Reset()
			scratch.WriteString("overwritten immediately")
			if err == nil && !reflect.DeepEqual(got, want) {
				t.Fatal("decoded record changed after scratch reuse")
			}
		}
	})
}
