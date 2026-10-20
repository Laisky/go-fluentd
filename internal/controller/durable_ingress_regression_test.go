//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"testing"
	"time"

	"gofluentd/library"
)

// Exercise the real ingress boundary, rather than assuming every adapter will
// always attach a receipt. An accidental async/bypass publication must fail
// closed under the overall retention contract, including when the disk is free.
func TestOverallDurableIngressRejectsBypass(t *testing.T) {
	for _, route := range []string{"missing-receipt", "skip-dump"} {
		t.Run(route, func(t *testing.T) {
			cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 16 << 20, SegmentBytes: 64 << 10}
			j, ctx := overallLegacy(t, cfg, false)
			dump, skip := make(chan *library.FluentMsg, 1), make(chan *library.FluentMsg, 1)
			out := j.DumpMsgFlow(ctx, j.MsgPool, dump, skip)
			msg := &library.FluentMsg{ID: 42, Tag: "fixture", Message: map[string]interface{}{"synthetic": route}}
			if route == "skip-dump" {
				msg.DurableAck = make(chan error, 1)
				receipt := msg.DurableAck
				skip <- msg
				select {
				case err := <-receipt:
					if err == nil {
						t.Fatal("skip-dump falsely acknowledged")
					}
				case got := <-out:
					t.Fatalf("unpersisted bypass reached downstream: %d", got.ID)
				case <-time.After(time.Second):
					t.Fatal("skip-dump refusal receipt missing")
				}
			} else {
				dump <- msg
				deadline := time.Now().Add(time.Second)
				for j.diskRefused.Load() == 0 && time.Now().Before(deadline) {
					select {
					case got := <-out:
						t.Fatalf("missing-receipt ingress reached downstream: %d", got.ID)
					case <-time.After(time.Millisecond):
					}
				}
				if j.diskRefused.Load() == 0 {
					t.Fatal("missing-receipt refusal invisible")
				}
			}
			if len(out) != 0 {
				t.Fatal("refusal was forwarded")
			}
			if id, err := overallBackend(t, j).LoadMaxId(); err != nil || id != 0 {
				t.Fatalf("bypassed original persisted or budget consumed: %d %v", id, err)
			}
		})
	}
}
