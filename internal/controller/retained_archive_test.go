//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	journal "github.com/Laisky/go-journal"
	"gofluentd/internal/otlpstate"
	"gofluentd/library"
	"gofluentd/library/otlpwire"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func archiveRequest(t *testing.T, id int) *otlpwire.Request {
	t.Helper()
	raw := []byte(fmt.Sprintf("{\"resourceLogs\":[{\"scopeLogs\":[{\"logRecords\":[{\"body\":{\"stringValue\":\"original-%d\"},\"attributes\":[{\"key\":\"nested\",\"value\":{\"kvlistValue\":{\"values\":[]}}}]}]}]}],\"future\":\"unchanged\"}", id))
	r, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(raw), otlpwire.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestOverallArchiveProcessHelper(t *testing.T) {
	if os.Getenv("OVERALL_ARCHIVE_CHILD") != "1" {
		return
	}
	cfg := sharedConfig(os.Getenv("OVERALL_ARCHIVE_ROOT"), os.Getenv("OVERALL_ARCHIVE_GROUP"))
	cfg.RetainAccepted = true
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	for i := 1; i <= 2; i++ {
		if e = p.Admit(context.Background(), archiveRequest(t, i)); e != nil {
			t.Fatal(e)
		}
	}
	capacityDrain(t, p)
	if capacityReceiptCount(t, cfg.Directory) != 2 {
		t.Fatal("transport ACK discarded original recovery archive")
	}
	fmt.Println("OVERALL_ARCHIVE_READY")
	select {}
}
func TestOverallArchiveSurvivesCollectorAndVolatileSinkLoss(t *testing.T) {
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestOverallArchiveProcessHelper$")
	cmd.Env = append(os.Environ(), "OVERALL_ARCHIVE_CHILD=1", "OVERALL_ARCHIVE_ROOT="+root, "OVERALL_ARCHIVE_GROUP="+group)
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
			if sc.Text() == "OVERALL_ARCHIVE_READY" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case yes := <-ready:
		if !yes {
			t.Fatal("child failed before retained archive")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child timeout")
	}
	cmd.Process.Kill()
	cmd.Wait()
	// Child's HTTP-accepted in-memory sink state no longer exists. Recover exact
	// raw requests from durable retained receipts after reopening the same roots.
	cfg := sharedConfig(root, group)
	cfg.RetainAccepted = true
	originals := map[string]bool{}
	for i := 1; i <= 2; i++ {
		originals[string(archiveRequest(t, i).Payload())] = false
	}
	var sent atomic.Int64
	send := func(_ context.Context, e otlpstate.Envelope) (otlpstate.Outcome, error) {
		if _, ok := originals[string(e.Payload)]; !ok {
			t.Fatal("archive changed original raw typed request")
		}
		originals[string(e.Payload)] = true
		sent.Add(1)
		return barrierAccepted(context.Background(), e)
	}
	p, e := OpenOTLPJournal(context.Background(), cfg, []OTLPDestination{{ID: "a", Send: send}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	capacityDrain(t, p)
	if sent.Load() != 0 {
		t.Fatal("normal replay blindly duplicated retained archive")
	}
	before := capacityReceiptCount(t, root)
	// Fill admission capacity, but archive recovery writes no replacement WAL.
	snap, _ := p.budget.Snapshot(context.Background())
	if e = p.budget.Reserve(context.Background(), snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	n, e := p.ReplayAcceptedArchive(context.Background())
	if e != nil || n != 2 || sent.Load() != 2 {
		t.Fatal("retained archive could not repair sink loss", n, e, sent.Load())
	}
	for _, recovered := range originals {
		if !recovered {
			t.Fatal("missing original after recovery")
		}
	}
	if capacityReceiptCount(t, root) != before {
		t.Fatal("recovery HTTP ACK deleted original archive")
	}
	if _, e = OpenOTLPJournal(context.Background(), OTLPJournalConfig{Directory: root}, []OTLPDestination{{ID: "a", Send: barrierAccepted}}); e == nil {
		t.Fatal("unbudgeted downgrade opened retained generation")
	}
}
func TestOverallArchiveLegacyACKedOriginalsRemainReplayable(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 8 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, true)
	backend := overallBackend(t, j)
	// Use the actual reliable writer path and its real receipt barrier.
	input, _ := j.tag2JJInchanMap.Load("fixture")
	ack := make(chan error, 1)
	input.(chan *library.FluentMsg) <- &library.FluentMsg{ID: 1, Tag: "fixture", Message: map[string]interface{}{"body": "chosen original", "future": true}, DurableAck: ack}
	select {
	case e := <-ack:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acceptance timeout")
	}
	m := componentRecv(t, j.outChan)
	if e := backend.WriteId(m.ID); e != nil {
		t.Fatal(e)
	}
	if e := backend.Sync(); e != nil {
		t.Fatal(e)
	}
	out := make(chan *library.FluentMsg, 8)
	if _, e := j.processLegacyMsg(ctx, out); e != nil {
		t.Fatal(e)
	}
	if len(out) != 0 {
		t.Fatal("normal delivery replayed an acknowledged record")
	}
	recovered := 0
	n, e := j.ReadRecoveryArchive(ctx, func(tag string, d *journal.Data) error {
		message := d.Data["message"].(map[string]interface{})
		if tag != "fixture" || d.ID != 1 || message["body"] != "chosen original" || message["future"] != true {
			t.Fatal("legacy archive changed content/context", tag, d.ID)
		}
		recovered++
		return nil
	})
	if e != nil || n != 1 || recovered != 1 {
		t.Fatal("legacy last copy not recoverable", n, recovered, e)
	}
	if retained, e := backend.HasRetainedData(); e != nil || !retained {
		t.Fatal("legacy recovery removed original", retained, e)
	}
}
