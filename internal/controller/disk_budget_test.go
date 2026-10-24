//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	journal "github.com/Laisky/go-journal/v2"
	"gofluentd/internal/diskbudget"
	"gofluentd/library"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func overallLegacy(t *testing.T, cfg *DiskBudgetConfig, gzip bool) (*Journal, context.Context) {
	t.Helper()
	return overallLegacyAt(t, cfg, gzip, sharedPrivateDir(t))
}
func overallLegacyAt(t *testing.T, cfg *DiskBudgetConfig, gzip bool, root string) (*Journal, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	j := NewJournal(ctx, &JournalCfg{DiskBudget: cfg, BufDirPath: root, BufSizeBytes: 64 << 10, IsCompress: gzip, JournalOutChanLen: 512, CommitIDChanLen: 512, ChildJournalDataInchanLen: 512, ChildJournalIDInchanLen: 512, MsgPool: &sync.Pool{New: func() interface{} { return &library.FluentMsg{} }}})
	t.Cleanup(func() {
		cancel()
		select {
		case <-j.directoriesClosed:
		case <-time.After(3 * time.Second):
			t.Error("legacy budget owner did not stop")
		}
	})
	if e := j.createJournalRunner(ctx, "fixture"); e != nil {
		t.Fatal(e)
	}
	return j, ctx
}
func overallBackend(t *testing.T, j *Journal) *journal.Journal {
	t.Helper()
	v, ok := j.tag2JMap.Load("fixture")
	if !ok {
		t.Fatal("missing tag")
	}
	return v.(*journal.Journal)
}
func TestOverallDiskConfigAppliesToEveryPipeline(t *testing.T) {
	cfg, e := ParseDiskBudgetConfig(map[string]interface{}{"directory": sharedPrivateDir(t), "max_bytes": 3000000000.0})
	if e != nil {
		t.Fatal(e)
	}
	otlp := &OTLPServiceConfig{}
	if e = cfg.ApplyOTLP(otlp); e != nil || otlp.SharedStorageDirectory != cfg.Directory || otlp.SharedStorageBytes != 3000000000 {
		t.Fatal(otlp, e)
	}
	if _, e = ParseDiskBudgetConfig(map[string]interface{}{"directory": cfg.Directory, "max_bytes": float64(10 << 30)}); e != nil {
		t.Fatal("other deployments artificially capped at b1 size", e)
	}
	for _, bad := range []map[string]interface{}{
		{"directory": cfg.Directory, "max_bytes": 1.5},
		{"directory": cfg.Directory, "max_bytes": "3000000000"},
		{"directory": cfg.Directory, "max_bytes": -1},
		{"directory": cfg.Directory, "unknown": "not-a-setting"},
	} {
		if _, e = ParseDiskBudgetConfig(bad); e == nil {
			t.Fatal("invalid config accepted", bad)
		}
	}
	if e = cfg.ApplyOTLP(&OTLPServiceConfig{SharedStorageDirectory: cfg.Directory + "-different"}); e == nil {
		t.Fatal("conflicting subpipeline silently omitted")
	}
	if cfg, e = ParseDiskBudgetConfig(nil); e != nil || cfg != nil {
		t.Fatal("old deployments changed", cfg, e)
	}
}
func TestOverallDiskLegacyAndOTLPShareOneCapAndDrain(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 16 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, false)
	backend := overallBackend(t, j)
	oc := sharedConfig(sharedPrivateDir(t), cfg.Directory)
	if e := cfg.ApplyOTLP(&OTLPServiceConfig{}); e != nil {
		t.Fatal(e)
	}
	oc.SharedStorageBytes = cfg.MaxBytes
	p, e := OpenOTLPJournal(ctx, oc, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	// The quota does not promise fairness: one goroutine can fill it before
	// the other starts. Seed both formats so the concurrent saturation/drain
	// assertions always cover accepted work from each pipeline.
	first := &journal.Data{ID: 1, Data: map[string]interface{}{"tag": "fixture", "message": map[string]interface{}{"synthetic": "1"}}}
	if e = backend.WriteData(first); e != nil {
		t.Fatal(e)
	}
	if e = p.Admit(ctx, boundedRequest(t, 1000)); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	legacyN, otlpN := 1, 1
	wg.Add(2)
	go func() {
		defer wg.Done()
		for id := 2; id < 100; id++ {
			d := &journal.Data{ID: int64(id), Data: map[string]interface{}{"tag": "fixture", "message": map[string]interface{}{"synthetic": fmt.Sprint(id)}}}
			e := backend.WriteData(d)
			if errors.Is(e, diskbudget.ErrCapacity) {
				break
			}
			if e != nil {
				t.Error(e)
				break
			}
			legacyN++
		}
	}()
	go func() {
		defer wg.Done()
		for i := 1; i < 100; i++ {
			e := p.Admit(ctx, boundedRequest(t, 1000))
			if errors.Is(e, ErrOTLPJournalCapacity) {
				break
			}
			if e != nil {
				t.Error(e)
				break
			}
			otlpN++
		}
	}()
	wg.Wait()
	if legacyN == 0 || otlpN == 0 {
		t.Fatal("fixture admitted no useful work", legacyN, otlpN)
	}
	s, e := j.diskBudget.Snapshot(ctx)
	if e != nil || s.ChargedBytes+s.ReservedBytes > s.Limit || s.Limit != cfg.MaxBytes {
		t.Fatal("cap multiplied across formats", s, e)
	}
	if e = backend.Sync(); e != nil {
		t.Fatal("funded accepted legacy Sync blocked", e)
	}
	// Fill the final unassigned headroom; existing accepted work retains its
	// own funded ACK, outcome and rotation space.
	s, _ = j.diskBudget.Snapshot(ctx)
	if e = j.diskBudget.Reserve(ctx, s.Limit-s.ChargedBytes-s.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	capacityDrain(t, p)
	s, _ = p.budget.Snapshot(ctx)
	if e = p.budget.Reserve(ctx, s.Limit-s.ChargedBytes-s.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	out := make(chan *library.FluentMsg, 512)
	if _, e = j.processLegacyMsg(ctx, out); e != nil {
		t.Fatal("full budget blocked retained source", e)
	}
	if len(out) != legacyN {
		t.Fatal("legacy accepted work missing", len(out), legacyN)
	}
	for len(out) > 0 {
		m := <-out
		if e = backend.WriteId(m.ID); e != nil {
			t.Fatal("full budget blocked funded ACK", e)
		}
	}
	if e = backend.Sync(); e != nil {
		t.Fatal(e)
	}
	if _, e = j.processLegacyMsg(ctx, out); e != nil {
		t.Fatal(e)
	}
	if len(out) != 0 {
		t.Fatal("acknowledged legacy records resent")
	}
	if backend.HasUnacknowledgedData() {
		t.Fatal("transport-acknowledged source still pending")
	}
	if retained, e := backend.HasRetainedData(); e != nil || !retained {
		t.Fatal("last recoverable legacy copy was discarded", retained, e)
	}
	if p.generation.ReleasedThrough != int64(otlpN) {
		t.Fatal("OTLP did not drain")
	}
	if e = backend.WriteData(&journal.Data{ID: 1000, Data: map[string]interface{}{"tag": "fixture", "message": map[string]interface{}{"after": "recovery"}}}); e != nil {
		t.Fatal("reclaimed capacity unusable", e)
	}
}
func TestOverallDiskLegacyRefusalIsExplicitAndDoesNotPersist(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 8 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, false)
	backend := overallBackend(t, j)
	s, _ := j.diskBudget.Snapshot(ctx)
	if e := j.diskBudget.Reserve(ctx, s.Limit-s.ChargedBytes-s.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	receipt := make(chan error, 1)
	v, _ := j.tag2JJInchanMap.Load("fixture")
	v.(chan *library.FluentMsg) <- &library.FluentMsg{ID: 1, Tag: "fixture", Message: map[string]interface{}{"body": "synthetic refused"}, DurableAck: receipt}
	select {
	case e := <-receipt:
		if !errors.Is(e, diskbudget.ErrCapacity) {
			t.Fatal("full legacy input falsely acknowledged", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no explicit legacy refusal")
	}
	if len(j.outChan) != 0 || j.diskRefused.Load() == 0 {
		t.Fatal("refusal forwarded a success or was invisible")
	}
	if id, e := backend.LoadMaxId(); e != nil || id != 0 {
		t.Fatal("refused record persisted", id, e)
	}
	if e := j.reconcileOverallBudget(ctx); e != nil {
		t.Fatal(e)
	}
	receipt = make(chan error, 1)
	v.(chan *library.FluentMsg) <- &library.FluentMsg{ID: 2, Tag: "fixture", Message: map[string]interface{}{"body": "synthetic accepted"}, DurableAck: receipt}
	select {
	case e := <-receipt:
		if e != nil {
			t.Fatal("recovered capacity refused useful input", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovery receipt timeout")
	}
	m := componentRecv(t, j.outChan)
	if m.ID != 2 {
		t.Fatal("rejected input appeared after recovery", m.ID)
	}
}
func TestOverallDiskLegacyRetainsUnacknowledgedPlainAndGzip(t *testing.T) {
	for _, gzip := range []bool{false, true} {
		t.Run(fmt.Sprint(gzip), func(t *testing.T) {
			cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 32 << 20, SegmentBytes: 64 << 10}
			j, ctx := overallLegacy(t, cfg, gzip)
			backend := overallBackend(t, j)
			for id := int64(1); id <= 12; id++ {
				if e := backend.WriteData(&journal.Data{ID: id, Data: map[string]interface{}{"tag": "fixture", "message": map[string]interface{}{"body": fmt.Sprintf("%04000d", id)}}}); e != nil {
					t.Fatal(e)
				}
				if e := backend.RotateIfFull(ctx); e != nil {
					t.Fatal(e)
				}
			}
			beforeFreeze, _ := filepath.Glob(filepath.Join(j.BufDirPath, "fixture", "*.buf*"))
			if len(beforeFreeze) < 2 {
				t.Fatal("write-size rotation not exercised")
			}
			if e := backend.RotateIfFull(ctx); e != nil {
				t.Fatal(e)
			}
			if e := backend.Sync(); e != nil {
				t.Fatal(e)
			}
			out := make(chan *library.FluentMsg, 128)
			if _, e := j.processLegacyMsg(ctx, out); e != nil {
				t.Fatal(e)
			}
			root := filepath.Join(j.BufDirPath, "fixture")
			before := map[string]os.FileInfo{}
			files, _ := filepath.Glob(filepath.Join(root, "*.buf*"))
			if len(files) < 2 {
				t.Fatal("size rotation not exercised", files)
			}
			for _, p := range files {
				st, _ := os.Stat(p)
				before[p] = st
				if st.Size() > cfg.SegmentBytes {
					t.Fatal("oversized bounded segment", st.Size())
				}
			}
			for pass := 0; pass < 4; pass++ {
				if _, e := j.processLegacyMsg(ctx, out); e != nil {
					t.Fatal(e)
				}
			}
			files, _ = filepath.Glob(filepath.Join(root, "*.buf*"))
			if len(files) != len(before) {
				t.Fatal("outage manufactured data segments")
			}
			for _, p := range files {
				st, _ := os.Stat(p)
				old, ok := before[p]
				if !ok || !os.SameFile(st, old) || st.Size() != old.Size() {
					t.Fatal("unACKed source changed", p)
				}
			}
			for id := int64(1); id <= 12; id++ {
				if e := backend.WriteId(id); e != nil {
					t.Fatal(e)
				}
			}
			if e := backend.Sync(); e != nil {
				t.Fatal(e)
			}
			if _, e := j.processLegacyMsg(ctx, out); e != nil {
				t.Fatal(e)
			}
			if backend.HasUnacknowledgedData() {
				t.Fatal("ACK transport progress incomplete")
			}
			if retained, e := backend.HasRetainedData(); e != nil || !retained {
				t.Fatal("archive discarded on transport ACK", retained, e)
			}
		})
	}
}

func TestOverallDiskLegacyProcessHelper(t *testing.T) {
	if os.Getenv("OVERALL_LEGACY_CHILD") != "1" {
		return
	}
	cfg := &DiskBudgetConfig{Directory: os.Getenv("OVERALL_LEGACY_GROUP"), MaxBytes: 8 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacyAt(t, cfg, true, os.Getenv("OVERALL_LEGACY_ROOT"))
	backend := overallBackend(t, j)
	for id := int64(1); id <= 4; id++ {
		if e := backend.WriteData(&journal.Data{ID: id, Data: map[string]interface{}{"tag": "fixture", "message": map[string]interface{}{"body": fmt.Sprintf("%04000d", id)}}}); e != nil {
			t.Fatal(e)
		}
	}
	if e := backend.Sync(); e != nil {
		t.Fatal(e)
	}
	snap, _ := j.diskBudget.Snapshot(ctx)
	if e := j.diskBudget.Charge(ctx, snap.Limit-snap.ChargedBytes); e != nil {
		t.Fatal(e)
	}
	fmt.Println("OVERALL_LEGACY_READY")
	select {}
}
func TestOverallDiskLegacySIGKILLFullLedgerRecovery(t *testing.T) {
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOverallDiskLegacyProcessHelper$")
	cmd.Env = append(os.Environ(), "OVERALL_LEGACY_CHILD=1", "OVERALL_LEGACY_GROUP="+group, "OVERALL_LEGACY_ROOT="+root)
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
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if sc.Text() == "OVERALL_LEGACY_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case yes := <-ready:
		if !yes {
			t.Fatal("child did not fund durable work")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	cfg := &DiskBudgetConfig{Directory: group, MaxBytes: 8 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacyAt(t, cfg, true, root)
	backend := overallBackend(t, j)
	if max, e := j.LoadMaxID(); e != nil || max < 4 {
		t.Fatal("restart lost identity frontier", max, e)
	}
	out := make(chan *library.FluentMsg, 16)
	if _, e = j.processLegacyMsg(ctx, out); e != nil {
		t.Fatal("full-ledger crash recovery deadlocked", e)
	}
	if len(out) != 4 {
		t.Fatal("missing accepted legacy records", len(out))
	}
	for len(out) > 0 {
		m := <-out
		if e = backend.WriteId(m.ID); e != nil {
			t.Fatal(e)
		}
	}
	if e = backend.Sync(); e != nil {
		t.Fatal(e)
	}
	if _, e = j.processLegacyMsg(ctx, out); e != nil {
		t.Fatal(e)
	}
	if backend.HasUnacknowledgedData() {
		t.Fatal("recovery did not record ACK transport progress")
	}
	if retained, e := backend.HasRetainedData(); e != nil || !retained {
		t.Fatal("recovery discarded retained originals", retained, e)
	}
	if max, e := j.LoadMaxID(); e != nil || max < 4 {
		t.Fatal("ACK frontier lost during reclamation", max, e)
	}
}
