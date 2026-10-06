package streamformat

import (
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Measure bytes, not allocation count: the vulnerable split table is one very
// large allocation. The body is prepared outside the measured decode interval.
func TestRegressionNDJSONBlankLineAllocationBound(t *testing.T) {
	body := bytes.Repeat([]byte{'\n'}, 4<<20)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	records, err := Decode(NDJSON, "application/x-ndjson", nil, body, 1024)
	runtime.ReadMemStats(&after)
	t.Logf("decode allocated %d bytes", after.TotalAlloc-before.TotalAlloc)
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("blank-line rejection allocated %d bytes; budget is 1 MiB", n)
	}
	if err == nil || len(records) != 0 {
		t.Fatalf("excessive empty lines must fail atomically: records=%d err=%v", len(records), err)
	}
}

func TestRegressionNDJSONBlankLineWorkBound(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		body := []byte(strings.Repeat(newline, 65537))
		records, err := Decode(NDJSON, "application/x-ndjson", nil, body, 1024)
		if err == nil || len(records) != 0 {
			t.Errorf("newline %q bypassed parsing-work limit: records=%d err=%v", newline, len(records), err)
		}
	}
}

func BenchmarkRegressionNDJSONBlankLines(b *testing.B) {
	body := bytes.Repeat([]byte{'\n'}, 4<<20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Decode(NDJSON, "application/x-ndjson", nil, body, 1024)
	}
}

func TestRegressionNDJSONLineAndRecordBoundaries(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		for _, tc := range []struct {
			name, body       string
			maxRecords, want int
			bad              bool
		}{
			{"empty", "", 1, 0, false},
			{"lines_at_limit", strings.Repeat(newline, MaxNDJSONLines), 1, 0, false},
			{"lines_over_limit", strings.Repeat(newline, MaxNDJSONLines+1), 1, 0, true},
			{"record_after_blanks", strings.Repeat(newline, MaxNDJSONLines-1) + `{"id":"owned"}` + newline, 1, 1, false},
			{"record_beyond_line_limit", strings.Repeat(newline, MaxNDJSONLines) + `{}` + newline, 1, 0, true},
			{"no_partial_prefix", `{}` + newline + strings.Repeat(newline, MaxNDJSONLines), 1, 0, true},
			{"records_at_limit", `{}` + newline + newline + `{}` + newline, 2, 2, false},
			{"records_over_limit", `{}` + newline + newline + `{}` + newline, 1, 0, true},
		} {
			t.Run(tc.name+fmt.Sprint(len(newline)), func(t *testing.T) {
				body := []byte(tc.body)
				records, err := Decode(NDJSON, "application/x-ndjson", nil, body, tc.maxRecords)
				if (err != nil) != tc.bad || len(records) != tc.want || tc.bad && records != nil {
					t.Fatalf("records=%d err=%v", len(records), err)
				}
				for i := range body {
					body[i] = 'x'
				}
				if tc.name == "record_after_blanks" && records[0]["id"] != "owned" {
					t.Fatal("record aliases scratch")
				}
			})
		}
	}
}

func TestRegressionNDJSONConcurrentBlankLineRequests(t *testing.T) {
	body := bytes.Repeat([]byte{'\n'}, 4<<20)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if records, err := Decode(NDJSON, "application/x-ndjson", nil, body, 1); err == nil || records != nil {
				t.Error("accepted excess lines")
			}
		}()
	}
	wg.Wait()
}
