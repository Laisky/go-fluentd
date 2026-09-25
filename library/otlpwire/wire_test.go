package otlpwire_test

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/plog/plogotlp"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/pmetric/pmetricotlp"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"gofluentd/library/otlpwire"
)

var signals = []otlpwire.Signal{otlpwire.Logs, otlpwire.Metrics, otlpwire.Traces}
var types = []string{otlpwire.JSON, otlpwire.Protobuf}

type codec interface {
	UnmarshalJSON([]byte) error
	UnmarshalProto([]byte) error
	MarshalProto() ([]byte, error)
}

func newCodec(s otlpwire.Signal) codec {
	switch s {
	case otlpwire.Logs:
		return plogotlp.NewExportRequest()
	case otlpwire.Metrics:
		return pmetricotlp.NewExportRequest()
	default:
		return ptraceotlp.NewExportRequest()
	}
}
func count(s otlpwire.Signal) int {
	if s == otlpwire.Metrics {
		return 6
	}
	return 1
}
func fixture(t testing.TB, s otlpwire.Signal, ct string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + string(s) + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if ct == otlpwire.JSON {
		return b
	}
	r := newCodec(s)
	if err := r.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	b, err = r.MarshalProto()
	if err != nil {
		t.Fatal(err)
	}
	return append(b, 0xa0, 0x06, 0x07) // Unknown field 100, value 7: retain despite decoder ignorance.
}
func gzipBytes(t testing.TB, b []byte) []byte {
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
func TestOTLPSignalPreservation(t *testing.T) {
	for _, s := range signals {
		for _, ct := range types {
			for _, zip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/gzip=%v", s, ct, zip), func(t *testing.T) {
					raw := fixture(t, s, ct)
					b := bytes.Clone(raw)
					enc := ""
					if zip {
						b = gzipBytes(t, b)
						enc = "gzip"
					}
					r, err := otlpwire.ReadRequest(s, ct, enc, bytes.NewBuffer(b), otlpwire.DefaultLimits())
					if err != nil {
						t.Fatal(err)
					}
					if r.Signal() != s || r.ContentType() != ct || r.Items() != count(s) || !bytes.Equal(r.Payload(), raw) {
						t.Fatal("changed payload/identity/count")
					}
					copy := r.Payload()
					copy[0] ^= 0xff
					if !bytes.Equal(r.Payload(), raw) {
						t.Fatal("retained bytes alias caller")
					}
					d := newCodec(s)
					if ct == otlpwire.JSON {
						err = d.UnmarshalJSON(r.Payload())
					} else {
						err = d.UnmarshalProto(r.Payload())
					}
					if err != nil {
						t.Fatal(err)
					}
					switch s {
					case otlpwire.Logs:
						rec := d.(plogotlp.ExportRequest).Logs().ResourceLogs().At(0).ScopeLogs().At(0).LogRecords().At(0)
						if uint64(rec.Timestamp()) != math.MaxUint64 || rec.TraceID().String() != "0123456789abcdef0123456789abcdef" || !bytes.Equal(rec.Body().Bytes().AsRaw(), []byte{0, 255, 'h', 'e', 'l', 'l', 'o'}) {
							t.Fatal("log precision/bytes/identity corrupted")
						}
					case otlpwire.Traces:
						span := d.(ptraceotlp.ExportRequest).Traces().ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
						if span.EndTimestamp()-span.StartTimestamp() != 1 || span.Events().Len() != 1 || span.Links().Len() != 1 {
							t.Fatal("trace timestamps/events/links changed")
						}
					case otlpwire.Metrics:
						ms := d.(pmetricotlp.ExportRequest).Metrics().ResourceMetrics().At(0).ScopeMetrics().At(0).Metrics()
						if ms.At(0).Gauge().DataPoints().At(0).IntValue() != math.MaxInt64 || !math.IsNaN(ms.At(0).Gauge().DataPoints().At(1).DoubleValue()) || ms.At(1).Sum().DataPoints().At(0).IntValue() != math.MinInt64 {
							t.Fatal("metric value precision/type changed")
						}
						if ms.At(1).Sum().AggregationTemporality() != pmetric.AggregationTemporalityDelta || !ms.At(1).Sum().IsMonotonic() {
							t.Fatal("delta/monotonic changed")
						}
						h := ms.At(2).Histogram()
						if h.AggregationTemporality() != pmetric.AggregationTemporalityCumulative || !reflect.DeepEqual(h.DataPoints().At(0).BucketCounts().AsRaw(), []uint64{1, 2}) || h.DataPoints().At(0).Exemplars().Len() != 1 {
							t.Fatal("histogram/cumulative/exemplar changed")
						}
						e := ms.At(3).ExponentialHistogram().DataPoints().At(0)
						if e.Scale() != 2 || e.Positive().Offset() != -2 || e.ZeroThreshold() != 0.001 || e.ZeroCount() != 1 {
							t.Fatal("exponential histogram changed")
						}
						if ms.At(4).Summary().DataPoints().At(0).QuantileValues().At(0).Value() != 2 {
							t.Fatal("summary changed")
						}
					}
				})
			}
		}
	}
}
func TestOTLPInvalidInputAndLimits(t *testing.T) {
	def := otlpwire.DefaultLimits()
	valid := fixture(t, otlpwire.Logs, otlpwire.JSON)
	cases := []struct {
		name, ct, enc string
		b             []byte
		l             otlpwire.Limits
	}{
		{"json-syntax", otlpwire.JSON, "", []byte(`{"resourceLogs":`), def},
		{"trailing", otlpwire.JSON, "", []byte(`{} {}`), def},
		{"null", otlpwire.JSON, "", []byte(`null`), def},
		{"array", otlpwire.JSON, "", []byte(`[]`), def},
		{"utf8", otlpwire.JSON, "", []byte("{\"x\":\"\xff\"}"), def},
		{"truncated-proto", otlpwire.Protobuf, "", []byte{10, 127}, def},
		{"media", "text/plain", "", valid, def},
		{"charset", "application/json;charset=latin1", "", valid, def},
		{"stacked-encoding", otlpwire.JSON, "gzip,gzip", valid, def},
		{"encoding", otlpwire.JSON, "zstd", valid, def},
		{"gzip", otlpwire.JSON, "gzip", []byte("not gzip"), def},
		{"wire-limit", otlpwire.JSON, "", valid, otlpwire.Limits{WireBytes: 2, DecodedBytes: 10000, Items: 1}},
		{"gzip-limit", otlpwire.JSON, "gzip", gzipBytes(t, valid), otlpwire.Limits{WireBytes: 10000, DecodedBytes: 2, Items: 1}},
		{"decoded-limit", otlpwire.JSON, "", valid, otlpwire.Limits{WireBytes: 10000, DecodedBytes: 2, Items: 1}},
		{"zero-limits", otlpwire.JSON, "", valid, otlpwire.Limits{}},
		{"overflow-limits", otlpwire.JSON, "", valid, otlpwire.Limits{WireBytes: math.MaxInt64, DecodedBytes: 1, Items: 1}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			r, err := otlpwire.ReadRequest(otlpwire.Logs, tt.ct, tt.enc, bytes.NewReader(tt.b), tt.l)
			if err == nil || r != nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	for _, ct := range types {
		t.Run("metric-count/"+ct, func(t *testing.T) {
			lim := def
			lim.Items = 5
			_, err := otlpwire.ReadRequest(otlpwire.Metrics, ct, "", bytes.NewReader(fixture(t, otlpwire.Metrics, ct)), lim)
			if !errors.Is(err, otlpwire.ErrTooLarge) {
				t.Fatal("six points in five descriptors not limited", err)
			}
		})
	}
	for _, s := range signals {
		for _, ct := range types {
			t.Run("empty/"+string(s)+ct, func(t *testing.T) {
				b := []byte{}
				if ct == otlpwire.JSON {
					b = []byte("{}")
				}
				r, err := otlpwire.ReadRequest(s, ct, "", bytes.NewReader(b), def)
				if err != nil || r.Items() != 0 {
					t.Fatal(r, err)
				}
			})
		}
	}
	zipped := gzipBytes(t, valid)
	zipped[len(zipped)-8] ^= 1
	if _, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "gzip", bytes.NewReader(zipped), def); err == nil {
		t.Fatal("gzip checksum ignored")
	}
	lim := otlpwire.Limits{WireBytes: int64(len(valid)), DecodedBytes: int64(len(valid)), Items: 1}
	if _, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(valid), lim); err != nil {
		t.Fatal("inclusive boundary rejected", err)
	}
	if _, err := otlpwire.ReadRequest("profiles", otlpwire.JSON, "", bytes.NewReader(valid), def); err == nil {
		t.Fatal("unsupported signal")
	}
	if _, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", nil, def); err == nil {
		t.Fatal("nil reader")
	}
}
func response(s otlpwire.Signal, ct string, n int64, msg string) []byte {
	if ct == otlpwire.JSON {
		key := map[otlpwire.Signal]string{otlpwire.Logs: "rejectedLogRecords", otlpwire.Metrics: "rejectedDataPoints", otlpwire.Traces: "rejectedSpans"}[s]
		return []byte(fmt.Sprintf(`{"partialSuccess":{%q:%q,"errorMessage":%q}}`, key, fmt.Sprint(n), msg))
	}
	// Independent protobuf wire fixture: partial_success=1, rejected=1, message=2.
	if n < 0 {
		return []byte{10, 11, 8, 255, 255, 255, 255, 255, 255, 255, 255, 255, 1}
	}
	in := []byte{8, byte(n)}
	if msg != "" {
		in = append(in, 18, byte(len(msg)))
		in = append(in, []byte(msg)...)
	}
	return append([]byte{10, byte(len(in))}, in...)
}
func TestOTLPResponseSemantics(t *testing.T) {
	for _, s := range signals {
		for _, ct := range types {
			t.Run(string(s)+ct, func(t *testing.T) {
				h := http.Header{"Content-Type": []string{ct}}
				success, err := otlpwire.SuccessBody(s, ct)
				if err != nil {
					t.Fatal(err)
				}
				if ct == otlpwire.JSON && string(success) != "{}" || ct == otlpwire.Protobuf && len(success) != 0 {
					t.Fatal("full-success payload includes unexpected fields")
				}
				cases := []struct {
					name string
					b    []byte
					kind otlpwire.Disposition
					n    int64
					msg  string
					bad  bool
				}{
					{"full", success, otlpwire.Accepted, 0, "", false},
					{"partial", response(s, ct, 1, "invalid"), otlpwire.PartiallyRejected, 1, "invalid", false},
					{"all-rejected", response(s, ct, 2, "invalid"), otlpwire.PartiallyRejected, 2, "invalid", false},
					{"warning", response(s, ct, 0, "warning"), otlpwire.Accepted, 0, "warning", false},
					{"negative", response(s, ct, -1, ""), otlpwire.InvalidResponse, 0, "", true},
					{"impossible", response(s, ct, 3, ""), otlpwire.InvalidResponse, 0, "", true},
					{"garbage", []byte("bad"), otlpwire.InvalidResponse, 0, "", true},
				}
				for _, tt := range cases {
					t.Run(tt.name, func(t *testing.T) {
						o, err := otlpwire.ReadResponse(s, ct, 200, h, bytes.NewReader(tt.b), otlpwire.DefaultLimits(), 2, time.Now())
						if (err != nil) != tt.bad || o.Disposition != tt.kind || o.Rejected != tt.n || o.Diagnostic != tt.msg || o.MayRetry() {
							t.Fatal(o, err)
						}
						if o.FullyAccepted() != (tt.kind == otlpwire.Accepted) {
							t.Fatal("false acceptance")
						}
					})
				}
				h.Set("Content-Encoding", "gzip")
				o, err := otlpwire.ReadResponse(s, ct, 200, h, bytes.NewReader(gzipBytes(t, success)), otlpwire.DefaultLimits(), 2, time.Now())
				if err != nil || !o.FullyAccepted() {
					t.Fatal(o, err)
				}
			})
		}
	}
}
func TestOTLPHTTPStatusAndResponseBounds(t *testing.T) {
	h := http.Header{"Content-Type": []string{otlpwire.JSON}, "Retry-After": []string{"7"}}
	for _, status := range []int{200, 201, 202, 204, 301, 307, 308, 400, 401, 403, 408, 413, 429, 500, 501, 502, 503, 504, 505} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			o, err := otlpwire.ReadResponse(otlpwire.Logs, otlpwire.JSON, status, h, strings.NewReader("{}"), otlpwire.DefaultLimits(), 1, time.Now())
			retry := status == 429 || status == 502 || status == 503 || status == 504
			if err != nil || o.MayRetry() != retry || o.FullyAccepted() != (status == 200) {
				t.Fatal(o, err)
			}
			if retry && o.RetryAfter != 7*time.Second {
				t.Fatal("missing retry-after")
			}
		})
	}
	for _, status := range []int{200, 503} {
		for _, gz := range []bool{false, true} {
			t.Run(fmt.Sprintf("bound/%d/%v", status, gz), func(t *testing.T) {
				hh := h.Clone()
				b := []byte(strings.Repeat(" ", 100))
				if gz {
					b = gzipBytes(t, b)
					hh.Set("Content-Encoding", "gzip")
				}
				o, err := otlpwire.ReadResponse(otlpwire.Logs, otlpwire.JSON, status, hh, bytes.NewReader(b), otlpwire.Limits{WireBytes: 1000, DecodedBytes: 10, Items: 1}, 1, time.Now())
				if !errors.Is(err, otlpwire.ErrTooLarge) || o.MayRetry() || o.FullyAccepted() {
					t.Fatal("oversized response accepted/retried", o, err)
				}
			})
		}
	}
	for _, ct := range []string{"", otlpwire.Protobuf, "text/plain"} {
		t.Run("media/"+ct, func(t *testing.T) {
			hh := h.Clone()
			hh.Set("Content-Type", ct)
			o, err := otlpwire.ReadResponse(otlpwire.Logs, otlpwire.JSON, 200, hh, strings.NewReader("{}"), otlpwire.DefaultLimits(), 1, time.Now())
			if err == nil || o.FullyAccepted() {
				t.Fatal("wrong response content type")
			}
		})
	}
	for _, b := range []string{`{} {}`, `null`, `[]`} {
		o, err := otlpwire.ReadResponse(otlpwire.Logs, otlpwire.JSON, 200, h, strings.NewReader(b), otlpwire.DefaultLimits(), 1, time.Now())
		if err == nil || o.FullyAccepted() {
			t.Fatal("malformed response accepted")
		}
	}
}
func TestOTLPRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		s string
		d time.Duration
	}{{"7", 7 * time.Second}, {"0", 0}, {" 12 ", 12 * time.Second}, {"-1", 0}, {"+2", 0}, {"0.1", 0}, {"", 0}, {"invalid", 0}, {"99999999999999999999", 0}, {"9223372036854775807", 0}, {now.Add(9 * time.Second).Format(http.TimeFormat), 9 * time.Second}, {now.Add(-time.Second).Format(http.TimeFormat), 0}} {
		t.Run(tt.s, func(t *testing.T) {
			if d := otlpwire.RetryDelay(tt.s, now); d != tt.d {
				t.Fatalf("%v != %v", d, tt.d)
			}
		})
	}
}

// These are real HTTP wire exchanges, NOT whole go-fluentd/journal E2E tests.
func TestOTLPHTTPWireExchange(t *testing.T) {
	for _, s := range signals {
		t.Run(string(s), func(t *testing.T) {
			path, _ := s.Path()
			raw := fixture(t, s, otlpwire.JSON)
			got := make(chan []byte, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != path || r.Method != "POST" {
					http.Error(w, "wrong endpoint", 400)
					return
				}
				req, err := otlpwire.ReadRequest(s, r.Header.Get("Content-Type"), r.Header.Get("Content-Encoding"), r.Body, otlpwire.DefaultLimits())
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				got <- req.Payload()
				w.Header().Set("Content-Type", otlpwire.JSON)
				w.WriteHeader(200)
				_, _ = w.Write(response(s, otlpwire.JSON, 1, "test rejection"))
			}))
			defer srv.Close()
			req, err := http.NewRequest("POST", srv.URL+path, io.NopCloser(bytes.NewReader(gzipBytes(t, raw))))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", otlpwire.JSON)
			req.Header.Set("Content-Encoding", "gzip")
			client := srv.Client()
			client.Timeout = 3 * time.Second
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			o, err := otlpwire.ReadResponse(s, otlpwire.JSON, resp.StatusCode, resp.Header, resp.Body, otlpwire.DefaultLimits(), int64(count(s)), time.Now())
			if err != nil || o.FullyAccepted() || o.MayRetry() || o.Rejected != 1 {
				t.Fatal("partial response accepted/retried", o, err)
			}
			select {
			case b := <-got:
				if !bytes.Equal(b, raw) {
					t.Fatal("payload changed")
				}
			case <-time.After(time.Second):
				t.Fatal("request missing")
			}
		})
	}
}

type badReader struct{}

func (badReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestOTLPReadFailures(t *testing.T) {
	if _, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", badReader{}, otlpwire.DefaultLimits()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	o, err := otlpwire.ReadResponse(otlpwire.Logs, otlpwire.JSON, 200, nil, badReader{}, otlpwire.DefaultLimits(), 1, time.Now())
	if err == nil || o.FullyAccepted() {
		t.Fatal(o, err)
	}
}
func BenchmarkOTLPRequest(b *testing.B) {
	for _, s := range signals {
		for _, ct := range types {
			b.Run(string(s)+ct, func(b *testing.B) {
				raw := fixture(b, s, ct)
				b.SetBytes(int64(len(raw)))
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r, err := otlpwire.ReadRequest(s, ct, "", bytes.NewReader(raw), otlpwire.DefaultLimits())
					if err != nil || r.Items() != count(s) {
						b.Fatal(r, err)
					}
				}
			})
		}
	}
}

func FuzzOTLPWire(f *testing.F) {
	f.Add([]byte("{}"), false)
	f.Add([]byte{10, 0}, true)
	f.Add([]byte(`{"partialSuccess":{"rejectedLogRecords":"1"}}`), false)
	f.Fuzz(func(t *testing.T, b []byte, binary bool) {
		if len(b) > 4096 {
			return
		}
		ct := otlpwire.JSON
		if binary {
			ct = otlpwire.Protobuf
		}
		lim := otlpwire.Limits{WireBytes: 4096, DecodedBytes: 4096, Items: 100}
		for _, s := range signals {
			r, err := otlpwire.ReadRequest(s, ct, "", bytes.NewReader(b), lim)
			if err == nil && !bytes.Equal(r.Payload(), b) {
				t.Fatal("accepted payload altered")
			}
			o, err := otlpwire.ReadResponse(s, ct, 200, http.Header{"Content-Type": []string{ct}}, bytes.NewReader(b), lim, 100, time.Now())
			if err != nil && o.FullyAccepted() {
				t.Fatal("decoding error acknowledged")
			}
			if o.Rejected > 0 && (o.FullyAccepted() || o.MayRetry()) {
				t.Fatal("rejection acknowledged/retried")
			}
		}
	})
}
