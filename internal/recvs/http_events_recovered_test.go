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

func TestIndependentReceiverScratchOwnsAllFormats(t *testing.T) {
	for _, tc := range []struct {
		name, format, ct, body string
		headers                http.Header
	}{
		{"ndjson", streamformat.NDJSON, "application/x-ndjson", "{\"escaped\":\"caf\\u00e9\",\"世界.key\":{\"text\":\"世界 plain\",\"list\":[\"second\"]}}\n", nil},
		{"structured", streamformat.CloudEvents, "application/cloudevents+json", `{"specversion":"1.0","id":"unique-id","source":"/source","type":"event","data":{"text":"世界"}}`, nil},
		{"batch", streamformat.CloudEvents, "application/cloudevents-batch+json", `[{"specversion":"1.0","id":"unique-id","source":"/source","type":"event","data":"世界"}]`, nil},
		{"binary-json", streamformat.CloudEvents, "application/json", `{"text":"世界"}`, http.Header{"Ce-Specversion": {"1.0"}, "Ce-Id": {"unique-id"}, "Ce-Source": {"/source"}, "Ce-Type": {"event"}}},
		{"binary-opaque", streamformat.CloudEvents, "application/octet-stream", "\x00\xff opaque", http.Header{"Ce-Specversion": {"1.0"}, "Ce-Id": {"unique-id"}, "Ce-Source": {"/source"}, "Ce-Type": {"event"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := streamformat.Decode(tc.format, tc.ct, tc.headers, []byte(tc.body), 16)
			if err != nil {
				t.Fatal(err)
			}
			var scratch bytes.Buffer
			got, err := independentDecodeInto(&scratch, tc.format, tc.ct, tc.headers, strings.NewReader(tc.body), len(tc.body), 16)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("changed decoding: %v %v", got, err)
			}
			// Explicitly overwrite/reuse the same storage, not a nondeterministic Pool.Get.
			for i := 0; i < 40; i++ {
				for j := range scratch.Bytes() {
					scratch.Bytes()[j] = 'X'
				}
				scratch.Reset()
				scratch.WriteString(strings.Repeat("y", len(tc.body)))
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("decoded records alias reused scratch")
			}
		})
	}
}

func TestIndependentReceiverScratchMatchesPrivateReader(t *testing.T) {
	for _, size := range []int{0, 1, 512, 16384, 65536, 131072} {
		body := []byte(fmt.Sprintf("{\"text\":%q}\n", strings.Repeat("a", size)))
		for _, hint := range []int64{-1, 0, 1, int64(len(body)), 65536, 65537} {
			for _, limit := range []int64{int64(len(body)), int64(len(body) - 1)} {
				t.Run(fmt.Sprintf("size%d/hint%d/limit%d", size, hint, limit), func(t *testing.T) {
					oldReader := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(&fragmentBody{data: body}), limit)
					old, oldErr := independentPrivateRead(oldReader, hint)
					var want []map[string]interface{}
					if oldErr == nil {
						want, oldErr = streamformat.Decode(streamformat.NDJSON, "application/x-ndjson", nil, old, 16)
					}
					reader := http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(&fragmentBody{data: body}), limit)
					got, err := independentDecode(streamformat.NDJSON, "application/x-ndjson", nil, reader, hint, 16)
					if (oldErr == nil) != (err == nil) || !reflect.DeepEqual(got, want) {
						t.Fatalf("changed read/decode: %v vs %v", oldErr, err)
					}
					if limit < int64(len(body)) {
						var maxErr *http.MaxBytesError
						if !errors.As(err, &maxErr) {
							t.Fatalf("size error hidden: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestIndependentReceiverScratchRejectsPrefixOnReadOrParseFailure(t *testing.T) {
	cause := errors.New("late read failure")
	valid := []byte("{\"text\":\"valid\"}\n")
	for _, hint := range []int64{-1, int64(len(valid)), 65537} {
		got, err := independentDecode(streamformat.NDJSON, "application/x-ndjson", nil, &fragmentBody{data: valid, end: cause}, hint, 8)
		if !errors.Is(err, cause) || got != nil {
			t.Fatalf("read failure published prefix: %v %v", got, err)
		}
	}
	for _, body := range []string{"{\"a\":1}\n{invalid}\n", "{\"a\":1,\"a\":2}\n", "{\"a\":\"\\ud800\"}\n", "{\"a\":1}\n{\"b\":2}\n"} {
		got, err := independentDecode(streamformat.NDJSON, "application/x-ndjson", nil, strings.NewReader(body), int64(len(body)), 1)
		if err == nil || got != nil {
			t.Fatalf("invalid batch published: %q %v", body, got)
		}
	}
}

func TestIndependentReceiverScratchConcurrentIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for worker := 0; worker < 24; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				text := fmt.Sprintf("worker-%d-%d-%s", worker, i, strings.Repeat("世界", 2048))
				payload, _ := stdjson.Marshal(map[string]string{"text": text})
				payload = append(payload, '\n')
				got, err := independentDecode(streamformat.NDJSON, "application/x-ndjson", nil, bytes.NewReader(payload), int64(len(payload)), 1)
				if err != nil || len(got) != 1 || got[0]["text"] != text {
					t.Errorf("cross-request corruption: %v", err)
					return
				}
				clear(payload)
				if got[0]["text"] != text {
					t.Error("returned value changed after caller input overwrite")
					return
				}
			}
		}(worker)
	}
	wg.Wait()
}

func FuzzIndependentReceiverScratchEquivalence(f *testing.F) {
	for _, seed := range []string{"{\"text\":\"世界\"}\n", "{\"a\":1}\n{\"a\":2}\n", "{\"a\":\"\\ud800\"}\n", "{invalid}\n", "", "{\"a\":1,\"a\":2}\n"} {
		f.Add([]byte(seed), int64(16), uint8(0))
	}
	f.Fuzz(func(t *testing.T, body []byte, hint int64, shorten uint8) {
		if len(body) > 131072 {
			return
		}
		limit := int64(len(body))
		if shorten%2 == 1 && limit > 0 {
			limit--
		}
		old, oldErr := independentPrivateRead(http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(bytes.NewReader(body)), limit), hint)
		var want []map[string]interface{}
		if oldErr == nil {
			want, oldErr = streamformat.Decode(streamformat.NDJSON, "application/x-ndjson", nil, old, 16)
		}
		got, err := independentDecode(streamformat.NDJSON, "application/x-ndjson", nil, http.MaxBytesReader(httptest.NewRecorder(), io.NopCloser(bytes.NewReader(body)), limit), hint, 16)
		if (oldErr == nil) != (err == nil) || !reflect.DeepEqual(got, want) {
			t.Fatalf("private/pooled disagreement: %v / %v", oldErr, err)
		}
	})
}

func independentDecode(format, contentType string, headers http.Header, r io.Reader, hint int64, maxRecords int) ([]map[string]interface{}, error) {
	return decodeEventInput(r, hint, format, contentType, headers, maxRecords)
}
func independentDecodeInto(b *bytes.Buffer, format, contentType string, headers http.Header, r io.Reader, hint, maxRecords int) ([]map[string]interface{}, error) {
	return decodeEventInputBuffer(b, r, int64(hint), format, contentType, headers, maxRecords)
}

// Frozen private input path from 0dc085cf; no pooled implementation calls.
func independentPrivateRead(r io.Reader, contentLength int64) ([]byte, error) {
	var b bytes.Buffer
	if contentLength > 0 && contentLength <= 64<<10 {
		b.Grow(int(contentLength) + bytes.MinRead)
	}
	if _, err := b.ReadFrom(r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
