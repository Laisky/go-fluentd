package alerts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gofluentd/library/otlpwire"
)

func BenchmarkReviewObserver(b *testing.B) {
	for _, mode := range []string{"disabled", "normal_duplicate", "burst_saturated", "stalled_transport"} {
		b.Run(mode, func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case called <- struct{}{}:
				default:
				}
				w.WriteHeader(429)
				io.WriteString(w, `{"ok":false,"parameters":{"retry_after":3600}}`)
			}))
			defer server.Close()
			var o *Observer
			if mode != "disabled" {
				var err error
				o, err = New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", Tags: []string{"app"}, IncludeMessage: mode == "burst_saturated" || mode == "stalled_transport"}, server.URL, server.Client())
				if err != nil {
					b.Fatal(err)
				}
				defer func() { cancel(); o.Wait() }()
			}
			if mode == "stalled_transport" {
				o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": "start"})
				select {
				case <-called:
				case <-time.After(3 * time.Second):
					b.Fatal("transport did not enter backoff")
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				m := "repeat"
				if mode == "burst_saturated" || mode == "stalled_transport" {
					m = wordyFailure(i)
				}
				o.ObserveFluent("app", map[string]interface{}{"level": "error", "msg": m})
			}
			b.StopTimer()
			cancel()
		})
	}
}
func BenchmarkReviewAdversarialMetadata(b *testing.B) {
	o := inert()
	o.cfg.Services = []string{"svc"}
	scope := strings.Repeat("x", 8192)
	records := strings.Repeat(`{"severityNumber":17,"body":{"stringValue":"synthetic"}},`, 63) + `{"severityNumber":17}`
	raw := fmt.Sprintf(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeLogs":[{"scope":{"name":%q},"logRecords":[%s]}]}]}`, scope, records)
	p := logsRequest(b, raw)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.parseOTLP(p)
	}
}
func BenchmarkReviewOTLPBackoff(b *testing.B) {
	called := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case called <- struct{}{}:
		default:
		}
		w.WriteHeader(429)
		io.WriteString(w, `{"ok":false,"parameters":{"retry_after":3600}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	o, err := New(ctx, Config{Token: "123:synthetic", ChatID: "synthetic", Services: []string{"svc"}}, server.URL, server.Client())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { cancel(); o.Wait() }()
	raw := `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"svc"}}]},"scopeLogs":[{"logRecords":[{"severityNumber":17}]}]}]}`
	req, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(raw), otlpwire.DefaultLimits())
	if err != nil {
		b.Fatal(err)
	}
	o.Observe(Event{Source: "kick", Severity: "error"})
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		b.Fatal("transport did not enter backoff")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o.ObserveOTLP(req)
	}
}

// BenchmarkFluentInfoRecord measures the common path of a selected tag whose
// records do not qualify for notification.
func BenchmarkFluentInfoRecord(b *testing.B) {
	body := []byte(strings.Repeat("x", 1024))
	for name, record := range map[string]map[string]interface{}{
		"string_level": {"level": "info", "msg": body},
		"bytes_level":  {"level": []byte("info"), "msg": body},
		"embedded":     {"log": []byte(`{"level":"info","msg":"synthetic"}`)},
	} {
		b.Run(name, func(b *testing.B) {
			o := inert()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.ObserveFluent("app", record)
			}
		})
	}
}

// BenchmarkFluentDuplicateError measures the synchronous dispatcher cost of a
// repeated error, including grouping of the hidden message.
func BenchmarkFluentDuplicateError(b *testing.B) {
	for _, size := range []int{64, 1024, 8192} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			o := inert()
			record := map[string]interface{}{"level": "error", "msg": strings.Repeat("connection refused ", size/19+1)[:size]}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.ObserveFluent("app", record)
			}
		})
	}
}

// BenchmarkTagOnlyOTLPCallback measures what admission pays when no OTLP service
// is selected.
func BenchmarkTagOnlyOTLPCallback(b *testing.B) {
	for _, size := range []int{1024, 1<<20 - 128} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			r := logsRequest(b, `{"resourceLogs":[],"padding":"`+strings.Repeat("x", size)+`"}`)
			o := inert()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.ObserveOTLP(r)
				select {
				case <-o.otlp:
					<-o.otlpSlots
				default:
				}
			}
		})
	}
}

// BenchmarkReviewOTLPCopy measures only the synchronous observer callback,
// not journal admission or downstream storage latency.
func BenchmarkReviewOTLPCopy(b *testing.B) {
	for _, size := range []int{1024, 1<<20 - 128} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			raw := `{"resourceLogs":[],"padding":"` + strings.Repeat("x", size) + `"}`
			r, err := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", strings.NewReader(raw), otlpwire.Limits{WireBytes: 2 << 20, DecodedBytes: 2 << 20, Items: 100})
			if err != nil {
				b.Fatal(err)
			}
			o := inert()
			o.cfg.Services = []string{"svc"}
			b.ReportAllocs()
			b.SetBytes(int64(len(raw)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.ObserveOTLP(r)
				<-o.otlp
				<-o.otlpSlots
			}
		})
	}
}
