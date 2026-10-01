//go:build (linux || darwin) && otlp_b1_measure

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/log"
	"gofluentd/library/otlpwire"
)

type b1Resource struct {
	UserSeconds, SystemSeconds float64
	MaxRSSKiB                  int64
	ReadBytes, WriteBytes      int64
}

func b1Resources(t *testing.T) b1Resource {
	t.Helper()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	r := b1Resource{UserSeconds: float64(usage.Utime.Sec) + float64(usage.Utime.Usec)/1e6,
		SystemSeconds: float64(usage.Stime.Sec) + float64(usage.Stime.Usec)/1e6, MaxRSSKiB: usage.Maxrss}
	if runtime.GOOS == "darwin" {
		r.MaxRSSKiB /= 1024
		return r // /proc disk I/O is available only for the actual Linux host run
	}
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		n, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		switch fields[0] {
		case "read_bytes:":
			r.ReadBytes = n
		case "write_bytes:":
			r.WriteBytes = n
		}
	}
	return r
}

// Explicitly selected, bounded qualification only. Unlike t.TempDir, the owned
// synthetic state is preserved for inspection. No listener, network, model,
// production path or external destination is used. Host/container bounds must
// also be applied by the caller; this measurement never claims business latency.
func TestOTLPB1IsolatedCapacity(t *testing.T) {
	_ = log.Logger.ChangeLevel("error")
	_ = journal.Logger.ChangeLevel("error")
	const healthy, offeredDown = 12, 96 // total attempted requests 108, below 128
	paced := os.Getenv("OTLP_MEASURE_PACED") == "1"
	pace, holdInterval := 500*time.Millisecond, time.Second
	if os.Getenv("OTLP_MEASURE_FAST_VALIDATE") == "1" {
		pace, holdInterval = 5*time.Millisecond, 5*time.Millisecond
	}
	root, err := os.MkdirTemp(os.Getenv("OTLP_MEASURE_STATE"), "synthetic-")
	if err != nil {
		t.Fatal(err)
	}
	cfg := OTLPJournalConfig{Directory: root, MaxWALBytes: 128 << 10, MaxStorageBytes: 512 << 10,
		StorageScanMaxEntries: 1024, StorageScanTimeout: 10 * time.Millisecond,
		ReceiptGC: true, ReplayBatch: 8, ReplayInterval: time.Second}
	online := true
	delivered := map[string]int{}
	peer := OTLPDestination{ID: "synthetic-home", Send: func(_ context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if !online {
			return otlpstate.Outcome{Kind: otlpstate.Retryable, HTTPStatus: 503}, nil
		}
		if e.Items != 1 {
			return otlpstate.Outcome{}, errors.New("synthetic fixture did not contain one item")
		}
		delivered[string(e.Payload)]++
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
	}}
	p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{peer})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	requests := make([]*otlpwire.Request, healthy+offeredDown)
	for i := range requests {
		signal := []otlpwire.Signal{otlpwire.Logs, otlpwire.Metrics, otlpwire.Traces}[i%3]
		item := `"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"synthetic"}}]}]}]`
		if signal == otlpwire.Metrics {
			item = `"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"synthetic","gauge":{"dataPoints":[{"asDouble":1}]}}]}]}]`
		} else if signal == otlpwire.Traces {
			item = `"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"synthetic","traceId":"00000000000000000000000000000001","spanId":"0000000000000001"}]}]}]`
		}
		body := []byte(fmt.Sprintf(`{%s,"future":{"id":%d,"padding":%q}}`, item, i, strings.Repeat("x", 2048)))
		requests[i], err = otlpwire.ReadRequest(signal, otlpwire.JSON, "", bytes.NewReader(body), otlpwire.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
	}
	report := func(phase string, start time.Time, before b1Resource, data map[string]interface{}) {
		t.Helper()
		after := b1Resources(t)
		var logical, allocated, walBytes int64
		entries := 0
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if path != root {
				entries++
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				logical += info.Size()
				allocated += info.Sys().(*syscall.Stat_t).Blocks * 512
				if filepath.Dir(path) == p.walDir {
					walBytes += info.Size()
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if allocated > 16<<20 || logical > 16<<20 {
			t.Fatal("isolated state exceeded 16MiB bound")
		}
		data["phase"], data["wall_seconds"] = phase, time.Since(start).Seconds()
		data["cpu_user_seconds"], data["cpu_system_seconds"] = after.UserSeconds-before.UserSeconds, after.SystemSeconds-before.SystemSeconds
		data["max_rss_kib"], data["read_bytes"], data["write_bytes"] = after.MaxRSSKiB, after.ReadBytes-before.ReadBytes, after.WriteBytes-before.WriteBytes
		data["root_logical_bytes"], data["root_allocated_bytes"], data["wal_logical_bytes"], data["entries"] = logical, allocated, walBytes, entries
		data["scan_budget_rejected"], data["frontier"] = p.ScanBudgetRejected(), p.frontier
		data["platform"], data["proc_disk_io_available"] = runtime.GOOS, runtime.GOOS == "linux"
		encoded, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Log("B1_SYNTHETIC " + string(encoded))
	}
	admit := func(phase string, requests []*otlpwire.Request) []*otlpwire.Request {
		t.Helper()
		start, before := time.Now(), b1Resources(t)
		var admitted []*otlpwire.Request
		counts, drops := map[otlpwire.Signal]int{}, map[otlpwire.Signal]int{}
		latency := make([]float64, 0, len(requests))
		scheduledReplay := 0
		for i, request := range requests {
			began := time.Now()
			err := p.Admit(context.Background(), request)
			latency = append(latency, float64(time.Since(began).Microseconds())/1000)
			if err == nil {
				admitted = append(admitted, request)
				counts[request.Signal()]++
			} else if errors.Is(err, ErrOTLPJournalCapacity) {
				drops[request.Signal()]++
			} else {
				t.Fatal(err)
			}
			if paced && phase == "home_down_growing_to_full" {
				// Drive the existing bounded replay API every four offered inputs;
				// the peer remains retryable. No new scheduler or transport is added.
				if i%4 == 3 {
					_, err := p.ReplayBatch(context.Background())
					if err != nil && !errors.Is(err, ErrOTLPDeliveryPending) {
						t.Fatal(err)
					}
					scheduledReplay++
				}
				time.Sleep(pace)
			}
		}
		sort.Float64s(latency)
		report(phase, start, before, map[string]interface{}{"offered": len(requests), "admitted": len(admitted), "admitted_per_signal": counts,
			"refused_per_signal": drops, "admission_p50_ms": latency[len(latency)/2], "admission_p95_ms": latency[(len(latency)-1)*95/100], "admission_max_ms": latency[len(latency)-1],
			"paced": paced, "pace_ms": pace.Milliseconds(), "scheduled_replay_batches": scheduledReplay})
		return admitted
	}
	drain := func(phase string) OTLPBatchReport {
		t.Helper()
		start, before := time.Now(), b1Resources(t)
		var total OTLPBatchReport
		for i := 0; i < 18; i++ { // bounded even if a consumer contract regresses
			r, err := p.ReplayBatch(context.Background())
			if err != nil && !errors.Is(err, ErrOTLPDeliveryPending) {
				t.Fatal(err)
			}
			total.Seen += r.Seen
			total.Delivered += r.Delivered
			total.Pending += r.Pending
			if r.Complete {
				total.Complete = true
				break
			}
		}
		if !total.Complete || p.Err() != nil {
			t.Fatal("bounded synthetic replay did not finish its snapshot", p.Err())
		}
		report(phase, start, before, map[string]interface{}{"seen": total.Seen, "delivered": total.Delivered, "pending": total.Pending,
			"reclaimed": p.receiptsReclaimed.Load(), "checkpoint": p.generation.ReleasedThrough})
		return total
	}
	accepted := admit("healthy_admission", requests[:healthy])
	if len(accepted) != healthy || drain("healthy_direct_replay").Delivered != healthy {
		t.Fatal("chosen caps could not admit/deliver healthy fixtures")
	}
	online = false
	downAccepted := admit("home_down_growing_to_full", requests[healthy:])
	if len(downAccepted) == 0 || len(downAccepted) == offeredDown {
		t.Fatal("bounded fixture did not exercise both backlog growth and capacity refusal")
	}
	if paced {
		// A smallest fixture's admission reserve proves the owned WAL stays
		// above its normal admission threshold throughout the full-state hold.
		minReserve := int64(1 << 62)
		for _, request := range requests[:3] {
			plan, err := p.producer.Plan(p.namespace, p.frontier+1, otlpstate.Envelope{Signal: string(request.Signal()), ContentType: request.ContentType(), Items: int64(request.Items()), Payload: request.Payload()})
			if err != nil {
				t.Fatal(err)
			}
			data, err := plan.JournalData()
			if err != nil {
				t.Fatal(err)
			}
			minReserve = min(minReserve, int64(len(data.Data["otlp_delivery"].([]byte)))*2+1024)
		}
		start, before := time.Now(), b1Resources(t)
		heldBatches := 0
		for i := 0; i < 10; i++ {
			wal, err := p.storageBytes(context.Background())
			if err != nil || wal <= cfg.MaxWALBytes-minReserve {
				t.Fatalf("owned backlog did not remain full for smallest fixture: wal=%d reserve=%d err=%v", wal, minReserve, err)
			}
			_, err = p.ReplayBatch(context.Background())
			if err != nil && !errors.Is(err, ErrOTLPDeliveryPending) {
				t.Fatal(err)
			}
			heldBatches++
			time.Sleep(holdInterval)
		}
		report("home_down_full_hold", start, before, map[string]interface{}{"held_replay_batches": heldBatches, "hold_interval_ms": holdInterval.Milliseconds(), "smallest_fixture_reserve": minReserve})
		// Paced replay can stop partway through a frozen snapshot. Finish that
		// cursor before measuring a fresh complete snapshot of the whole backlog.
		drain("home_down_remaining_snapshot")
	}
	if drain("home_down_pending_replay").Pending != len(downAccepted) {
		t.Fatal("retryable synthetic destination did not retain admitted backlog")
	}
	online = true
	if drain("home_recovery_replay").Delivered != len(downAccepted) {
		t.Fatal("repair did not deliver retained backlog")
	}
	accepted = append(accepted, downAccepted...)
	for _, request := range accepted {
		if delivered[string(request.Payload())] != 1 {
			t.Fatal("admitted original payload lost or delivered twice")
		}
	}
	if p.generation.ReleasedThrough != p.frontier || p.receiptsReclaimed.Load() != uint64(len(accepted)) {
		t.Fatal("recovery did not complete checkpoint/receipt reclamation")
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("B1_SYNTHETIC_FINAL state=%s attempted=%d admitted=%d delivered=%d preserved=true transport=in-process-no-network", root, len(requests), len(accepted), len(delivered))
}
