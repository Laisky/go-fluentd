package recvs

import (
	"github.com/Laisky/go-syslog/format"
	utils "github.com/Laisky/go-utils"
	"net"
	"testing"
	"time"
)

func TestBehaviorRsyslogTransform(t *testing.T) {
	for _, sameKey := range []bool{false, true} {
		timeKey, msgKey := "timestamp", "content"
		if sameKey {
			timeKey = "@timestamp"
			msgKey = "message"
		}
		r := NewRsyslogRecv(&RsyslogCfg{Name: "syslog", Tag: "syslog.prod", TagKey: "tag", TimeKey: timeKey, NewTimeKey: "@timestamp", MsgKey: msgKey, NewTimeFormat: time.RFC3339, TimeShift: time.Hour, RewriteTags: map[string]string{"host": "hostname"}})
		r.SetCounter(utils.NewCounter())
		r.SetMsgPool(behaviorDirtyPool())
		m := r.parseLogPart(format.LogParts{timeKey: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), msgKey: "hello", "host": "node"})
		if m == nil || m.Message["message"] != "hello" || m.Message["@timestamp"] != "2020-01-01T01:00:00Z" || m.Message["hostname"] != "node" || m.Message["tag"] != "syslog.prod" || len(m.ExtIds) != 0 {
			t.Fatalf("same-key rename lost a field: %+v", m)
		}
	}
}
func TestBehaviorRsyslogInvalidTimeRejected(t *testing.T) {
	r := NewRsyslogRecv(&RsyslogCfg{Tag: "logs", TimeKey: "timestamp", NewTimeKey: "@timestamp", MsgKey: "content"})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(behaviorDirtyPool())
	for _, value := range []interface{}{nil, 3, "not-time"} {
		if m := r.parseLogPart(format.LogParts{"timestamp": value, "content": "x"}); m != nil {
			t.Errorf("invalid timestamp was forwarded: %+v", m)
		}
	}
}
func TestBehaviorRsyslogRenameUsesOriginalValues(t *testing.T) {
	r := NewRsyslogRecv(&RsyslogCfg{Tag: "logs", TimeKey: "timestamp", NewTimeKey: "ts", MsgKey: "content", NewTimeFormat: time.RFC3339, RewriteTags: map[string]string{"a": "b", "b": "c", "message": "message"}})
	r.SetCounter(utils.NewCounter())
	r.SetMsgPool(behaviorDirtyPool())
	m := r.parseLogPart(format.LogParts{"timestamp": time.Now(), "content": "body", "a": "A", "b": "B"})
	if m.Message["b"] != "A" || m.Message["c"] != "B" || m.Message["message"] != "body" {
		t.Fatalf("rename order/self-rename damaged data: %v", m.Message)
	}
}
func TestBehaviorRsyslogPartialBindReleasesUDP(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	addr := tcp.Addr().String()
	if srv, _, err := NewRsyslogSrv(addr); err == nil {
		srv.Kill()
		t.Fatal("TCP bind should fail on an occupied port")
	}
	udp, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatalf("failed TCP startup leaked its UDP socket: %v", err)
	}
	udp.Close()
}
