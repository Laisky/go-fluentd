package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
)

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// audit regenerates the independent caller workload and recomputes all latency
// and capacity fields. It does not trust a passing flag or an application counter.
// Payload equality is checked by the live independent sink; saved timestamps do
// not constitute a cryptographic attestation of a historical destination.
func audit(root string) error {
	var o options
	var rs []record
	var failures []string
	var saved map[string]any
	var process struct {
		ExitCode int  `json:"exit_code"`
		Graceful bool `json:"graceful"`
	}
	var m struct {
		Begin         int64   `json:"begin_ns"`
		Elapsed       float64 `json:"elapsed_seconds"`
		Before, After resources
		MemoryBefore  map[string]uint64 `json:"memory_before"`
		MemoryAfter   map[string]uint64 `json:"memory_after"`
	}
	for _, f := range []struct {
		name string
		dst  any
	}{
		{"options.json", &o}, {"requests.json", &rs}, {"summary.json", &saved},
		{"errors.json", &failures}, {"process.json", &process}, {"measurement.json", &m},
	} {
		if err := readJSON(filepath.Join(root, f.name), f.dst); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
	}
	if saved["passed"] != true || len(failures) != 0 || !process.Graceful || process.ExitCode != 0 {
		return errors.New("trial or process did not complete successfully")
	}
	if o.Requests < 1 || o.Warmup < 1 || o.Destinations < 1 || len(rs) != o.Requests+o.Warmup || m.Elapsed <= 0 {
		return errors.New("incomplete workload")
	}
	for id, r := range rs {
		protocol := o.Protocol
		if protocol == "mixed" {
			protocol = []string{"ndjson", "cloudevents", "logs", "metrics", "traces"}[id%5]
		}
		f := makeFixture(protocol, id, o.Payload)
		if r.ID != id || r.Protocol != protocol || r.SHA != hash(f.Body) {
			return errors.New("caller identity or workload changed")
		}
		status := 200
		if protocol == "ndjson" || protocol == "cloudevents" {
			status = 204
		}
		if r.Dropped || r.Error != "" || r.Status != status {
			return errors.New("failed or dropped request")
		}
		if r.Scheduled <= 0 || r.Start < r.Scheduled || r.Ack < r.Start || len(r.Sink) != o.Destinations {
			return errors.New("invalid request clocks or destinations")
		}
		for _, ts := range r.Sink {
			if ts < r.Start {
				return errors.New("missing or impossible destination receipt")
			}
		}
	}
	actual, err := summarize(rs[o.Warmup:], m.Begin, m.Elapsed, o.Destinations)
	if err != nil {
		return err
	}
	for key, value := range actual {
		var normalized any
		if err := json.Unmarshal(jsonBytes(value), &normalized); err != nil {
			return err
		}
		if !reflect.DeepEqual(saved[key], normalized) {
			return fmt.Errorf("summary does not match requests: %s", key)
		}
	}
	expected := map[string]float64{
		"app_cpu_seconds":          m.After.CPU - m.Before.CPU,
		"app_cpu_us_per_delivered": (m.After.CPU - m.Before.CPU) * 1e6 / float64(o.Requests),
		"app_peak_rss_bytes":       float64(m.After.HWM),
		"app_rss_before_bytes":     float64(m.Before.RSS), "app_rss_after_bytes": float64(m.After.RSS),
		"app_write_bytes": float64(m.After.WriteBytes - m.Before.WriteBytes),
		"app_cpu_cores":   (m.After.CPU - m.Before.CPU) / m.Elapsed,
	}
	for key, value := range expected {
		v, ok := saved[key].(float64)
		if !ok || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v-value) > math.Max(1, math.Abs(value))*1e-12 {
			return fmt.Errorf("summary does not match resource observations: %s", key)
		}
	}
	return nil
}
