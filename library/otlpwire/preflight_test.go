package otlpwire

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/protobuf/encoding/protowire"
)

func pfWrap(n protowire.Number, b []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(nil, n, protowire.BytesType), b)
}
func pfGzip(t testing.TB, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	if _, err := z.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func pfReferenceCount(s Signal, ct string, b []byte) (int, error) {
	switch s {
	case Logs:
		r := plogotlp.NewExportRequest()
		err := decode(r, ct, b)
		return r.Logs().LogRecordCount(), err
	case Traces:
		r := ptraceotlp.NewExportRequest()
		err := decode(r, ct, b)
		return r.Traces().SpanCount(), err
	default:
		r := pmetricotlp.NewExportRequest()
		err := decode(r, ct, b)
		return r.Metrics().DataPointCount(), err
	}
}
func TestOTLPPreflightAllMetricKindsAndAliases(t *testing.T) {
	kinds := []struct {
		name  string
		field protowire.Number
	}{{"gauge", 5}, {"sum", 7}, {"histogram", 9}, {"exponentialHistogram", 10}, {"summary", 11}}
	for _, k := range kinds {
		for _, ct := range []string{JSON, Protobuf} {
			for _, n := range []int{2, 3, 32} {
				var body []byte
				if ct == JSON {
					body = []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"` + k.name + `":{"dataPoints":[` + strings.TrimSuffix(strings.Repeat("{},", n), ",") + `]}}]}]}]}`)
				} else {
					body = pfWrap(1, pfWrap(2, pfWrap(2, pfWrap(k.field, bytes.Repeat([]byte{0x0a, 0}, n)))))
				}
				t.Run(fmt.Sprintf("%s/%s/%d", k.name, ct, n), func(t *testing.T) {
					got, err := pfReferenceCount(Metrics, ct, body)
					if err != nil || got != n {
						t.Fatalf("fixture not valid: %d %v", got, err)
					}
					for _, gz := range []bool{false, true} {
						wire, encoding := body, ""
						if gz {
							wire, encoding = pfGzip(t, body), "gzip"
						}
						l := DefaultLimits()
						l.Items = 2
						r, err := ReadRequest(Metrics, ct, encoding, bytes.NewReader(wire), l)
						if n > 2 {
							if r != nil || !errors.Is(err, ErrTooLarge) {
								t.Fatalf("bypass: %v", err)
							}
						} else if err != nil || r.Items() != n {
							t.Fatalf("boundary: %v", err)
						}
					}
				})
			}
		}
	}
	for _, b := range []string{
		`{"resource_logs":[{"scope_logs":[{"log_records":[{},{}]}]}]}`,
		`{"resource\u004cogs":[{"scopeLogs":[{"log\u0052ecords":[{},{}]}]}]}`,
		`{"resourceLogs":[{"scopeLogs":[{"logRecords":[null,null]}]}]}`,
		`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{}],"logRecords":[{}]}]}]}`,
	} {
		count, err := pfReferenceCount(Logs, JSON, []byte(b))
		if err != nil || count != 2 {
			t.Fatalf("alias/null/duplicate fixture %s: %d %v", b, count, err)
		}
		if err := preflight(Logs, JSON, []byte(b), 1); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("JSON spelling/duplicate bypass: %s: %v", b, err)
		}
	}
	// Deprecated records are materialized, although the current final count
	// drops this deprecated-only container. Count that work conservatively.
	deprecated := []byte(`{"resourceLogs":[{"deprecatedScopeLogs":[{"logRecords":[{},{}]}]}]}`)
	if n, err := pfReferenceCount(Logs, JSON, deprecated); err != nil || n != 0 {
		t.Fatalf("deprecated fixture: %d %v", n, err)
	}
	if err := preflight(Logs, JSON, deprecated, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatal("deprecated allocation bypass", err)
	}
	if err := preflight(Logs, JSON, deprecated, 2); err != nil {
		t.Fatal("deprecated boundary", err)
	}
	// Both old field 1000 and normal scope field 2 must be counted before migration.
	b := pfWrap(1, append(pfWrap(1000, pfWrap(2, nil)), pfWrap(2, pfWrap(2, nil))...))
	if err := preflight(Logs, Protobuf, b, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatal("deprecated protobuf count", err)
	}
}
func TestOTLPPreflightKnownSchemaFixtures(t *testing.T) {
	for _, s := range []Signal{Logs, Traces, Metrics} {
		b, err := os.ReadFile("testdata/" + string(s) + ".json")
		if err != nil {
			t.Fatal(err)
		}
		n, err := pfReferenceCount(s, JSON, b)
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{n, n - 1} {
			err := preflight(s, JSON, b, limit)
			if limit == n && err != nil {
				t.Fatalf("full fixture rejected: %s %v", s, err)
			}
			if limit < n && !errors.Is(err, ErrTooLarge) {
				t.Fatalf("full fixture bypass: %s %v", s, err)
			}
		}
	}
}
func TestOTLPPreflightIndependentStructureAndDepth(t *testing.T) {
	for _, ct := range []string{JSON, Protobuf} {
		for _, excess := range []bool{false, true} {
			n := maxPreflightValues - 1
			if ct == JSON {
				n = maxPreflightValues - 3
			}
			if excess {
				n++
			}
			var b []byte
			if ct == JSON {
				b = []byte(`{"future":[` + strings.TrimSuffix(strings.Repeat("0,", n), ",") + `]}`)
			} else {
				b = bytes.Repeat([]byte{0x78, 0}, n)
			}
			err := preflight(Logs, ct, b, 1)
			if excess {
				if !errors.Is(err, ErrTooLarge) {
					t.Fatal("structure bypass", ct, err)
				}
			} else if err != nil {
				t.Fatal("structure boundary", ct, err)
			}
		}
	}
	for _, depth := range []int{8, 64, 65} {
		// root=0; depth-1 arrays plus one scalar make this exact JSON depth.
		b := []byte(`{"future":` + strings.Repeat("[", depth-1) + `null` + strings.Repeat("]", depth-1) + `}`)
		err := preflight(Logs, JSON, b, 1)
		if depth > maxPreflightDepth {
			if !errors.Is(err, ErrTooLarge) {
				t.Fatal("JSON depth bypass", err)
			}
		} else if err != nil {
			t.Fatal("JSON depth boundary", err)
		}
		// Unknown groups have a real nesting budget and matching end-group checks.
		b = append(bytes.Repeat([]byte{0x7b}, depth), bytes.Repeat([]byte{0x7c}, depth)...)
		err = preflight(Logs, Protobuf, b, 1)
		if depth > maxPreflightDepth {
			if !errors.Is(err, ErrTooLarge) {
				t.Fatal("proto group depth bypass", err)
			}
		} else if err != nil {
			t.Fatal("proto depth boundary", err)
		}
	}
	// Empty known resource containers consume budget even without telemetry.
	b := bytes.Repeat([]byte{0x0a, 0}, maxPreflightValues)
	if err := preflight(Logs, Protobuf, b, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatal("empty resource bypass", err)
	}
	// Deep known AnyValue messages must not be treated as opaque length fields.
	b = pfWrap(1, []byte("synthetic"))
	for i := 0; i < maxPreflightDepth; i++ {
		b = pfWrap(5, pfWrap(1, b))
	}
	b = pfWrap(1, pfWrap(2, pfWrap(2, pfWrap(5, b))))
	if err := preflight(Logs, Protobuf, b, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatal("known depth bypass", err)
	}
}
func TestOTLPPreflightPackedNumbers(t *testing.T) {
	for _, size := range []int{1, 8} {
		// Current metric packed arrays use varints or fixed64.
		p := preflightBudget{maxItems: 1}
		k := pfExponentialHistogramDataPointBuckets
		field := protowire.Number(2)
		if size == 8 {
			k = pfHistogramDataPoint
			field = 6
		}

		b := pfWrap(field, make([]byte, size*maxPreflightValues))
		if _, err := p.proto(b, k, 0, 0); !errors.Is(err, ErrTooLarge) {
			t.Fatal("packed structure bypass", size, err)
		}
		b = pfWrap(field, make([]byte, size*8))
		p = preflightBudget{maxItems: 1}
		if _, err := p.proto(b, k, 0, 0); err != nil {
			t.Fatal("small packed array", err)
		}
	}
}
func TestOTLPPreflightUnknownFieldsAndMalformedInput(t *testing.T) {
	for _, b := range [][]byte{
		[]byte(`{"future":{"logRecords":[{},{}],"nested":[true,false,null,1.2,-3,"a\\\"b"]}}`),
		[]byte(`{"\\unknown":"valid","future\u0041":{"escaped":"\\\\","unicode":"日志"}}`),
	} {
		r, err := ReadRequest(Logs, JSON, "", bytes.NewReader(b), Limits{4 << 20, 4 << 20, 1})
		if err != nil || r.Items() != 0 || !bytes.Equal(r.Payload(), b) {
			t.Fatal("unknown JSON changed", err)
		}
	}
	// Length-delimited unknown data stays opaque even when it is not protobuf.
	b := pfWrap(100, []byte{0xff, 0xff, 0xff})
	b = append(b, 0xa0, 6, 7)
	r, err := ReadRequest(Logs, Protobuf, "", bytes.NewReader(b), DefaultLimits())
	if err != nil || r.Items() != 0 || !bytes.Equal(r.Payload(), b) {
		t.Fatal("unknown protobuf changed", err)
	}
	for _, b := range [][]byte{{0}, {0x0a, 0xff}, {0x09}, {0x0d, 0}, {0x7c}, {0x7b}, {0x7b, 0x74}, {0x7f}, bytes.Repeat([]byte{0x80}, 12), pfWrap(1, []byte{0x12, 0xff})} {
		if err := preflight(Logs, Protobuf, b, 1); err == nil {
			t.Fatalf("malformed protobuf admitted by preflight: %x", b)
		}
	}
	for _, b := range []string{"", "[]", "{} {}", "{", `{"x":"bad\q"}`, "{\"x\":\"\xff\"}"} {
		if err := preflight(Logs, JSON, []byte(b), 1); err == nil {
			t.Fatal("malformed JSON", b)
		}
	}
	// A field using a wrong primitive wire type may pass the structural scanner,
	// but cannot evade the existing schema decoder or reach admission.
	if r, err := ReadRequest(Logs, Protobuf, "", bytes.NewReader([]byte{8, 1}), DefaultLimits()); r != nil || err == nil {
		t.Fatal("schema check bypassed")
	}
}
func TestOTLPPreflightSaturationIsPerRequest(t *testing.T) {
	for i := 0; i < 4; i++ {
		b := preflightRepeatedFixture(Logs, JSON, 9)
		if err := preflight(Logs, JSON, b, 8); !errors.Is(err, ErrTooLarge) {
			t.Fatal(err)
		}
		if err := preflight(Logs, JSON, preflightRepeatedFixture(Logs, JSON, 8), 8); err != nil {
			t.Fatal("budget leaked between requests", err)
		}
	}
}
func FuzzOTLPPreflight(f *testing.F) {
	for _, s := range []Signal{Logs, Traces, Metrics} {
		for _, ct := range []string{JSON, Protobuf} {
			f.Add(string(s), ct, preflightRepeatedFixture(s, ct, 2))
		}
	}
	f.Add("logs", JSON, []byte(`{"future":[[[null]],"\\\"",{"logRecords":[]}]}`))
	f.Fuzz(func(t *testing.T, s, ct string, b []byte) {
		signal := Signal(s)
		if _, err := signal.Path(); err != nil {
			return
		}
		if ct != JSON && ct != Protobuf {
			return
		}
		if len(b) > 65536 {
			return
		}
		if err := preflight(signal, ct, b, 16); err != nil {
			return
		}
		n, err := pfReferenceCount(signal, ct, b)
		if err == nil && n > 16 {
			t.Fatalf("preflight allowed %d items", n)
		}
	})
}
func BenchmarkOTLPPreflightReject(b *testing.B) {
	for _, ct := range []string{JSON, Protobuf} {
		payload := preflightRepeatedFixture(Traces, ct, 65536)
		b.Run(ct, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := preflight(Traces, ct, payload, 8); !errors.Is(err, ErrTooLarge) {
					b.Fatal(err)
				}
			}
		})
	}
}
