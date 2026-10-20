//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"fmt"
	"gofluentd/internal/diskbudget"
	"gofluentd/internal/otlpstate"
	"gofluentd/library/otlpwire"
	"testing"
	"time"
)

func TestRetainedHealthyStreamOutlivesLooseReceiptCeiling(t *testing.T) {
	ctx := context.Background()
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cfg := sharedConfig(root, group)
	cfg.RetainAccepted = true
	cfg.MaxStorageFiles = 64
	cfg.StorageScanMaxEntries = 64
	cfg.StorageScanTimeout = time.Second
	cfg.ReplayBatch = 8
	cfg.MaxWALBytes = 8 << 20
	cfg.MaxStorageBytes = 32 << 20
	p, e := OpenOTLPJournal(ctx, cfg, []OTLPDestination{{ID: "home", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	want := map[string]bool{}
	for i := 0; i < 128; i++ {
		raw := []byte(fmt.Sprintf(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"original-%d"}}]}]}]}`, i))
		r, e := otlpwire.ReadRequest(otlpwire.Logs, otlpwire.JSON, "", bytes.NewReader(raw), otlpwire.DefaultLimits())
		if e != nil {
			t.Fatal(e)
		}
		if e = p.Admit(ctx, r); e != nil {
			t.Fatalf("healthy retained stream stopped at%d under unchanged64entry ceiling: %v", i, e)
		}
		capacityDrain(t, p)
		want[string(raw)] = true
	}
	count, e := p.store.WalkAcceptedArchive(ctx, p.namespace, func(_ otlpstate.Key, v otlpstate.Envelope) error {
		if !want[string(v.Payload)] {
			return fmt.Errorf("changed or duplicate original")
		}
		delete(want, string(v.Payload))
		return nil
	})
	if e != nil || count != 128 || len(want) != 0 {
		t.Fatal("retained originals not exactly recoverable", count, len(want), e)
	}
	snap, e := p.budget.Snapshot(ctx)
	if e != nil || snap.ChargedBytes+snap.ReservedBytes > snap.Limit {
		t.Fatal(snap, e)
	}
	if cfg.StorageScanMaxEntries != 64 || p.cfg.MaxStorageFiles != 64 {
		t.Fatal("file budgets were raised")
	}
	t.Logf("accepted=128 recovered=%d unchanged_entry_limit=64 bytes_charged=%d bytes_reserved=%d", count, snap.ChargedBytes, snap.ReservedBytes)
}

func TestRetainedPackingKeepsFundingAcrossPartialBatchRestart(t *testing.T) {
	ctx := context.Background()
	root, group := sharedPrivateDir(t), sharedPrivateDir(t)
	cfg := sharedConfig(root, group)
	cfg.RetainAccepted = true
	cfg.SharedStorageBytes = 64 << 20
	cfg.MaxStorageBytes = 64 << 20
	cfg.ReplayBatch = 64
	p, e := OpenOTLPJournal(ctx, cfg, []OTLPDestination{{ID: "home", Send: barrierAccepted}})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 48; i++ {
		if e = p.Admit(ctx, boundedRequest(t, 32+i)); e != nil {
			p.Close()
			t.Fatal("funded initial admission refused", i, e)
		}
	}
	other, e := diskbudget.Open(ctx, group, sharedPrivateDir(t), cfg.SharedStorageBytes)
	if e != nil {
		p.Close()
		t.Fatal(e)
	}
	defer other.Close()
	snap, e := p.budget.Snapshot(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.Reserve(ctx, snap.Limit-snap.ChargedBytes-snap.ReservedBytes); e != nil {
		t.Fatal(e)
	}
	report, e := p.ReplayBatch(ctx)
	if e != nil || !report.Complete || report.Delivered != 48 {
		p.Close()
		t.Fatal("funded full-store pass failed", report, e)
	}
	if !p.store.PackingPending() {
		p.Close()
		t.Fatal("partial32-receipt batch was called complete")
	}
	held, e := p.budget.Snapshot(ctx)
	if e != nil || held.ReservedBytes <= 2<<20 {
		p.Close()
		t.Fatal("unwritten compaction obligations were refunded", held, e)
	}
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	q, e := OpenOTLPJournal(ctx, cfg, []OTLPDestination{{ID: "home", Send: barrierAccepted}})
	if e != nil {
		t.Fatal("funded compaction restart refused", e)
	}
	defer q.Close()
	if !q.store.PackingPending() {
		t.Fatal("cold open forgot uncompacted originals")
	}
	capacityDrain(t, q)
	if q.store.PackingPending() || q.Err() != nil {
		t.Fatal("packing did not finish", q.Err())
	}
	n, e := q.store.WalkAcceptedArchive(ctx, q.namespace, func(_ otlpstate.Key, _ otlpstate.Envelope) error { return nil })
	if e != nil || n != 48 {
		t.Fatal("originals lost across partial batch", n, e)
	}
	final, e := q.budget.Snapshot(ctx)
	if e != nil || final.ChargedBytes+final.ReservedBytes > final.Limit {
		t.Fatal(final, e)
	}
	t.Logf("full-store48 originals: batch32 retained funding through restart, recovered=%d", n)
}
