//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"gofluentd/internal/diskbudget"
	"gofluentd/internal/otlphttp"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func sharedConfig(root, group string) OTLPJournalConfig {
	return OTLPJournalConfig{Directory: root, SharedStorageDirectory: group, SharedStorageBytes: 32 << 20, WALSegmentBytes: 64 << 10, MaxWALBytes: 8 << 20, MaxStorageBytes: 16 << 20, MaxStorageFiles: 4096, StorageScanMaxEntries: 4096, StorageScanTimeout: time.Second, Limits: otlpstate.Limits{PayloadBytes: 4 << 20, ResponseBytes: 512}, ReceiptGC: true, ReplayBatch: 2}
}
func boundedRequest(t *testing.T, n int) *otlpwire.Request {
	t.Helper()
	r, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewBufferString(fmt.Sprintf("{\"resourceLogs\":[],\"future\":%q}", strings.Repeat("x", n))), otlpwire.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestSharedOTLPBoundedRotationRestartAndExactReplay(t *testing.T) {
	for _, compress := range []bool{false, true} {
		t.Run(fmt.Sprint(compress), func(t *testing.T) {
			root, group := sharedPrivateDir(t), sharedPrivateDir(t)
			cfg := sharedConfig(root, group)
			cfg.Compress = compress
			var calls atomic.Int64
			down := atomic.Bool{}
			down.Store(true)
			send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
				if down.Load() {
					return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("isolated sink down")
				}
				calls.Add(1)
				return barrierAccepted(ctx, e)
			}
			p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
			if e != nil {
				t.Fatal(e)
			}
			req := boundedRequest(t, 4000)
			for i := 0; i < 20; i++ {
				if e = p.Admit(context.Background(), req); e != nil {
					t.Fatal(i, e)
				}
			}
			// Failed destinations leave accepted work durable through rotation and restart.
			for i := 0; i < 5; i++ {
				_, e = p.ReplayBatch(context.Background())
				if p.Err() != nil {
					t.Fatal(e)
				}
			}
			if e = p.Close(); e != nil {
				t.Fatal(e)
			}
			q, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
			if e != nil {
				t.Fatal(e)
			}
			defer q.Close()
			down.Store(false)
			for i := 0; i < 1000; i++ {
				r, e := q.ReplayBatch(context.Background())
				if e != nil {
					t.Fatal(r, e)
				}
				if r.Complete && q.generation.ReleasedThrough == 20 {
					break
				}
			}
			if q.generation.ReleasedThrough != 20 || calls.Load() != 20 {
				t.Fatal("missing/duplicate accepted work", q.generation, calls.Load())
			}
			snap, e := q.budget.Snapshot(context.Background())
			if e != nil || snap.ChargedBytes+snap.ReservedBytes > cfg.SharedStorageBytes {
				t.Fatal(snap, e)
			}
			files, e := os.ReadDir(q.walDir)
			if e != nil {
				t.Fatal(e)
			}
			for _, f := range files {
				if strings.Contains(f.Name(), ".buf") {
					st, _ := f.Info()
					if st.Size() > cfg.WALSegmentBytes {
						t.Fatal("segment exceeded bound", st.Size())
					}
				}
			}
			if capacityReceiptCount(t, root) != 0 {
				t.Fatal("checkpoint-proven receipts not reclaimed")
			}
			for i := 0; i < 3; i++ {
				r, e := q.ReplayBatch(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				_ = r
			}
			if calls.Load() != 20 {
				t.Fatal("quiet replay retransmitted accepted records")
			}
		})
	}
}
func TestSharedOTLPTwoWorkersCapAndAdmissionFalsePositives(t *testing.T) {
	group := sharedPrivateDir(t)
	var ps []*OTLPJournal
	send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("down")
	}
	for i := 0; i < 2; i++ {
		cfg := sharedConfig(sharedPrivateDir(t), group)
		cfg.SharedStorageBytes = 8 << 20
		p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
		if e != nil {
			t.Fatal(e)
		}
		ps = append(ps, p)
		defer p.Close()
	}
	req := boundedRequest(t, 1000)
	var wg sync.WaitGroup
	var refusals atomic.Int64
	for _, p := range ps {
		wg.Add(1)
		go func(p *OTLPJournal) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				before := p.frontier
				e := p.Admit(context.Background(), req)
				if errors.Is(e, ErrOTLPJournalCapacity) {
					refusals.Add(1)
					if p.frontier != before || p.Err() != nil {
						t.Error("refusal mutated identity/fault")
					}
					break
				}
				if e != nil {
					t.Error(e)
					break
				}
			}
		}(p)
	}
	wg.Wait()
	s, e := ps[0].budget.Snapshot(context.Background())
	if e != nil || s.ChargedBytes+s.ReservedBytes > s.Limit || refusals.Load() == 0 {
		t.Fatal(s, e, refusals.Load())
	}
	before, _ := ps[0].budget.Snapshot(context.Background())
	frontier := ps[0].frontier
	if e = ps[0].Admit(context.Background(), req); !errors.Is(e, ErrOTLPJournalCapacity) {
		t.Fatal(e)
	}
	after, _ := ps[0].budget.Snapshot(context.Background())
	if before != after || ps[0].frontier != frontier {
		t.Fatal("refused admission leaked budget")
	}
}
func TestSharedOTLPSizeRefusalLeavesSmallWorkUsable(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	before, _ := p.budget.Snapshot(context.Background())
	if e = p.Admit(context.Background(), boundedRequest(t, 50000)); !errors.Is(e, ErrOTLPJournalCapacity) {
		t.Fatal("oversized segment input accepted", e)
	}
	after, _ := p.budget.Snapshot(context.Background())
	if before != after || p.frontier != 0 {
		t.Fatal("oversized refusal claimed space")
	}
	if e = p.Admit(context.Background(), boundedRequest(t, 10)); e != nil {
		t.Fatal("small valid work falsely refused", e)
	}
	capacityDrain(t, p)
}
func TestSharedOTLPReplayExhaustionStopsBeforeDiscard(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Admit(context.Background(), boundedRequest(t, 1000)); e != nil {
		t.Fatal(e)
	}
	// Simulate repeated crashed replay consuming all conservative shared headroom.
	snap, _ := p.budget.Snapshot(context.Background())
	if e = p.budget.Charge(context.Background(), snap.Limit-snap.ChargedBytes); e != nil {
		t.Fatal(e)
	}
	_, e = p.ReplayBatch(context.Background())
	if !errors.Is(e, diskbudget.ErrCapacity) || p.Err() == nil {
		t.Fatal("full replay not stopped", e)
	}
	p.Close()
	files, _ := filepath.Glob(filepath.Join(cfg.Directory, "wal", "*.buf*"))
	if len(files) == 0 {
		t.Fatal("accepted WAL deleted on capacity fault")
	}
}
func TestLegacyAdmissionThresholdDoesNotBoundReplayControl(t *testing.T) {
	p := journalBarrierOwner(t, sharedPrivateDir(t), barrierAccepted)
	if e := p.Admit(context.Background(), boundedRequest(t, 4000)); e != nil {
		t.Fatal(e)
	}
	before, _, e := otlpDirectoryUsage(context.Background(), p.cfg.Directory, 1<<40, 4096)
	if e != nil {
		t.Fatal(e)
	}
	p.cfg.MaxStorageBytes = before + 1
	r, e := p.ReplayBatch(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	after, _, e := otlpDirectoryUsage(context.Background(), p.cfg.Directory, 1<<40, 4096)
	if e != nil || after <= p.cfg.MaxStorageBytes {
		t.Fatal("legacy bypass not reproduced", before, after, r, e)
	}
}

func sharedPrivateDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if e := os.Chmod(p, 0700); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestSharedOTLPActualHTTPBackpressureAndRecovery(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	receiver, e := otlphttp.NewReceiver(otlphttp.ReceiverConfig{BearerToken: "synthetic-only"}, p.Admit)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(receiver)
	defer server.Close()
	snap, _ := p.budget.Snapshot(context.Background())
	if e = p.budget.Reserve(context.Background(), snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	post := func() int {
		req, _ := http.NewRequest("POST", server.URL+"/v1/logs", strings.NewReader("{\"resourceLogs\":[]}"))
		req.Header.Set("Content-Type", otlpwire.JSON)
		req.Header.Set("Authorization", "Bearer synthetic-only")
		r, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		return r.StatusCode
	}
	if code := post(); code != 503 {
		t.Fatal("full budget returned success", code)
	}
	if p.frontier != 0 || p.SharedBudgetRejected() != 1 {
		t.Fatal("refusal mutated admission or invisible")
	}
	// Release synthetic unused reservation through the same quiescent reconciliation.
	if e = p.budget.Reconcile(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if code := post(); code != 200 {
		t.Fatal("recovered capacity refused valid work", code)
	}
	capacityDrain(t, p)
	if p.generation.ReleasedThrough != 1 {
		t.Fatal("accepted HTTP work not replayed")
	}
}
func TestSharedOTLPProcessChild(t *testing.T) {
	if os.Getenv("SHARED_OTLP_CHILD") != "1" {
		return
	}
	cfg := sharedConfig(os.Getenv("SHARED_OTLP_ROOT"), os.Getenv("SHARED_OTLP_GROUP"))
	send := func(context.Context, otlpstate.Envelope) (otlpstate.Outcome, error) {
		return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("isolated outage")
	}
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 7; i++ {
		if e = p.Admit(context.Background(), boundedRequest(t, 4000)); e != nil {
			t.Fatal(e)
		}
	}
	p.ReplayBatch(context.Background())
	fmt.Println("SHARED_READY")
	select {}
}
func TestSharedOTLPSIGKILLRetainsBudgetAndAcceptedWork(t *testing.T) {
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSharedOTLPProcessChild$")
	cmd.Env = append(os.Environ(), "SHARED_OTLP_CHILD=1", "SHARED_OTLP_ROOT="+root, "SHARED_OTLP_GROUP="+group)
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			if s.Text() == "SHARED_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case yes := <-ready:
		if !yes {
			t.Fatal("child failed before durable acceptance")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	var calls atomic.Int64
	send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		calls.Add(1)
		return barrierAccepted(ctx, e)
	}
	p, e := OpenOTLPJournal(context.Background(), sharedConfig(root, group), []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for i := 0; i < 100; i++ {
		r, e := p.ReplayBatch(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if r.Complete && p.generation.ReleasedThrough == 7 {
			break
		}
	}
	if calls.Load() != 7 || p.generation.ReleasedThrough != 7 {
		t.Fatal("crash lost/duplicated work", calls.Load(), p.generation)
	}
}
func TestSharedOTLPGenerationPreventsUnbudgetedReopen(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	p.Admit(context.Background(), boundedRequest(t, 10))
	capacityDrain(t, p)
	p.Close()
	if _, _, e = openOTLPGeneration(cfg.Directory); e == nil {
		t.Fatal("unbudgeted reopen bypassed retained generation")
	}
}

func TestSharedOTLPOutageDoesNotRotateOrRetireUnacknowledgedSegments(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) { sharedOutageRetainsSource(t, gzip) })
	}
}
func sharedOutageRetainsSource(t *testing.T, gzip bool) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	cfg.Compress = gzip
	down := true
	send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if down {
			return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("down")
		}
		return barrierAccepted(ctx, e)
	}
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for i := 0; i < 5; i++ {
		if e = p.Admit(context.Background(), boundedRequest(t, 4000)); e != nil {
			t.Fatal(e)
		}
	}
	// Finish one failed frozen pass and retain the names, sizes and inode identities.
	for {
		r, _ := p.ReplayBatch(context.Background())
		if p.Err() != nil {
			t.Fatal(p.Err())
		}
		if r.Complete {
			break
		}
	}
	before := map[string]os.FileInfo{}
	entries, _ := os.ReadDir(p.walDir)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".buf") {
			st, _ := entry.Info()
			before[entry.Name()] = st
		}
	}
	for pass := 0; pass < 20; pass++ {
		for {
			r, _ := p.ReplayBatch(context.Background())
			if p.Err() != nil {
				t.Fatal(p.Err())
			}
			if r.Complete {
				break
			}
		}
	}
	entries, _ = os.ReadDir(p.walDir)
	n := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".buf") {
			n++
			st, _ := entry.Info()
			old, ok := before[entry.Name()]
			if !ok || !os.SameFile(old, st) || old.Size() != st.Size() {
				t.Fatal("outage rotated/replaced accepted source", entry.Name())
			}
		}
	}
	if n != len(before) {
		t.Fatal("unacknowledged source segment disappeared")
	}
	down = false
	capacityDrain(t, p)
	if p.generation.ReleasedThrough != 5 || capacityReceiptCount(t, cfg.Directory) != 0 {
		t.Fatal("recovery failed checkpoint/reclamation")
	}
}

func TestSharedOTLPFullAdmissionBudgetStillDrainsAcceptedWork(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	cfg.SharedStorageBytes = 8 << 20
	down := true
	var calls int
	send := func(ctx context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if down {
			return otlpstate.Outcome{Kind: otlpstate.Retryable}, errors.New("isolated down")
		}
		calls++
		return otlpstate.Outcome{Kind: otlpstate.Accepted, HTTPStatus: 200, Response: bytes.Repeat([]byte("r"), 512), Diagnostic: strings.Repeat("d", 4096)}, nil
	}
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	req := boundedRequest(t, 4000)
	n := 0
	for {
		e = p.Admit(context.Background(), req)
		if errors.Is(e, ErrOTLPJournalCapacity) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		n++
		if n > 100 {
			t.Fatal("fixture did not fill")
		}
	}
	if n == 0 {
		t.Fatal("no useful admission")
	}
	// Fill the last unassigned byte with a future reservation, not a fake disk file.
	snap, _ := p.budget.Snapshot(context.Background())
	if e = p.budget.Reserve(context.Background(), snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	full, _ := p.budget.Snapshot(context.Background())
	if full.ChargedBytes+full.ReservedBytes != full.Limit {
		t.Fatal("not exactly full", full)
	}
	down = false
	capacityDrain(t, p)
	if calls != n || p.generation.ReleasedThrough != int64(n) || p.Err() != nil {
		t.Fatal("full budget deadlocked accepted recovery", n, calls, p.generation, p.Err())
	}
	if e = p.Admit(context.Background(), req); e != nil {
		t.Fatal("proof-backed reclamation did not reopen capacity", e)
	}
	capacityDrain(t, p)
}

func TestSharedOTLPRestartReconcilesCrashOvercountAtExactCap(t *testing.T) {
	cfg := sharedConfig(sharedPrivateDir(t), sharedPrivateDir(t))
	cfg.SharedStorageBytes = 8 << 20
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	if e = p.Admit(context.Background(), boundedRequest(t, 4000)); e != nil {
		t.Fatal(e)
	}
	snap, _ := p.budget.Snapshot(context.Background())
	// A durable charge followed by a crash before its physical write is safe
	// overcount. Restart must not need unreserved space merely to refund it.
	if e = p.budget.Charge(context.Background(), snap.Limit-snap.ChargedBytes); e != nil {
		t.Fatal(e)
	}
	p.Close()
	q, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal("overcount deadlocked restart", e)
	}
	defer q.Close()
	capacityDrain(t, q)
	if q.generation.ReleasedThrough != 1 {
		t.Fatal("accepted record lost during overcount recovery")
	}
}
