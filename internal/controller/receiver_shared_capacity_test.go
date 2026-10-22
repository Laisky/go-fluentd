//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/gin-gonic/gin"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/recvs"
	"gofluentd/library"
)

func TestOverallSharedCapRefusesActualHTTPReceiversAndRecovers(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 16 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, false)
	oc := sharedConfig(sharedPrivateDir(t), cfg.Directory)
	oc.SharedStorageBytes = cfg.MaxBytes
	p, err := OpenOTLPJournal(ctx, oc, []OTLPDestination{{ID: "synthetic", Send: barrierAccepted}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ch, _ := j.tag2JJInchanMap.Load("fixture")
	engine := gin.New()
	counter := utils.NewCounter()
	for _, format := range []string{"ndjson", "cloudevents"} {
		r, err := recvs.NewHTTPEventsRecv(recvs.HTTPEventsRecvCfg{HTTPSrv: engine, Name: format, Path: "/" + format, Tag: "fixture", Format: format, AckTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		r.SetMsgPool(j.MsgPool)
		r.SetCounter(counter)
		r.SetSyncOutChan(ch.(chan *library.FluentMsg))
		r.SetDurableAdmission(true)
	}
	otlp, err := otlphttp.NewReceiver(otlphttp.ReceiverConfig{}, p.Admit)
	if err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write([]byte(`{"resourceLogs":[],"future":"synthetic"}`))
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	types := []struct {
		name, path, ct, body string
		handler              http.Handler
		gzip                 bool
		success              int
	}{
		{"ndjson", "/ndjson", "application/x-ndjson", "{\"synthetic\":1}\n", engine, false, 204},
		{"cloudevents", "/cloudevents", "application/cloudevents+json", `{"specversion":"1.0","id":"synthetic","source":"/fixture","type":"test","data":{"n":1}}`, engine, false, 204},
		{"otlp-gzip", "/v1/logs", "application/json", compressed.String(), otlp, true, 200},
	}
	snap, err := j.diskBudget.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.diskBudget.Reserve(ctx, snap.Limit-snap.ChargedBytes-snap.ReservedBytes); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, tc := range types {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 3; n++ {
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
				r.Header.Set("Content-Type", tc.ct)
				if tc.gzip {
					r.Header.Set("Content-Encoding", "gzip")
				}
				w := httptest.NewRecorder()
				tc.handler.ServeHTTP(w, r)
				if w.Code != 503 {
					t.Errorf("%s falsely accepted full shared budget: %d", tc.name, w.Code)
				}
				if tc.gzip && (w.Header().Get("Retry-After") != "1" || strings.Contains(w.Body.String(), "partial")) {
					t.Errorf("OTLP full request not retryable: %v %s", w.Header(), w.Body.String())
				}
			}
		}()
	}
	wg.Wait()
	if id, err := overallBackend(t, j).LoadMaxId(); err != nil || id != 0 || p.frontier != 0 || len(j.outChan) != 0 {
		t.Fatalf("refused request persisted/published: id=%d frontier=%d err=%v", id, p.frontier, err)
	}
	snap, err = j.diskBudget.Snapshot(ctx)
	if err != nil || snap.ChargedBytes+snap.ReservedBytes > snap.Limit {
		t.Fatal("aggregate cap multiplied", snap, err)
	}
	if err = j.reconcileOverallBudget(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range types {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.ct)
		if tc.gzip {
			r.Header.Set("Content-Encoding", "gzip")
		}
		w := httptest.NewRecorder()
		tc.handler.ServeHTTP(w, r)
		if w.Code != tc.success {
			t.Fatalf("%s did not recover after capacity restored: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}
