//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"gofluentd/library"
)

func TestOverallDurableSourceMetadataSurvivesRecoveryWithoutPayloadLeak(t *testing.T) {
	cfg := &DiskBudgetConfig{Directory: sharedPrivateDir(t), MaxBytes: 16 << 20, SegmentBytes: 64 << 10}
	j, ctx := overallLegacy(t, cfg, false)
	meta := map[string]interface{}{"protocol": "kafka", "topic": "synthetic", "partition": int64(2), "offset": int64(41), "timestamp": time.Unix(1234, 567).UTC(), "raw_value": []byte("private-source-canary")}
	msg := &library.FluentMsg{ID: 1, Tag: "fixture", Message: map[string]interface{}{"payload": "original"}, SourceMetadata: meta, DurableAck: make(chan error, 1)}
	receipt := msg.DurableAck
	ch, _ := j.tag2JJInchanMap.Load("fixture")
	ch.(chan *library.FluentMsg) <- msg
	select {
	case err := <-receipt:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("durable receipt missing")
	}
	// Live copy cannot be used as evidence of recoverability.
	select {
	case <-j.outChan:
	case <-time.After(time.Second):
		t.Fatal("live publication missing")
	}
	out := make(chan *library.FluentMsg, 8)
	replayCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := j.processLegacyMsg(replayCtx, out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("recovered records=%d", len(out))
	}
	recovered := <-out
	gotTime, validTime := recovered.SourceMetadata["timestamp"].(time.Time)
	if !validTime || !gotTime.Equal(meta["timestamp"].(time.Time)) {
		t.Fatal("source timestamp instant changed")
	}
	// MessagePack timestamps preserve the instant, not Go's location pointer.
	meta["timestamp"] = gotTime
	if !reflect.DeepEqual(recovered.SourceMetadata, meta) {
		t.Fatalf("source context changed: %#v", recovered.SourceMetadata)
	}
	wire, err := recovered.MarshalMsg(nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wire, []byte("private-source-canary")) {
		t.Fatal("source context leaked to MessagePack export")
	}
	wire, err = json.Marshal(recovered)
	if err != nil || bytes.Contains(wire, []byte("private-source-canary")) {
		t.Fatal("source context leaked to JSON export")
	}
	if !reflect.DeepEqual(recovered.Message, map[string]interface{}{"payload": "original"}) {
		t.Fatal("source context collided with application payload")
	}
}
