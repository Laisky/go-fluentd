package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFixtureIdentityAndIntegerPrecision(t *testing.T) {
	for _, p := range []string{"ndjson", "cloudevents", "logs", "metrics", "traces"} {
		a, b := makeFixture(p, 1, 512), makeFixture(p, 2, 512)
		if hash(a.Body) == hash(b.Body) || !json.Valid(a.Body) || a.Path == "" {
			t.Fatalf("invalid %s fixture", p)
		}
		if a.Canonical != "" {
			s, e := canonical(a.Body)
			if e != nil || s != a.Canonical || !strings.Contains(s, "9223372036854775807") {
				t.Fatalf("integer changed: %s %v", s, e)
			}
		}
	}
	if _, e := canonical([]byte("{}{}")); e == nil {
		t.Fatal("trailing JSON accepted")
	}
}
func goodRecord() record {
	return record{Protocol: "logs", Start: 1000000, Scheduled: 500000, Ack: 2000000, Status: 200, Sink: []int64{3000000, 4000000}}
}
func TestSummaryUsesSlowestDestinationAndScheduledArrival(t *testing.T) {
	r, e := summarize([]record{goodRecord()}, 0, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	if r["e2e_ms"].(map[string]float64)["p99"] != 3 || r["scheduled_e2e_ms"].(map[string]float64)["p99"] != 3.5 {
		t.Fatal(r)
	}
}
func TestSummaryRejectsMaskedCapacityFailures(t *testing.T) {
	for _, name := range []string{"lost", "failed", "dropped", "wrong-ack"} {
		t.Run(name, func(t *testing.T) {
			r := goodRecord()
			switch name {
			case "lost":
				r.Sink[1] = 0
			case "failed":
				r.Error = "timeout"
			case "dropped":
				r.Dropped = true
			case "wrong-ack":
				r.Status = 204
			}
			if _, e := summarize([]record{r}, 0, 1, 2); e == nil {
				t.Fatalf("accepted %s", name)
			}
		})
	}
}
func TestSinkRejectsCorruptAndWrongRoute(t *testing.T) {
	for _, bad := range []string{"payload", "route", "auth"} {
		t.Run(bad, func(t *testing.T) {
			f := makeFixture("logs", 0, 16)
			tr := &trial{opts: options{Destinations: 1}, epoch: time.Now(), lookup: map[string]int{"logs:" + hash(f.Body): 0}, records: []record{{Sink: make([]int64, 1)}}}
			body := string(f.Body)
			path := "/0/v1/logs"
			if bad == "payload" {
				body = "{}"
			}
			if bad == "route" {
				path = "/0/v1/traces"
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			if bad == "auth" {
				r.Header.Del("Authorization")
			}
			tr.sink(httptest.NewRecorder(), r)
			if len(tr.failures) == 0 || tr.completed.Load() != 0 {
				t.Fatal("corrupt sink accepted")
			}
		})
	}
}
func TestOpenLoopDoesNotRebaseOrHideDroppedArrivals(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(50 * time.Millisecond); w.Write([]byte("{}")) }))
	defer s.Close()
	tr := &trial{opts: options{Concurrency: 1}, epoch: time.Now(), ingest: s.URL}
	for i := 0; i < 20; i++ {
		tr.fixtures = append(tr.fixtures, makeFixture("logs", i, 1))
		tr.records = append(tr.records, record{Protocol: "logs"})
	}
	tr.load(s.Client(), 0, 20, 10000)
	dropped := 0
	for i, r := range tr.records {
		if r.Dropped {
			dropped++
		}
		if i > 0 && r.Scheduled-tr.records[0].Scheduled != int64(i)*100000 {
			t.Fatal("arrival schedule moved")
		}
	}
	if dropped == 0 {
		t.Fatal("overload hidden")
	}
}

func TestSavedAuditRejectsForgedPerformance(t *testing.T) {
	root := t.TempDir()
	o := options{Protocol: "logs", Requests: 1, Warmup: 1, Destinations: 1, Payload: 1}
	rs := []record{}
	for id := 0; id < 2; id++ {
		rs = append(rs, record{ID: id, Protocol: "logs", SHA: hash(makeFixture("logs", id, 1).Body),
			Scheduled: 1, Start: 2, Ack: 3, Status: 200, Sink: []int64{4}})
	}
	summary, err := summarize(rs[1:], 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"app_cpu_seconds", "app_cpu_us_per_delivered", "app_peak_rss_bytes", "app_rss_before_bytes", "app_rss_after_bytes", "app_write_bytes", "app_cpu_cores"} {
		summary[key] = float64(0)
	}
	summary["passed"] = true
	write := func() {
		for name, value := range map[string]any{
			"options.json": o, "requests.json": rs, "summary.json": summary,
			"errors.json": []string{}, "process.json": map[string]any{"graceful": true, "exit_code": 0},
			"measurement.json": map[string]any{"begin_ns": 1, "elapsed_seconds": 1, "before": resources{}, "after": resources{}},
		} {
			if e := save(filepath.Join(root, name), value); e != nil {
				t.Fatal(e)
			}
		}
	}
	write()
	if err := audit(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"latency", "cpu", "source", "missing", "exit"} {
		t.Run(name, func(t *testing.T) {
			write()
			switch name {
			case "latency":
				var bad map[string]any
				readJSON(filepath.Join(root, "summary.json"), &bad)
				bad["e2e_ms"] = map[string]float64{"p50": 0, "p95": 0, "p99": 0, "max": 0}
				save(filepath.Join(root, "summary.json"), bad)
			case "cpu":
				var bad map[string]any
				readJSON(filepath.Join(root, "summary.json"), &bad)
				bad["app_cpu_seconds"] = 9
				save(filepath.Join(root, "summary.json"), bad)
			case "source":
				bad := append([]record(nil), rs...)
				bad[1].SHA = "forged"
				save(filepath.Join(root, "requests.json"), bad)
			case "missing":
				save(filepath.Join(root, "requests.json"), rs[:1])
			case "exit":
				save(filepath.Join(root, "process.json"), map[string]any{"graceful": false, "exit_code": -1})
			}
			if e := audit(root); e == nil {
				t.Fatalf("accepted %s", name)
			}
			write()
			if e := audit(root); e != nil {
				t.Fatalf("restored control: %v", e)
			}
		})
	}
}
