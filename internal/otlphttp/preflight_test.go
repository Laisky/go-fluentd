package otlphttp_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gofluentd/internal/otlphttp"
	"gofluentd/library/otlpwire"
)

func TestOTLPHTTPPreflightRejectsWithoutAdmissionAndRecoversSlots(t *testing.T) {
	var calls atomic.Int32
	limits := otlpwire.DefaultLimits()
	limits.Items = 8
	h := receiver(t, otlphttp.ReceiverConfig{BearerToken: "test-token", MaxConcurrent: 2, Limits: limits}, func(context.Context, *otlpwire.Request) error { calls.Add(1); return nil })
	bodies := map[otlpwire.Signal][]byte{
		otlpwire.Logs:    []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[` + strings.TrimSuffix(strings.Repeat("{},", 65536), ",") + `]}]}]}`),
		otlpwire.Traces:  []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[` + strings.TrimSuffix(strings.Repeat("{},", 65536), ",") + `]}]}]}`),
		otlpwire.Metrics: []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"gauge":{"dataPoints":[` + strings.TrimSuffix(strings.Repeat("{},", 65536), ",") + `]}}]}]}]}`),
	}
	for _, s := range signals {
		for _, gz := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gzip=%v", s, gz), func(t *testing.T) {
				body := bodies[s]
				if gz {
					body = compressed(t, body)
				}
				run := func() int {
					r := request("/v1/"+string(s), otlpwire.JSON, body)
					r.ContentLength = -1
					if gz {
						r.Header.Set("Content-Encoding", "gzip")
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Body.Len() > 256 {
						t.Error("unbounded rejection body")
					}
					return w.Code
				}
				if status := run(); status != 413 {
					t.Fatalf("expected preflight rejection, got %d", status)
				}
				var wg sync.WaitGroup
				for i := 0; i < 16; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						status := run()
						if status != 413 && status != 503 {
							t.Errorf("concurrent rejection=%d", status)
						}
					}()
				}
				wg.Wait()
				if calls.Load() != 0 {
					t.Fatal("over-limit body reached durable admission")
				}
				r := request("/v1/"+string(s), otlpwire.JSON, []byte(`{}`))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != http.StatusOK || calls.Swap(0) != 1 || !bytes.Equal(w.Body.Bytes(), []byte(`{}`)) {
					t.Fatal("decode slots leaked or valid request changed")
				}
			})
		}
	}
}
