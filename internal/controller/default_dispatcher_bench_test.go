package controller

import (
	"context"
	"gofluentd/library"
	collectorlog "gofluentd/library/log"
	"sort"
	"testing"
	"time"
)

func BenchmarkReviewDefaultDispatcher(b *testing.B) {
	_ = collectorlog.SetLevel("error")
	defer collectorlog.SetLevel("info")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 1)
	pipe := &componentPipeline{counts: map[string]int{}}
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 1})
	d.Run(ctx)
	m := &library.FluentMsg{Tag: "app", Message: map[string]interface{}{"level": "error", "msg": "synthetic"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in <- m
		<-d.GetOutChan()
	}
}
func TestReviewDefaultDispatcherLatency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan *library.FluentMsg, 1)
	pipe := &componentPipeline{counts: map[string]int{}}
	d := NewDispatcher(&DispatcherCfg{InChan: in, TagPipeline: pipe, NFork: 1, OutChanSize: 1})
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
}
