package recvs

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	utils "github.com/Laisky/go-utils"
	"gofluentd/library"
)

// waitDurableSyslog polls synthetic readiness and receipt counters within a finite deadline.
func waitDurableSyslog(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("syslog condition deadline expired")
}

// TestDurableSyslogTCPAndUDPRefusalRecovery uses the actual server and parsed-log admission path.
// Best-effort transport has no log ACK, but capacity rejection remains visible independently.
func TestDurableSyslogTCPAndUDPRefusalRecovery(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	durableCheck.NoError(t, err)
	address := reservation.Addr().String()
	durableCheck.NoError(t, reservation.Close())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewRsyslogRecv(&RsyslogCfg{Name: "synthetic-syslog", Addr: address, Tag: "logs", TimeKey: "timestamp", NewTimeKey: "@timestamp", NewTimeFormat: time.RFC3339Nano, MsgKey: "content"})
	r.SetMsgPool(recvPool())
	r.SetCounter(utils.NewCounter())
	r.SetDurableAdmission(true)
	out := make(chan *library.FluentMsg, 1)
	async := make(chan *library.FluentMsg, 1)
	r.SetSyncOutChan(out)
	r.SetAsyncOutChan(async)
	r.Run(ctx)
	waitDurableSyslog(t, func() bool {
		c, e := net.DialTimeout("tcp", address, 20*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	defer func() {
		cancel()
		waitDurableSyslog(t, func() bool {
			c, e := net.DialTimeout("tcp", address, 20*time.Millisecond)
			if e == nil {
				c.Close()
				return false
			}
			return true
		})
	}()
	for _, protocol := range []string{"tcp", "udp"} {
		t.Run(protocol, func(t *testing.T) {
			c, err := net.DialTimeout(protocol, address, time.Second)
			durableCheck.NoError(t, err)
			defer c.Close()
			body := "<34>Oct 18 17:35:01 synthetic-host app: synthetic original\n"
			_, err = c.Write([]byte(body))
			durableCheck.NoError(t, err)
			msg := recvTake(t, out)
			durableCheck.NotNil(t, msg.DurableAck)
			durableCheck.Empty(t, async)
			durableCheck.Equal(t, "syslog", msg.SourceMetadata["protocol"])
			original := msg.SourceMetadata["parts"].(map[string]interface{})
			if _, ok := original["timestamp"].(time.Time); !ok {
				t.Fatalf("original timestamp type %T", original["timestamp"])
			}
			if _, ok := msg.Message["@timestamp"].(string); !ok {
				t.Fatal("normalized timestamp missing")
			}
			if _, ok := original["@timestamp"]; ok {
				t.Fatal("original metadata mutated by normalizer")
			}
			before := r.DurableAdmissionStats()
			msg.CompleteAcceptance(errors.New("synthetic aggregate capacity"))
			waitDurableSyslog(t, func() bool { return r.DurableAdmissionStats()["refused"] == before["refused"]+1 })
			durableCheck.Equal(t, before["accepted"], r.DurableAdmissionStats()["accepted"])
			durableCheck.NoError(t, c.SetReadDeadline(time.Now().Add(15*time.Millisecond)))
			var b [1]byte
			_, err = c.Read(b[:])
			durableCheck.Error(t, err)
			_, err = c.Write([]byte(body))
			durableCheck.NoError(t, err)
			msg = recvTake(t, out)
			msg.CompleteAcceptance(nil)
			waitDurableSyslog(t, func() bool { return r.DurableAdmissionStats()["accepted"] == before["accepted"]+1 })
		})
	}
}
