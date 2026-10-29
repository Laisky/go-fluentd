package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"gofluentd/internal/alerts"
	"gofluentd/library"
	collectorlog "gofluentd/library/log"
)

func BenchmarkReviewDispatcher(b *testing.B) {
	_ = collectorlog.SetLevel("error")
	defer collectorlog.SetLevel("info")
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled_duplicate"
		}
		b.Run(name, func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pipe := &componentPipeline{counts: map[string]int{}}
			in := make(chan *library.FluentMsg, 1)
			var observe func(*library.FluentMsg)
			if enabled {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
				defer server.Close()
				o, err := alerts.New(ctx, alerts.Config{Token: "123:synthetic", ChatID: "synthetic", Tags: []string{"app"}}, server.URL, server.Client())
				if err != nil {
					b.Fatal(err)
				}
				defer func() { cancel(); o.Wait() }()
				observe = func(m *library.FluentMsg) { o.ObserveFluent(m.Tag, m.Message) }
			}
			d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 1, Observe: observe})
			d.Run(ctx)
			m := &library.FluentMsg{Tag: "app", Message: map[string]interface{}{"level": "error", "msg": "synthetic"}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				in <- m
				<-d.GetOutChan()
			}
			b.StopTimer()
		})
	}
}
func TestReviewDispatcherLatencyDistribution(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pipe := &componentPipeline{counts: map[string]int{}}
			in := make(chan *library.FluentMsg, 1)
			var observe func(*library.FluentMsg)
			if enabled {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) }))
				defer server.Close()
				o, err := alerts.New(ctx, alerts.Config{Token: "123:synthetic", ChatID: "synthetic", Tags: []string{"app"}}, server.URL, server.Client())
				if err != nil {
					t.Fatal(err)
				}
				defer func() { cancel(); o.Wait() }()
				observe = func(m *library.FluentMsg) { o.ObserveFluent(m.Tag, m.Message) }
			}
			d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 1, Observe: observe})
			d.Run(ctx)
			m := &library.FluentMsg{Tag: "app", Message: map[string]interface{}{"level": "error", "msg": "synthetic"}}
			samples := make([]int64, 2000)
			for i := 0; i < 100; i++ {
				in <- m
				<-d.GetOutChan()
			}
			started := time.Now()
			for i := range samples {
				ts := time.Now()
				in <- m
				<-d.GetOutChan()
				samples[i] = time.Since(ts).Nanoseconds()
			}
			elapsed := time.Since(started)
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			t.Logf("samples=%d p50_ns=%d p95_ns=%d p99_ns=%d throughput_per_sec=%.0f", len(samples), samples[1000], samples[1900], samples[1980], float64(len(samples))/elapsed.Seconds())
		})
	}
}
