//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"context"
	"net"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	"github.com/tinylib/msgp/msgp"
	"gofluentd/internal/recvs"
	"gofluentd/library"
)

// Pair the real socket protocol with the real budgeted writer. Unit tests of
// each boundary alone would miss an adapter accidentally selecting async input.
func TestOverallSharedCapForwardWithholdsACKAndRecovers(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 16 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, false)
	slot, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := slot.Addr().String()
	if err = slot.Close(); err != nil {
		t.Fatal(err)
	}
	r := recvs.NewFluentdRecv(&recvs.FluentdRecvCfg{Name: "synthetic", Addr: addr, Ingress: recvs.FluentIngressCfg{FrameTimeout: time.Second, IdleTimeout: time.Second}})
	r.SetDurableAdmission(true)
	r.SetMsgPool(j.MsgPool)
	r.SetCounter(utils.NewCounter())
	ch, _ := j.tag2JJInchanMap.Load("fixture")
	r.SetSyncOutChan(ch.(chan *library.FluentMsg))
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(rctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Forward listener did not stop")
		}
	})
	dial := func() net.Conn {
		deadline := time.Now().Add(2 * time.Second)
		for {
			c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
			if err == nil {
				return c
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	snap, err := j.diskBudget.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.diskBudget.Reserve(ctx, snap.Limit-snap.ChargedBytes-snap.ReservedBytes); err != nil {
		t.Fatal(err)
	}
	request := func(chunk string, accepted bool) {
		c := dial()
		defer c.Close()
		if err := c.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		w := msgp.NewWriter(c)
		frame := []interface{}{"fixture", int64(123), map[string]interface{}{"synthetic": "original"}, map[string]interface{}{"chunk": chunk}}
		if err := w.WriteIntf(frame); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		value, err := msgp.NewReader(c).ReadIntf()
		if !accepted {
			if err == nil {
				t.Fatalf("full budget produced ACK: %#v", value)
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("full budget did not close failed chunk")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		ack, ok := value.(map[string]interface{})
		if !ok || len(ack) != 1 || ack["ack"] != chunk {
			t.Fatalf("invalid Forward ACK: %#v", value)
		}
	}
	request("refused", false)
	if id, err := overallBackend(t, j).LoadMaxId(); err != nil || id != 0 || len(j.outChan) != 0 {
		t.Fatal("refused chunk persisted or forwarded", id, err)
	}
	if err = j.reconcileOverallBudget(ctx); err != nil {
		t.Fatal(err)
	}
	request("accepted", true)
	select {
	case m := <-j.outChan:
		if m.SourceMetadata["timestamp"] != int64(123) || m.Message["synthetic"] != "original" {
			t.Fatal("accepted original/context changed")
		}
	case <-time.After(time.Second):
		t.Fatal("ACKed chunk missing durable publication")
	}
	stats := r.DurableAdmissionStats()
	if stats["refused"] != 1 || stats["accepted"] != 1 {
		t.Fatal("Forward refusal/progress invisible", stats)
	}
}
