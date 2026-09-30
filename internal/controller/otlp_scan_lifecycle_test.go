//go:build otlp_lifecycle_measure

package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/log"
	"gofluentd/library/otlpwire"
)

// This bounded healthy lifecycle measurement uses the proposed edge scan caps.
// It measures the local filesystem/in-process peer only, with no HTTP/TLS or
// production probes. Timing is reported, never asserted as a portable SLA.
func TestOTLPJournalChosenScanCapsHealthyLifecycle(t *testing.T) {
	t.Run("reopen4x72", func(t *testing.T) { measureChosenScanCapsLifecycle(t, 4, 24) })
	t.Run("sustained3x256", func(t *testing.T) { measureChosenScanCapsLifecycle(t, 1, 256) })
}

func measureChosenScanCapsLifecycle(t *testing.T, cycles, perSignal int) {
	t.Helper()
	_ = log.Logger.ChangeLevel("error")
	_ = journal.Logger.ChangeLevel("error")
	cfg := OTLPJournalConfig{Directory: t.TempDir(), MaxWALBytes: 1 << 30, MaxStorageBytes: 2 << 30,
		StorageScanMaxEntries: 1024, StorageScanTimeout: 10 * time.Millisecond,
		ReceiptGC: true, ReplayBatch: 16, ReplayInterval: 10 * time.Millisecond}
	signals := []otlpwire.Signal{otlpwire.Logs, otlpwire.Metrics, otlpwire.Traces}
	var totalAccepted, totalRefused, highwater int
	var totalAdmissionTime, totalLifecycleTime time.Duration
	var namespace string
	for cycle := 0; cycle < cycles; cycle++ {
		var mu sync.Mutex
		admitted, delivered := map[string]bool{}, map[string]int{}
		acceptedSignal, refusedSignal := map[otlpwire.Signal]int{}, map[otlpwire.Signal]int{}
		var measureErr error
		peer := OTLPDestination{ID: "home", Send: func(_ context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
			mu.Lock()
			defer mu.Unlock()
			if e.Items != 1 {
				return otlpstate.Outcome{}, fmt.Errorf("expected one synthetic item, got %d", e.Items)
			}
			delivered[string(e.Payload)]++
			return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200}, nil
		}}
		p, err := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{peer})
		if err != nil {
			t.Fatal(err)
		}
		if namespace != "" && namespace != p.Namespace() {
			t.Fatal("namespace changed after healthy receipt GC/reopen")
		}
		namespace = p.Namespace()
		runCtx, stopRun := context.WithCancel(context.Background())
		runDone := make(chan error, 1)
		go func() { runDone <- p.Run(runCtx) }()
		observeDone := make(chan struct{})
		observeCtx, stopObserve := context.WithCancel(context.Background())
		cycleHighwater := 0
		go func() {
			defer close(observeDone)
			ticker := time.NewTicker(2 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-observeCtx.Done():
					return
				case <-ticker.C:
					root, rootErr := os.ReadDir(cfg.Directory)
					wal, walErr := os.ReadDir(p.walDir)
					if rootErr != nil || walErr != nil {
						mu.Lock()
						measureErr = errors.Join(measureErr, rootErr, walErr)
						mu.Unlock()
						return
					}
					// Root + WAL entries match the cumulative recursive admission
					// traversal in this flat receipt layout, including directories.
					cycleHighwater = max(cycleHighwater, len(root)+len(wal))
				}
			}
		}()
		t.Cleanup(func() {
			stopObserve()
			<-observeDone
			stopRun()
			p.Close()
		})
		requests := map[otlpwire.Signal][]*otlpwire.Request{}
		for _, signal := range signals {
			for i := 0; i < perSignal; i++ {
				var item string
				switch signal {
				case otlpwire.Logs:
					item = `"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"synthetic"}}]}]}]`
				case otlpwire.Metrics:
					item = `"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"synthetic","gauge":{"dataPoints":[{"asDouble":1}]}}]}]}]`
				case otlpwire.Traces:
					item = `"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"synthetic","traceId":"00000000000000000000000000000001","spanId":"0000000000000001"}]}]}]`
				}
				body := []byte(fmt.Sprintf(`{%s,"future":{"id":%q,"padding":%q}}`, item, fmt.Sprintf("%d-%s-%d", cycle, signal, i), strings.Repeat("x", 2048)))
				request, err := otlpwire.ReadRequest(signal, otlpwire.JSON, "", bytes.NewReader(body), otlpwire.DefaultLimits())
				if err != nil {
					t.Fatal(err)
				}
				requests[signal] = append(requests[signal], request)
			}
		}
		started := time.Now()
		var producers sync.WaitGroup
		for _, signal := range signals {
			producers.Add(1)
			go func(signal otlpwire.Signal) {
				defer producers.Done()
				for _, request := range requests[signal] {
					err := p.Admit(context.Background(), request)
					mu.Lock()
					switch {
					case err == nil:
						admitted[string(request.Payload())] = true
						acceptedSignal[signal]++
					case errors.Is(err, ErrOTLPJournalCapacity):
						refusedSignal[signal]++
					default:
						measureErr = errors.Join(measureErr, err)
					}
					mu.Unlock()
				}
			}(signal)
		}
		producers.Wait()
		admissionTime := time.Since(started)
		deadline := time.Now().Add(30 * time.Second)
		for {
			mu.Lock()
			allReceived := len(admitted) == len(delivered)
			mu.Unlock()
			// Receipt pruning proves a complete snapshot passed its final ACKs
			// and checkpoint; callback arrival alone does not prove local durability.
			if allReceived && p.Counters().Accepted == uint64(len(admitted)) && p.receiptsReclaimed.Load() == uint64(len(admitted)) {
				break
			}
			if time.Now().After(deadline) {
				mu.Lock()
				measureErr = errors.Join(measureErr, errors.New("healthy recovery failed to complete"))
				mu.Unlock()
				break
			}
			time.Sleep(time.Millisecond)
		}
		lifecycleTime := time.Since(started)
		stopObserve()
		<-observeDone
		stopRun()
		if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
			measureErr = errors.Join(measureErr, err)
		}
		if err := p.Close(); err != nil {
			measureErr = errors.Join(measureErr, err)
		}
		if p.generation.ReleasedThrough != p.frontier {
			measureErr = errors.Join(measureErr, errors.New("healthy checkpoint did not release complete admitted prefix"))
		}
		entries, err := os.ReadDir(cfg.Directory)
		if err != nil {
			measureErr = errors.Join(measureErr, err)
		}
		for _, entry := range entries {
			if (len(entry.Name()) == 69 && strings.HasSuffix(entry.Name(), ".json")) || strings.HasPrefix(entry.Name(), ".pending-") {
				measureErr = errors.Join(measureErr, errors.New("healthy cycle retained receipt or incomplete evidence"))
			}
		}
		for payload := range admitted {
			if delivered[payload] != 1 {
				t.Fatalf("lost/duplicated admitted synthetic payload: calls=%d", delivered[payload])
			}
		}
		for _, signal := range signals {
			if acceptedSignal[signal] == 0 {
				t.Errorf("chosen caps refused all normal %s telemetry", signal)
			}
			totalRefused += refusedSignal[signal]
		}
		if measureErr != nil {
			t.Fatal(measureErr)
		}
		totalAccepted += len(admitted)
		totalAdmissionTime += admissionTime
		totalLifecycleTime += lifecycleTime
		highwater = max(highwater, cycleHighwater)
		t.Logf("cycle=%d admitted=%v refused=%v scan_budget_refused=%d sampled_root_wal_entries_highwater=%d admission_s=%.3f lifecycle_s=%.3f reclaimed=%d",
			cycle, acceptedSignal, refusedSignal, p.ScanBudgetRejected(), cycleHighwater, admissionTime.Seconds(), lifecycleTime.Seconds(), p.receiptsReclaimed.Load())
	}
	t.Logf("Mac-only total offered=%d admitted=%d refused=%d admission_msgs_s=%.2f complete_lifecycle_msgs_s=%.2f sampled_entries_highwater=%d caps=1024entries/10ms cycles=%d",
		cycles*perSignal*3, totalAccepted, totalRefused, float64(totalAccepted)/totalAdmissionTime.Seconds(), float64(totalAccepted)/totalLifecycleTime.Seconds(), highwater, cycles)
}
