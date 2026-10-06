package otlpwire

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// These fixtures are valid, small, and contain only synthetic empty telemetry.
// They exercise ReadRequest, not a replacement decoder or an OOM experiment.
func preflightRepeatedFixture(s Signal, ct string, count int) []byte {
	if ct == JSON {
		items := strings.TrimSuffix(strings.Repeat("{},", count), ",")
		switch s {
		case Logs:
			return []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[` + items + `]}]}]}`)
		case Traces:
			return []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[` + items + `]}]}]}`)
		default:
			return []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"gauge":{"dataPoints":[` + items + `]}}]}]}]}`)
		}
	}
	wrap := func(num protowire.Number, body []byte) []byte {
		return protowire.AppendBytes(protowire.AppendTag(nil, num, protowire.BytesType), body)
	}
	if s == Metrics {
		return wrap(1, wrap(2, wrap(2, wrap(5, bytes.Repeat([]byte{0x0a, 0}, count)))))
	}
	return wrap(1, wrap(2, bytes.Repeat([]byte{0x12, 0}, count)))
}

func TestRegressionOTLPRejectsBeforeMaterialization(t *testing.T) {
	for _, s := range []Signal{Logs, Traces, Metrics} {
		for _, ct := range []string{JSON, Protobuf} {
			t.Run(fmt.Sprint(s, "/", ct), func(t *testing.T) {
				payload := preflightRepeatedFixture(s, ct, 65536)
				l := DefaultLimits()
				l.Items = 8
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				r, err := ReadRequest(s, ct, "", bytes.NewReader(payload), l)
				runtime.ReadMemStats(&after)
				if r != nil || !errors.Is(err, ErrTooLarge) {
					t.Fatalf("expected atomic limit rejection: %v %v", r, err)
				}
				allocated := after.TotalAlloc - before.TotalAlloc
				t.Logf("body_bytes=%d allocated_bytes=%d", len(payload), allocated)
				// Includes bounded body buffering. It deliberately does not assert exact
				// allocator-dependent sizes, timings, peak RSS, or a process heap quota.
				if allocated > 3<<20 {
					t.Fatalf("OTLP_PREDECODE_REGRESSION: allocated %d bytes before rejecting eight-item budget", allocated)
				}
			})
		}
	}
}

func TestRegressionOTLPItemBoundaryPayloadOwnership(t *testing.T) {
	for _, s := range []Signal{Logs, Traces, Metrics} {
		for _, ct := range []string{JSON, Protobuf} {
			for _, n := range []int{0, 8, 9} {
				t.Run(fmt.Sprint(s, "/", ct, "/", n), func(t *testing.T) {
					b := preflightRepeatedFixture(s, ct, n)
					original := bytes.Clone(b)
					l := DefaultLimits()
					l.Items = 8
					r, err := ReadRequest(s, ct, "", bytes.NewReader(b), l)
					if n > 8 {
						if r != nil || !errors.Is(err, ErrTooLarge) {
							t.Fatalf("over-limit: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if r.Items() != n || !bytes.Equal(original, r.Payload()) {
						t.Fatal("count or payload changed")
					}
					clear(b)
					exposed := r.Payload()
					clear(exposed)
					if !bytes.Equal(original, r.Payload()) {
						t.Fatal("payload aliases mutable input/output")
					}
				})
			}
		}
	}
}
